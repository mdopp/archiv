package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The Datenpfleger's pages (pfleger.go): on the start one line „N Dinge
// zum Aufräumen ›“; behind it one finding per screen, with the step in
// the owning app, „Passt so“ and „Nächstes“.

const (
	pflegePfad   = "/tresor/pflege"
	pflegeBilder = 12 // thumbnails on one finding's screen

	saysPflegeLeer  = "Nichts zum Aufräumen. Dein Archiv schaut wieder, während es offen ist."
	saysPflegeWeg   = "Das ist schon erledigt. Hier ist das Nächste."
	saysPflegePasst = "Gemerkt: Das bleibt so. Das Archiv fragt nicht wieder danach."
	saysPflegeZu    = "Das Archiv ist abgeschlossen. Öffne es, dann kannst du weiter aufräumen."
)

// pflegeZeile is the start's line, when there is something to tidy.
func (w *Web) pflegeZeile(b *strings.Builder) {
	innen, auf := w.Tresor.Innen()
	if !auf {
		return
	}
	if n := w.pflege.zaehlen(innen, w.Umfeld.Netz(), w.jetzt(), false); n > 0 {
		fmt.Fprintf(b, `<p class=weiter-in id=pflege-zeile><a href="%s">%s ›</a></p>`, pflegePfad, esc(anzahl(n, "Ding", "Dinge")+" zum Aufräumen"))
	}
}

// pflegeGet is one finding (?n= its place, the first by default).
func (w *Web) pflegeGet(rw http.ResponseWriter, r *http.Request) {
	innen, auf := w.Tresor.Innen()
	if !auf {
		http.Redirect(rw, r, "/tresor", http.StatusSeeOther)
		return
	}
	bs := Befunde(innen, w.Umfeld.Netz())
	w.pflege.zaehlen(innen, w.Umfeld.Netz(), w.jetzt(), true)
	var b strings.Builder
	meldungenText(&b, meldungen[r.URL.Query().Get("m")], "")
	if len(bs) == 0 {
		fmt.Fprintf(&b, `<div class=gut><span class=punkt></span><p>%s</p></div><p class=weiter-in><a href="/tresor">Zurück zum Archiv</a></p>`, esc(saysPflegeLeer))
		seite(rw, http.StatusOK, "Aufräumen", b.String(), "")
		return
	}
	i, _ := strconv.Atoi(r.URL.Query().Get("n"))
	i = max(0, min(i, len(bs)-1))
	w.befundKarte(&b, bs[i], i, len(bs))
	seite(rw, http.StatusOK, "Aufräumen", b.String(), "")
}

// befundKarte is the finding's one screen.
func (w *Web) befundKarte(b *strings.Builder, f Befund, i, n int) {
	fmt.Fprintf(b, `<section class=karte id=befund><p class=k>%s · %d von %d</p><h2>%s</h2>`, esc(f.Bereich), i+1, n, esc(befundTitel(f)))
	if f.Bilder {
		w.befundBilder(b, f.Teile)
	} else if len(f.Zeilen) > 0 {
		b.WriteString(`<ul>`)
		for _, z := range f.Zeilen {
			fmt.Fprintf(b, `<li>%s</li>`, esc(z))
		}
		b.WriteString(`</ul>`)
	}
	fmt.Fprintf(b, `<p><b>So geht's:</b> %s</p>`, esc(f.Schritt))
	fmt.Fprintf(b, `<form method=post action="%s/passt" class=knoepfe><input type=hidden name=id value="%s"><input type=hidden name=n value="%d"><button data-laeuft="Merkt es …">Passt so</button>`, pflegePfad, esc(f.ID), i)
	if i+1 < n {
		fmt.Fprintf(b, `<a class=k href="%s?n=%d">Nächstes ›</a>`, pflegePfad, i+1)
	}
	b.WriteString(`</form></section><p class=weiter-in><a class=leise href="/tresor">Zurück zum Archiv</a></p>`)
}

// befundTitel counts an aggregate's parts in its title.
func befundTitel(f Befund) string {
	if len(f.Teile) > 1 {
		return fmt.Sprintf("%s (%d)", f.Titel, len(f.Teile))
	}
	return f.Titel
}

// befundBilder are the first photos as thumbnails, each opening in Immich.
func (w *Web) befundBilder(b *strings.Builder, ids []string) {
	oeffentlich := ""
	if w.Umfeld != nil {
		oeffentlich = w.Umfeld.Oeffentlich
	}
	b.WriteString(`<div class=bilder>`)
	for _, id := range ids[:min(len(ids), pflegeBilder)] {
		img := fmt.Sprintf(`<img src="%s" loading=lazy decoding=async width=88 height=88 alt="Foto">`, esc(kantePfad+"/bild?"+url.Values{"id": {id}}.Encode()))
		if oeffentlich != "" {
			img = `<a href="` + esc(oeffentlich+"/photos/"+url.PathEscape(id)) + `" target=_blank rel=noopener>` + img + `</a>`
		}
		b.WriteString(img)
	}
	b.WriteString(`</div>`)
	if len(ids) > pflegeBilder {
		fmt.Fprintf(b, `<p class=k>und %d weitere</p>`, len(ids)-pflegeBilder)
	}
}

// pflegePasst is „Passt so“: the finding is remembered in the vault and
// the next one shown in its place.
func (w *Web) pflegePasst(rw http.ResponseWriter, r *http.Request) {
	innen, auf := w.Tresor.Innen()
	if !auf {
		w.antwort(rw, r, http.StatusLocked, "", saysPflegeZu)
		return
	}
	id, n := r.PostFormValue("id"), r.PostFormValue("n")
	bs := Befunde(innen, w.Umfeld.Netz())
	m := "pflege-weg"
	for _, f := range bs {
		if f.ID != id {
			continue
		}
		if err := PasstSo(innen, f, w.jetzt()); err != nil {
			log.Printf("archiv: Datenpfleger: Passt so nicht gespeichert: %v", err)
			w.antwort(rw, r, http.StatusInternalServerError, "", saysUmfeldVerbergen)
			return
		}
		m = "pflege-passt"
	}
	w.pflege.zaehlen(innen, w.Umfeld.Netz(), w.jetzt(), true)
	if w.Geaendert != nil {
		w.Geaendert() // the count for Homes
	}
	http.Redirect(rw, r, pflegePfad+"?"+url.Values{"n": {n}, "m": {m}}.Encode(), http.StatusSeeOther)
}

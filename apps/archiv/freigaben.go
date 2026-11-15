package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/mdopp/archiv/apps/archiv/tresor"
)

// What the person grants, and what the programs did with it (archiv ADR
// 0001 §3): on the closed page, a folded block above the opening forms
// with a box per program, area and right, ticked as last time; while
// open, /tresor/freigaben shows this session's grants and its protocol.

const freigabenPfad = "/tresor/freigaben"

const (
	saysFreigabenWozu    = "Beim Öffnen erlaubst du jedem Programm deines Archivs nur die Bereiche, die du hier ankreuzt, für diese eine Sitzung. Abschließen beendet alle Erlaubnisse sofort."
	saysFreigabenAnders  = "Andere Freigaben gelten ab dem nächsten Öffnen: Schließ das Archiv ab und öffne es mit den neuen Haken."
	saysNichtFreigegeben = "Das hast du diesem Programm in dieser Sitzung nicht erlaubt. Schließ das Archiv ab und öffne es wieder, mit dem Haken bei diesem Bereich."
)

// freigabenWahl is the closed page's block: the boxes belong to the
// password form (form=mit-passwort); the passkey's form takes them along.
func (w *Web) freigabenWahl(b *strings.Builder) {
	f := w.Freigaben.Letzte()
	alle := 0
	for _, p := range programme {
		alle += len(p.will)
	}
	gewaehlt := 0
	for _, r := range f {
		gewaehlt += len(r)
	}
	zeile := "Alle Programme dürfen, was sie brauchen"
	if gewaehlt < alle {
		zeile = "Wie beim letzten Mal, mit Einschränkungen"
	}
	fmt.Fprintf(b, `<details class=freigaben id=freigaben><summary>Freigaben: %s</summary><p class=k>%s</p>`, esc(zeile), esc(saysFreigabenWozu))
	b.WriteString(`<input type=hidden name=freigaben value=1 form=mit-passwort>`)
	for _, p := range programme {
		fmt.Fprintf(b, `<fieldset><legend><b>%s</b></legend><p class=k>%s</p>`, esc(p.name), esc(p.wozu))
		for _, ber := range tresor.Bereiche {
			x := p.will[ber]
			if x == 0 {
				continue
			}
			fmt.Fprintf(b, `<p class=zeile>%s`, esc(bereichNamen[ber]))
			for _, rr := range []struct {
				r     tresor.Recht
				k, wo string
			}{{l, "l", "lesen"}, {s, "s", "schreiben"}} {
				if !x.Hat(rr.r) {
					continue
				}
				an := ""
				if f[p.id][ber].Hat(rr.r) {
					an = " checked"
				}
				fmt.Fprintf(b, ` <label class=haken><input type=checkbox form=mit-passwort name="f.%s.%s.%s" value=1%s> %s</label>`, esc(p.id), esc(string(ber)), rr.k, an, rr.wo)
			}
			b.WriteString(`</p>`)
		}
		b.WriteString(`</fieldset>`)
	}
	b.WriteString(`</details>`)
}

// freigabenZeile is the open workplace's quiet line to this session's grants.
func freigabenZeile(b *strings.Builder) {
	fmt.Fprintf(b, `<p class=weiter-in><a class=leise href="%s">Was die Programme in dieser Sitzung dürfen ›</a></p>`, freigabenPfad)
}

// freigabenSeite: this session's grants, and its protocol.
func (w *Web) freigabenSeite(rw http.ResponseWriter, r *http.Request) {
	s, ok := w.Tresor.Sitzung()
	if !ok {
		http.Redirect(rw, r, "/tresor", http.StatusSeeOther)
		return
	}
	var b strings.Builder
	b.WriteString(oben("Freigaben", `<a class=leise href="/tresor">Zurück</a>`))
	fmt.Fprintf(&b, `<section class=karte><h2>Diese Sitzung</h2><p>Geöffnet um %s, endet spätestens um %s.</p><ul>`, esc(uhrzeit(s.Seit)), esc(uhrzeit(s.Bis)))
	for _, p := range programme {
		var teile []string
		for _, ber := range tresor.Bereiche {
			if x := s.Freigaben[p.id][ber]; x != 0 {
				teile = append(teile, bereichNamen[ber]+" "+x.String())
			}
		}
		was := "nichts"
		if len(teile) > 0 {
			was = strings.Join(teile, ", ")
		}
		fmt.Fprintf(&b, `<li><b>%s</b>: %s</li>`, esc(p.name), esc(was))
	}
	fmt.Fprintf(&b, `</ul><p class=k>%s</p></section>`, esc(saysFreigabenAnders))
	u := w.Tresor.Zugriffe()
	b.WriteString(`<section class=karte><h2>Protokoll</h2><p class=k>Jeder Zugriff eines Programms steht im Protokoll deines Tresors, auch die abgelehnten. Abgeschlossen kann es niemand lesen.</p>`)
	if len(u.Zaehler) == 0 {
		b.WriteString(`<p>Noch kein Zugriff in dieser Sitzung.</p>`)
	}
	b.WriteString(`<ul>`)
	for _, z := range u.Zaehler {
		zeile := fmt.Sprintf("%s: %s %s, %d erlaubt", programmName(z.Programm), bereichNamen[z.Bereich], z.Recht, z.Erlaubt)
		if z.Verweigert > 0 {
			zeile += fmt.Sprintf(", %d abgelehnt", z.Verweigert)
		}
		fmt.Fprintf(&b, `<li>%s</li>`, esc(zeile))
	}
	b.WriteString(`</ul>`)
	if len(u.Letzte) > 0 {
		b.WriteString(`<details><summary>Die letzten Zugriffe</summary><ul class=k>`)
		for _, z := range u.Letzte {
			if z.Bereich == "" {
				continue
			}
			zeile := fmt.Sprintf("%s · %s · %s %s · %s", uhrzeit(z.Am), programmName(z.Programm), bereichNamen[z.Bereich], z.Recht, z.Was)
			if z.Verweigert != "" {
				zeile += " · abgelehnt: " + z.Verweigert
			}
			fmt.Fprintf(&b, `<li>%s</li>`, esc(zeile))
		}
		b.WriteString(`</ul></details>`)
	}
	b.WriteString(`</section>`)
	seite(rw, http.StatusOK, "Freigaben", b.String(), "")
}

func programmName(id string) string {
	for _, p := range programme {
		if p.id == id {
			return p.name
		}
	}
	return id
}

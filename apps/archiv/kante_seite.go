package main

import (
	"cmp"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
)

// „Was verbindet die beiden?“ (archiv#27 steps 4 and 7): one page per pair,
// /tresor/kante?a=…&b=…, reached with one tap from a Steht nahe row, a
// person's detail, a question card („Warum?“) and every line of the
// Stammbaum. On top both faces and the relation as the question card's
// chips, the current kind chosen; one tap sets it (Festlegen: the same
// write to Homes, the same chain, the same Rückgängig). Under it the
// shared photo days as thumbnails (Immich's, through the archive with the
// person's own key, never kept; a tap opens the photo in Immich), the
// shared group chats and mail threads with their last line (a tap opens
// it in msgvault's view), the cue the box's model read first, and why
// the two are tied. „+ Beziehung“ picks anyone else by name. No script
// needed; the page's script only sends a chip in the background.

const (
	kantePfad           = "/tresor/kante"
	saysKanteWeg        = "Diese Verbindung gibt es nicht (mehr): Eine der beiden Personen ist ausgeblendet oder nicht mehr im Umfeld."
	saysKanteNiemand    = "Niemanden mit diesem Namen gefunden. Versuch es mit dem Vor- oder Nachnamen."
	saysKanteSichSelbst = "Das ist dieselbe Person. Wähle eine andere."
	saysKantePruefen    = "Diese Linie passt nicht zu den anderen im Stammbaum (sie ergäbe einen Kreis oder eine andere Generation). Stimmt sie? Ein Tipp bessert sie nach."
	saysKanteFotosOhne  = "Keine gemeinsamen Fotos."
	saysKanteZurueck    = "Rückgängig gemacht."
)

// kanteAdresse is the edge page of a and b.
func kanteAdresse(a, b string) string {
	return kantePfad + "?" + url.Values{"a": {a}, "b": {b}}.Encode()
}

// kanteGet is the edge page; with a name instead of an id (von, wer:
// „+ Beziehung“), it finds the person first: one goes straight to the
// page, several are offered, none says so.
func (w *Web) kanteGet(rw http.ResponseWriter, r *http.Request) {
	if _, auf := w.Tresor.Innen(); !auf {
		http.Redirect(rw, r, "/tresor", http.StatusSeeOther)
		return
	}
	n := w.Umfeld.Netz()
	if n == nil {
		seite(rw, http.StatusOK, "Was verbindet die beiden?", `<p class=ruhig><span class="punkt grau"></span>`+esc(saysUmfeldNochNicht)+`</p>`, "")
		return
	}
	q := r.URL.Query()
	a, b := q.Get("a"), q.Get("b")
	for _, s := range []struct {
		id   *string
		name string
	}{{&a, q.Get("von")}, {&b, q.Get("wer")}} {
		if *s.id != "" || strings.TrimSpace(s.name) == "" {
			continue
		}
		ks := n.Finden(s.name)
		switch len(ks) {
		case 0:
			w.kanteWahl(rw, n, a, nil, saysKanteNiemand)
			return
		case 1:
			*s.id = ks[0].ID
		default:
			if a == "" {
				a = n.Ich()
			}
			w.kanteWahl(rw, n, a, ks, "")
			return
		}
	}
	if a == "" {
		a = n.Ich()
	}
	if a != "" && a == b {
		w.kanteWahl(rw, n, a, nil, saysKanteSichSelbst)
		return
	}
	if q.Get("a") != a || q.Get("b") != b { // found by name: the page's own address
		http.Redirect(rw, r, kanteAdresse(a, b), http.StatusSeeOther)
		return
	}
	w.kanteSeite(rw, r, http.StatusOK, a, b, meldungen[q.Get("m")], "")
}

// kanteWahl is „Welche Person meinst du?“: each of ks a link to its edge
// with a, or (none) the sentence and the form again.
func (w *Web) kanteWahl(rw http.ResponseWriter, n *Netz, a string, ks []Knoten, fehler string) {
	var b strings.Builder
	meldungenText(&b, "", fehler)
	if len(ks) > 0 {
		b.WriteString(`<section class=karte><h2>Welche Person meinst du?</h2><ul class=leute>`)
		for _, k := range ks {
			fmt.Fprintf(&b, `<li><a href="%s">%s<span class=wer>%s</span></a></li>`, esc(kanteAdresse(a, k.ID)), gesicht(k.Name), esc(k.Name))
		}
		b.WriteString(`</ul></section>`)
	}
	if k, ok := n.Person(a); ok {
		b.WriteString(beziehungDazu(n, k, true))
	}
	seite(rw, http.StatusOK, "Beziehung ergänzen", b.String(), "")
}

// kanteSeite is the page of a and b, with meldung or fehler.
func (w *Web) kanteSeite(rw http.ResponseWriter, r *http.Request, code int, a, b, meldung, fehler string) {
	n := w.Umfeld.Netz()
	p, ok := n.Paar(a, b)
	if !ok {
		seite(rw, http.StatusNotFound, "Was verbindet die beiden?", `<p class=ruhig><span class="punkt grau"></span>`+esc(saysKanteWeg)+`</p><p class=weiter-in><a href="/tresor?tab=stammbaum">Zum Stammbaum</a></p>`, "")
		return
	}
	hier := kanteAdresse(p.X.ID, p.Y.ID)
	var s strings.Builder
	meldungenText(&s, meldung, fehler)
	fmt.Fprintf(&s, `<div class=paar><a href="%s">%s<span>%s</span></a><span class=k aria-hidden=true>↔</span><a href="%s">%s<span>%s</span></a></div>`,
		esc(umfeldMitte(p.X.ID)), gesicht(p.X.Name), esc(n.Anzeige(p.X)), esc(umfeldMitte(p.Y.ID)), gesicht(p.Y.Name), esc(n.Anzeige(p.Y)))
	w.kanteFrage(&s, n, p, hier, r.URL.Query().Get("pruefen") != "")
	s.WriteString(w.toast(n, r.URL.Query().Get("getan"), hier))
	w.kanteFotos(&s, p)
	w.kanteNachrichten(&s, r, p)
	if len(p.Gruende) > 0 {
		s.WriteString(`<section class=karte id=warum><h2>Warum</h2><ul>`)
		for _, g := range p.Gruende {
			fmt.Fprintf(&s, `<li>%s</li>`, esc(g))
		}
		s.WriteString(`</ul></section>`)
	}
	s.WriteString(beziehungDazu(n, p.X, false))
	seite(rw, code, "Was verbindet die beiden?", s.String(), "")
}

// kanteFrage is the relation as a form: „X ist für Y …“, the current
// kind chosen, a suggestion marked; where it comes from; on a line the
// Stammbaum could not place (pruefen), why to look.
func (w *Web) kanteFrage(s *strings.Builder, n *Netz, p PaarSicht, hier string, pruefen bool) {
	vorschlag := ""
	if p.Frage != nil {
		vorschlag = p.Frage.Vorschlag
	}
	fmt.Fprintf(s, `<form method=post action="%s" class="frage sofort" id=frage><input type=hidden name=x value="%s"><input type=hidden name=y value="%s"><input type=hidden name=zurueck value="%s">`,
		antwortPfad, esc(p.X.ID), esc(p.Y.ID), esc(hier))
	fmt.Fprintf(s, `<h2>%s</h2><div class=chips>`, esc(p.X.Name+" ist für "+map[bool]string{true: "dich", false: p.Y.Name}[p.Ich]+" …"))
	klasse := func(a string) string {
		switch {
		case a == p.Art:
			return " gewaehlt"
		case a == vorschlag:
			return " vorschlag"
		}
		return ""
	}
	var vorn []string
	for _, a := range append([]string{p.Art, vorschlag}, haeufig...) {
		if slices.Contains(arten, a) && len(vorn) < 5 && !slices.Contains(vorn, a) {
			vorn = append(vorn, a)
		}
	}
	for _, a := range vorn {
		s.WriteString(chipGewaehlt(a, kurzWort(a), klasse(a)))
	}
	s.WriteString(chipGewaehlt(artKeine, "keine Beziehung", klasse(artKeine)) + chipGewaehlt(artFremd, "kennen sich nicht", klasse(artFremd)) +
		`<details class=mehr><summary>mehr …</summary><div class=chips>`)
	for _, a := range arten {
		if !slices.Contains(vorn, a) {
			s.WriteString(chipGewaehlt(a, kurzWort(a), klasse(a)))
		}
	}
	s.WriteString(`</div></details></div>`)
	switch {
	case p.Art == "":
		s.WriteString(`<p class=k>Noch keine Beziehung eingetragen.</p>`)
	case p.Quelle == "buch":
		fmt.Fprintf(s, `<p class=k>Jetzt: %s, aus dem Telefonbuch.</p>`, esc(artJetzt(p.Art)))
	case p.Quelle == "gefolgert":
		fmt.Fprintf(s, `<p class=k>Jetzt: %s, folgt aus einer deiner Antworten, weil %s.</p>`, esc(artJetzt(p.Art)), esc(p.Weil))
	default:
		fmt.Fprintf(s, `<p class=k>Jetzt: %s, von dir angegeben.</p>`, esc(artJetzt(p.Art)))
	}
	if p.Frage != nil && vorschlag != "" && vorschlag != p.Art {
		was := "Vorschlag"
		if p.Frage.Abgeleitet {
			was = "Abgeleitet"
		}
		fmt.Fprintf(s, `<p class=k>%s</p>`, esc(was+": „"+kurzWort(vorschlag)+"“, weil "+p.Frage.Weil+". Ein Tipp auf den markierten Knopf bestätigt es."))
	}
	if pruefen {
		fmt.Fprintf(s, `<p class="meldung fehler">%s</p>`, esc(saysKantePruefen))
	}
	punkt, satz := w.wohinSatz(n.Uebernahme(p.X.ID, p.Y.ID, ""))
	fmt.Fprintf(s, `<p class=wohin><span class="punkt %s"></span>%s</p></form>`, punkt, esc(satz))
}

// artJetzt is a kind or Keine/Fremd as the page's „Jetzt:“ says it.
func artJetzt(art string) string {
	switch art {
	case artKeine:
		return "keine Beziehung"
	case artFremd:
		return "kennen sich nicht"
	}
	return kurzWort(art)
}

// chipGewaehlt is a chip; the current kind's is pressed.
func chipGewaehlt(art, text, klasse string) string {
	gedrueckt := ""
	if klasse == " gewaehlt" {
		gedrueckt = " aria-pressed=true"
	}
	return fmt.Sprintf(`<button name=art value=%s class="chip%s"%s data-laeuft="%s">%s</button>`, art, klasse, gedrueckt, esc(text+" …"), esc(text))
}

// kanteFotos are the shared photo days, the newest first, each day's
// thumbnails loaded lazily through the archive; a tap opens the photo in
// Immich (IMMICH_PUBLIC_URL), without it the thumbnail is all.
func (w *Web) kanteFotos(s *strings.Builder, p PaarSicht) {
	fmt.Fprintf(s, `<section class=karte id=fotos><h2>Fotos%s</h2>`, map[bool]string{true: " (" + anzahlT(p.FotoTage, "Tag", "Tage") + ")"}[p.FotoTage > 0])
	if len(p.Fotos) == 0 {
		fmt.Fprintf(s, `<p class=ruhig><span class="punkt grau"></span>%s</p></section>`, esc(saysKanteFotosOhne))
		return
	}
	oeffentlich := ""
	if w.Umfeld != nil {
		oeffentlich = w.Umfeld.Oeffentlich
	}
	s.WriteString(`<div class=fototage>`)
	for _, t := range p.Fotos {
		tag := datumVon(t.Tag).Format("02.01.2006")
		fmt.Fprintf(s, `<div class=fototag><span class=k>%s</span><div class=bilder>`, tag)
		for _, id := range t.IDs {
			img := fmt.Sprintf(`<img src="%s" loading=lazy decoding=async width=88 height=88 alt="%s">`, esc(kantePfad+"/bild?"+url.Values{"id": {id}}.Encode()), esc("Foto vom "+tag))
			if oeffentlich != "" {
				img = `<a href="` + esc(oeffentlich+"/photos/"+url.PathEscape(id)) + `" target=_blank rel=noopener>` + img + `</a>`
			}
			s.WriteString(img)
		}
		s.WriteString(`</div></div>`)
	}
	s.WriteString(`</div>`)
	if p.FotoTage > len(p.Fotos) {
		fmt.Fprintf(s, `<p class=k>Die %d neuesten von %s.</p>`, len(p.Fotos), esc(anzahlT(p.FotoTage, "Tag", "Tagen")))
	}
	s.WriteString(`</section>`)
}

// kanteNachrichten are the cues first („Hallo Mama“), then the shared
// group chats and mail threads with their last line, each a link into
// msgvault's own view.
func (w *Web) kanteNachrichten(s *strings.Builder, r *http.Request, p PaarSicht) {
	if len(p.Zitate) == 0 && len(p.Gespraeche) == 0 {
		return
	}
	var letzte map[string]letzteNachricht
	if w.Umfeld != nil && w.Umfeld.Msgvault != nil && len(p.Gespraeche) > 0 {
		var ids []string
		for _, g := range p.Gespraeche {
			ids = append(ids, g.ID)
		}
		var err error
		if letzte, err = w.Umfeld.Msgvault.Letzte(r.Context(), ids); err != nil && !errors.Is(err, errNichtBereit) && !errors.Is(err, errGespraecheSpaeter) {
			log.Printf("archiv: Kante: letzte Nachrichten aus msgvault: %v", err)
		}
	}
	s.WriteString(`<section class=karte id=nachrichten><h2>Nachrichten</h2><ul class=gespraeche>`)
	for _, z := range p.Zitate {
		fmt.Fprintf(s, `<li><a href="%s"><b>%s</b><span class=k>%s</span></a></li>`, esc(nachrichtAdresse(z.ID)), esc("„"+z.Text+"“"),
			esc("Hinweis auf die Beziehung, in einer Nachricht vom "+z.Am.Format("02.01.2006")))
	}
	for _, g := range p.Gespraeche {
		titel := g.Titel
		switch {
		case g.Art == gespraechMail:
			titel = "Mail-Verlauf mit " + anzahl(g.Groesse, "Person", "Personen")
		case titel == "":
			titel = "Gruppe mit " + anzahl(g.Groesse, "Person", "Personen")
		}
		ziel, zeile := msgvaultSuche(cmp.Or(g.Titel, p.X.Name)), anzahlT(g.Nachrichten, "Nachricht", "Nachrichten")+", zuletzt am "+g.Zuletzt.Format("02.01.2006")
		if l, da := letzte[g.ID]; da && nurZiffern.MatchString(l.ID) {
			ziel = nachrichtAdresse(l.ID)
			text := bisZu(l.Text, letzteZeichen)
			if l.VonMir {
				text = "Du: " + text
			}
			zeile = cmp.Or(text, "(ohne Text)") + " · " + l.Am.Format("02.01.2006")
		}
		fmt.Fprintf(s, `<li><a href="%s"><b>%s</b><span class=k>%s</span></a></li>`, esc(ziel), esc(titel), esc(zeile))
	}
	s.WriteString(`</ul></section>`)
}

// nachrichtAdresse is a message in msgvault's own view, in its conversation.
func nachrichtAdresse(id string) string {
	if !nurZiffern.MatchString(id) {
		return msgvaultSuche("")
	}
	return "/messages/" + id
}

// beziehungDazu is „+ Beziehung“: anyone by name (the browser suggests
// the net's names; the archive finds umlauts alike), then the edge page
// of k and that person, where one tap sets the kind. offen: unfolded.
func beziehungDazu(n *Netz, k Knoten, offen bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<details class=karte id=dazu%s><summary>+ Beziehung von %s</summary><form method=get action="%s" class=feld><input type=hidden name=a value="%s"><label>Zu wem?<input name=wer list=namen autocomplete=off required placeholder="Name"></label><button>Weiter</button></form>`,
		offenWenn(offen), esc(map[bool]string{true: "dir", false: k.Name}[k.ID == n.Ich()]), kantePfad, esc(k.ID))
	b.WriteString(namenListe(n))
	b.WriteString(`</details>`)
	return b.String()
}

// namenListe is the datalist of the net's names.
func namenListe(n *Netz) string {
	var b strings.Builder
	b.WriteString(`<datalist id=namen>`)
	for _, name := range n.Namen() {
		fmt.Fprintf(&b, `<option value="%s">`, esc(name))
	}
	b.WriteString(`</datalist>`)
	return b.String()
}

// kanteBild is a thumbnail of the net's photos, read from Immich with the
// person's key from the vault for this one answer; nothing is kept on the
// box (the browser may keep it, private, an hour).
func (w *Web) kanteBild(rw http.ResponseWriter, r *http.Request) {
	innen, auf := w.Tresor.Innen()
	id := r.URL.Query().Get("id")
	if !auf || w.Umfeld == nil || !w.Umfeld.Netz().HatFoto(id) || !immichID(id) {
		http.NotFound(rw, r)
		return
	}
	key, err := os.ReadFile(innen.ImmichSchluessel())
	if err != nil || len(trimNL(key)) == 0 {
		http.NotFound(rw, r)
		return
	}
	defer nullen(key)
	bild, typ, err := (&Immich{URL: w.Umfeld.URL, Key: string(trimNL(key))}).Vorschau(r.Context(), id)
	if err != nil {
		log.Printf("archiv: Kante: Vorschau aus Immich: %v", err)
		http.Error(rw, "", http.StatusBadGateway)
		return
	}
	rw.Header().Set("Content-Type", typ)
	rw.Header().Set("Cache-Control", "private, max-age=3600")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Write(bild)
}

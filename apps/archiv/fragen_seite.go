package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// „Wie steht X zu Y?“ (archiv#27 step 2, owner 2026-11-10: one tap): on the
// main page's Fragen one question at a time, a card with both faces,
// „X ist für Y …“, the kinds as chips (the suggestion marked, „mehr …“ for
// the rest, „kennen sich nicht“), why it is suggested and where the answer
// goes. One tap on a chip saves it in the vault and has Homes write it
// (uebernahme.go); the page comes back with the next question and a
// toast „✓ … eingetragen · Rückgängig“, which undoes both. Every chip is a
// form post (no script needed); the page's script sends it in the
// background and swaps the page in place. On /tresor/umfeld „Deine
// Antworten“ lists every answer with Vergessen (the same undo) and, for
// one Homes did not take, Eintragen.

const (
	saysFrageWeg      = "Diese Frage gibt es nicht mehr; das Umfeld wurde inzwischen neu gerechnet."
	saysFrageOhneWahl = "Bitte wähle zuerst, wie die beiden zueinander stehen."
	saysNichtZurueck  = "Das ließ sich nicht rückgängig machen: Homes antwortet gerade nicht. Versuch es gleich noch einmal."
	saysNichtEin      = "Homes hat es gerade nicht eingetragen. Versuch es gleich noch einmal."
	antwortPfad       = "/tresor/umfeld/antwort"
)

// haeufig are the kinds offered first, after the suggestion.
var haeufig = []string{"partner", "eltern", "kind", "geschwister", "freund", "kollege", "bekannt"}

// duOder is a node as a sentence names it: „dir“ for the person.
func duOder(k Knoten, ich bool) string {
	if ich {
		return "dir"
	}
	return k.Name
}

// gesicht is a person's initials on a colour of their name.
func gesicht(name string) string {
	ws, kurz := strings.Fields(name), "?"
	if len(ws) > 0 {
		kurz = strings.ToUpper(string([]rune(ws[0])[:1]))
	}
	if len(ws) > 1 {
		kurz += strings.ToUpper(string([]rune(ws[len(ws)-1])[:1]))
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	return fmt.Sprintf(`<span class=av style="--h:%d" aria-hidden=true>%s</span>`, h.Sum32()%360, esc(kurz))
}

// wohinSatz says where an answer about the pair will go.
func (w *Web) wohinSatz(u Uebernahme) (punkt, satz string) {
	switch {
	case u.Wohin == "" && len(u.Ohne) == 1:
		return "grau", "bleibt in deinem Archiv: " + u.Ohne[0] + " steht in keinem Adressbuch, nur auf deinen Fotos"
	case u.Wohin == "":
		return "grau", "bleibt in deinem Archiv: die beiden stehen in keinem Adressbuch, nur auf deinen Fotos"
	case !w.homesVerbunden():
		return "grau", "bleibt vorerst in deinem Archiv: Homes ist noch nicht mit ihm verbunden"
	case u.Wohin == wohinHaushalt:
		return "familie", "kommt ins Adressbuch der Familie"
	}
	return "", "kommt nur auf dein Handy"
}

// nochZu is the portion's rest, the card's footer: „noch 3 zum Nachbessern“.
func nochZu(rest int) string {
	if rest == 1 {
		return "die letzte zum Nachbessern"
	}
	return fmt.Sprintf("noch %d zum Nachbessern", rest)
}

// frageKarte is the question f as one form: of the visit's portion with
// rest left in it, or (mehr) one beyond it, with a way out, no count.
func (w *Web) frageKarte(n *Netz, f Frage, rest int, mehr bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<form method=post action="%s" class="frage sofort" id=frage><input type=hidden name=x value="%s"><input type=hidden name=y value="%s"><input type=hidden name=zurueck value=/tresor>`,
		antwortPfad, esc(f.X.ID), esc(f.Y.ID))
	if mehr {
		b.WriteString(`<input type=hidden name=mehr value=1>`)
	}
	fmt.Fprintf(&b, `<div class=gesichter>%s<span class=k>↔</span>%s</div><h2>%s</h2><div class=chips>`, gesicht(f.X.Name), gesicht(f.Y.Name),
		esc(f.X.Name+" ist für "+map[bool]string{true: "dich", false: f.Y.Name}[f.Ich]+" …"))
	var vorn []string
	for _, a := range append([]string{f.Vorschlag}, haeufig...) {
		if a != "" && len(vorn) < 4 && !slices.Contains(vorn, a) {
			vorn = append(vorn, a)
		}
	}
	for _, a := range vorn {
		b.WriteString(chip(a, kurzWort(a), map[bool]string{true: " vorschlag"}[a == f.Vorschlag]))
	}
	b.WriteString(chip(artFremd, "kennen sich nicht", "") + `<details class=mehr><summary>mehr …</summary><div class=chips>`)
	for _, a := range arten {
		if !slices.Contains(vorn, a) {
			b.WriteString(chip(a, kurzWort(a), ""))
		}
	}
	b.WriteString(chip(artKeine, "keine Beziehung", "") + `</div></details></div>`)
	if f.Weil != "" {
		fmt.Fprintf(&b, `<p class=k>%s</p>`, esc("Vorschlag, weil "+f.Weil+"."))
	}
	punkt, satz := w.wohinSatz(n.Uebernahme(f.X.ID, f.Y.ID, ""))
	fmt.Fprintf(&b, `<p class=wohin><span class="punkt %s"></span>%s</p>`, punkt, esc(satz))
	fuss := `<span class=k>` + esc(nochZu(rest)) + `</span>`
	if mehr {
		fuss = `<a class=k href="/tresor?tab=fragen">Genug für jetzt</a>`
	}
	fmt.Fprintf(&b, `<div class=fuss><button name=art value=spaeter class=leise data-laeuft="Legt es nach hinten …">Später</button>%s</div></form>`, fuss)
	return b.String()
}

func chip(art, text, klasse string) string {
	return fmt.Sprintf(`<button name=art value=%s class="chip%s" data-laeuft="%s">%s</button>`, art, klasse, esc(text+" …"), esc(text))
}

// gegebenSatz is an answer as a sentence: „Clara ist Kind von dir.“
func gegebenSatz(g Gegeben) string {
	switch {
	case g.Art == artKeine && g.Ich:
		return fmt.Sprintf("Du und %s: keine Beziehung.", g.X.Name)
	case g.Art == artKeine:
		return fmt.Sprintf("%s und %s: keine Beziehung.", g.X.Name, g.Y.Name)
	case g.Art == artFremd && g.Ich:
		return fmt.Sprintf("Du kennst %s nicht.", g.X.Name)
	case g.Art == artFremd:
		return fmt.Sprintf("%s und %s kennen sich nicht.", g.X.Name, g.Y.Name)
	}
	return fmt.Sprintf("%s ist %s von %s.", g.X.Name, kurzWort(g.Art), duOder(g.Y, g.Ich))
}

// standSatz says what became of an answer: gemerkt, eingetragen, or why not.
func (w *Web) standSatz(g Gegeben) string {
	switch {
	case !slices.Contains(arten, g.Art):
		return "gemerkt"
	case g.Eingetragen != nil && g.Eingetragen.Wohin == wohinHandy:
		return "eingetragen, kommt auf dein Handy"
	case g.Eingetragen != nil:
		return "eingetragen"
	case g.Karte.Wohin != "" && w.homesVerbunden():
		return "noch nicht eingetragen: Homes antwortet gerade nicht"
	}
	_, satz := w.wohinSatz(g.Karte)
	return satz
}

// toast is „✓ … eingetragen · Rückgängig“ for the answer key, with the
// chain of what followed from it („daraus: weil …, ist …“); it goes
// after ten seconds (the stylesheet), the answer stays undoable under
// „Deine Antworten“. One Rückgängig takes back the answer and its chain.
func (w *Web) toast(n *Netz, key, zurueck string) string {
	g, ok := n.Gegebenes(key)
	if !ok {
		return ""
	}
	text := strings.TrimSuffix(gegebenSatz(g), ".") + " · " + w.standSatz(g)
	for _, d := range n.Gefolgerte(key) {
		text += " · daraus: weil " + d.Weil + ", ist " + d.X.Name + " " + kurzWort(d.Art) + " von " + duOder(d.Y, d.Ich)
	}
	return fmt.Sprintf(`<div class=toast role=status><span>✓ %s</span><form method=post action="%s" class=sofort><input type=hidden name=paar value="%s"><input type=hidden name=zurueck value="%s"><button name=tu value=vergessen class=leise data-laeuft="Nimmt zurück …">Rückgängig</button></form></div>`,
		esc(text), antwortPfad, esc(key), esc(zurueck))
}

// antwortenListe is „Deine Antworten“: each with what the phone books say,
// what became of it, Eintragen when Homes has not, and Vergessen.
func (w *Web) antwortenListe(b *strings.Builder, n *Netz, zurueck string) {
	gs := n.Gegebene()
	if len(gs) == 0 {
		return
	}
	b.WriteString(`<section id=antworten class=karte><h2>Deine Antworten</h2><ul>`)
	for _, g := range gs {
		fmt.Fprintf(b, `<li class=tu><div>%s<span class=k>%s`, esc(gegebenSatz(g)), esc(w.standSatz(g)))
		if g.Buch != "" {
			fmt.Fprintf(b, ` · im Telefonbuch: %s`, esc(g.Buch))
		}
		fmt.Fprintf(b, `</span></div><form method=post action="%s" class="knoepfe sofort"><input type=hidden name=paar value="%s"><input type=hidden name=zurueck value="%s">`, antwortPfad, esc(g.Key), esc(zurueck))
		if g.Eingetragen == nil && g.Karte.Wohin != "" && w.homesVerbunden() {
			b.WriteString(`<button name=tu value=eintragen data-laeuft="Trägt ein …">Eintragen</button>`)
		}
		b.WriteString(`<button name=tu value=vergessen class=leise data-laeuft="Vergisst …">Vergessen</button></form></li>`)
	}
	b.WriteString(`</ul><p class=k>Vergessen nimmt eine Antwort zurück, auch aus dem Adressbuch, und die Frage kommt wieder.</p></section>`)
}

// zurueckNach is where an answer's form goes back to: the main page's
// Fragen or the Umfeld page, never elsewhere.
func zurueckNach(z string) string {
	if strings.HasPrefix(z, "/tresor/umfeld") {
		return "/tresor/umfeld"
	}
	return "/tresor"
}

func mitAnfrage(pfad string, v url.Values) string {
	if pfad == "/tresor" {
		v.Set("tab", "fragen")
	}
	return pfad + "?" + v.Encode()
}

// umfeldAntwort takes a chip (x, y, art), an answer's Vergessen or
// Eintragen (paar, tu), and goes back where it came from with the toast.
func (w *Web) umfeldAntwort(rw http.ResponseWriter, r *http.Request) {
	innen, ok := w.Tresor.Innen()
	if !ok {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
		return
	}
	zurueck, key := zurueckNach(r.PostFormValue("zurueck")), r.PostFormValue("paar")
	weiter := func(v url.Values) string { // „weitere Fragen“ goes on with the next one beyond today's
		if r.PostFormValue("mehr") == "1" && zurueck == "/tresor" {
			v.Set("mehr", "1")
		}
		return mitAnfrage(zurueck, v)
	}
	switch r.PostFormValue("tu") {
	case "vergessen":
		if err := w.ausHomes(r.Context(), innen, key); err != nil {
			log.Printf("archiv: Umfeld: Rückgängig nicht an Homes: %v", err)
			w.zurueckFehler(rw, r, zurueck, http.StatusBadGateway, saysNichtZurueck)
			return
		}
		if err := w.Umfeld.Vergessen(innen, key); err != nil {
			w.antwortFehler(rw, r, zurueck, err)
			return
		}
		http.Redirect(rw, r, weiter(url.Values{"m": {"zurueck"}}), http.StatusSeeOther)
		return
	case "eintragen":
		if err := w.eintragen(r.Context(), innen, key); err != nil {
			w.zurueckFehler(rw, r, zurueck, http.StatusBadGateway, saysNichtEin)
			return
		}
		http.Redirect(rw, r, weiter(url.Values{"getan": {key}}), http.StatusSeeOther)
		return
	}
	art := r.PostFormValue("art")
	if !gueltig(art) {
		w.zurueckFehler(rw, r, zurueck, http.StatusBadRequest, saysFrageOhneWahl)
		return
	}
	x, y := r.PostFormValue("x"), r.PostFormValue("y")
	if g, da := w.Umfeld.Netz().Gegebenes(paarKey(x, y)); da && g.Eingetragen != nil && art != artSpaeter { // a conflict answered anew: the old line leaves Homes first
		if err := w.homesSchreiben(r.Context(), g.Eingetragen.Zeile, true); err != nil {
			log.Printf("archiv: Umfeld: eine alte Antwort nicht aus Homes: %v", err)
			w.zurueckFehler(rw, r, zurueck, http.StatusBadGateway, saysNichtEin)
			return
		}
		w.Umfeld.Eintragen(innen, g.Key, nil)
	}
	f, err := w.Umfeld.Beantworten(innen, x, y, art)
	if err != nil {
		w.antwortFehler(rw, r, zurueck, err)
		return
	}
	key = paarKey(f.X.ID, f.Y.ID)
	if art == artSpaeter {
		http.Redirect(rw, r, weiter(url.Values{}), http.StatusSeeOther)
		return
	}
	// a refusal stays visible: the toast and the list say „noch nicht eingetragen“
	w.eintragen(r.Context(), innen, key)
	for _, d := range w.Umfeld.Netz().Gefolgerte(key) {
		w.eintragen(r.Context(), innen, d.Key)
	}
	http.Redirect(rw, r, weiter(url.Values{"getan": {key}}), http.StatusSeeOther)
}

// eintragen has Homes write the answer key, if it goes anywhere and is not
// written yet, and keeps in the vault what was written.
func (w *Web) eintragen(ctx context.Context, innen Innen, key string) error {
	g, da := w.Umfeld.Netz().Gegebenes(key)
	if !da || g.Eingetragen != nil || g.Karte.Wohin == "" || !slices.Contains(arten, g.Art) {
		return nil
	}
	if err := w.homesSchreiben(ctx, g.Karte.Zeile, false); err != nil {
		if !errors.Is(err, errKeinHomes) {
			log.Printf("archiv: Umfeld: eine Antwort nicht an Homes: %v", err)
		}
		return err
	}
	return w.Umfeld.Eintragen(innen, key, &Eingetragen{Wohin: g.Karte.Wohin, Zeile: g.Karte.Zeile, Am: w.jetzt()})
}

// ausHomes takes back from Homes what it wrote of the answer key and of
// what followed from it, each marked as taken back in the vault at once,
// so a second try after a failure sends only what is left.
func (w *Web) ausHomes(ctx context.Context, innen Innen, key string) error {
	n := w.Umfeld.Netz()
	gs := n.Gefolgerte(key)
	if g, da := n.Gegebenes(key); da {
		gs = append(gs, g)
	}
	for _, g := range gs {
		if g.Eingetragen == nil {
			continue
		}
		if err := w.homesSchreiben(ctx, g.Eingetragen.Zeile, true); err != nil {
			return err
		}
		if err := w.Umfeld.Eintragen(innen, g.Key, nil); err != nil {
			return err
		}
	}
	return nil
}

// zurueckFehler shows the page the form came from with fehler.
func (w *Web) zurueckFehler(rw http.ResponseWriter, r *http.Request, zurueck string, code int, fehler string) {
	if zurueck == "/tresor/umfeld" {
		w.umfeldSeite(rw, r, code, "", "", fehler)
		return
	}
	w.antwort(rw, r, code, "", fehler)
}

func (w *Web) antwortFehler(rw http.ResponseWriter, r *http.Request, zurueck string, err error) {
	if errors.Is(err, errFrageWeg) || errors.Is(err, errUmfeldZu) {
		w.zurueckFehler(rw, r, zurueck, http.StatusConflict, saysFrageWeg)
		return
	}
	log.Printf("archiv: Umfeld: eine Antwort nicht gespeichert: %v", err)
	w.zurueckFehler(rw, r, zurueck, http.StatusInternalServerError, saysUmfeldVerbergen)
}

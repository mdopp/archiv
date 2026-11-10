package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
)

// „Beziehungen klären“ and „Deine Antworten“ on /tresor/umfeld (archiv#27
// step 2): the first fragenZeigen questions, each with the tie's reasons,
// the kinds with the suggestion chosen and why, Speichern, Keine Beziehung
// and Überspringen (Kennen sich nicht is a kind's choice, as in Homes);
// then the answers given, each with what the phone books say of the two,
// its Übernehmen into Homes (uebernahme.go) and Vergessen. Outside the
// page's live blocks: a change while the person chooses never swaps a form.

const (
	fragenZeigen = 5

	saysFrageWeg      = "Diese Frage gibt es nicht mehr; das Umfeld wurde inzwischen neu gerechnet."
	saysFrageOhneWahl = "Bitte wähle zuerst, wie die beiden zueinander stehen."
	saysUebernehmenOk = "Mit „Übernehmen“ öffnet Homes eine Seite, die zeigt, was wohin kommt; erst wenn du dort bestätigst, schreibt Homes es ins Adressbuch."
	saysKeinHomes     = "Ins Adressbuch übernehmen geht erst, wenn Homes mit deinem Archiv verbunden ist. Sag dem Admin Bescheid."
)

// duOder is a node as a sentence names it: „dir“ for the person.
func duOder(k Knoten, ich bool) string {
	if ich {
		return "dir"
	}
	return k.Name
}

// umfeldFragen writes the questions and the answers.
func (w *Web) umfeldFragen(b *strings.Builder, n *Netz, mitte string) {
	if fs := n.Fragen(); len(fs) > 0 {
		b.WriteString(`<section id=fragen><h3>Beziehungen klären</h3>`)
		for _, f := range fs[:min(len(fs), fragenZeigen)] {
			b.WriteString(frageKarte(n, f, mitte))
		}
		b.WriteString(`</section>`)
	}
	gs := n.Gegebene()
	if len(gs) == 0 {
		return
	}
	b.WriteString(`<section id=antworten><h3>Deine Antworten</h3><ul class=nahe>`)
	uebernehmbar := false
	for _, g := range gs {
		fmt.Fprintf(b, `<li>%s`, esc(gegebenSatz(g)))
		if g.Buch != "" {
			fmt.Fprintf(b, ` <span class=k>Im Telefonbuch: %s.</span>`, esc(g.Buch))
		}
		if g.Karte.Wohin != "" {
			uebernehmbar = true
		} else if slices.Contains(arten, g.Art) {
			b.WriteString(` <span class=k>Keiner der beiden steht auf deinem Handy, und nicht beide im Haushalts-Adressbuch: übernehmen lässt sich das nicht.</span>`)
		}
		fmt.Fprintf(b, `%s<form method=post action="/tresor/umfeld/antwort" class=knoepfe><input type=hidden name=mitte value="%s"><input type=hidden name=paar value="%s"><button class=k name=antwort value=vergessen data-laeuft="Vergisst …">Vergessen</button></form></li>`,
			w.uebernehmenKnopf(g.Karte), esc(mitte), esc(g.Key))
	}
	b.WriteString(`</ul>`)
	if uebernehmbar {
		fmt.Fprintf(b, `<p class=k>%s</p>`, esc(map[bool]string{true: saysUebernehmenOk, false: saysKeinHomes}[w.HomesSecret != "" && w.HomesURL != ""]))
	}
	b.WriteString(`</section>`)
}

// gegebenSatz is an answer as a sentence: „Clara ist Kind von dir.“
func gegebenSatz(g Gegeben) string {
	switch g.Art {
	case artKeine:
		return fmt.Sprintf("%s und %s: keine Beziehung.", g.X.Name, duOder(g.Y, g.Ich))
	case artFremd:
		return fmt.Sprintf("%s und %s kennen sich nicht.", g.X.Name, duOder(g.Y, g.Ich))
	}
	return fmt.Sprintf("%s ist %s von %s.", g.X.Name, kurzWort(g.Art), duOder(g.Y, g.Ich))
}

// frageKarte is one question and its form.
func frageKarte(n *Netz, f Frage, mitte string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<article class=frage><h4>%s</h4><p class=k>%s</p><form method=post action="/tresor/umfeld/antwort">`,
		esc(fmt.Sprintf("Wie steht %s zu %s?", f.X.Name, duOder(f.Y, f.Ich))), esc(n.Gruende(f.Y.ID, f.Kante)))
	fmt.Fprintf(&b, `<input type=hidden name=mitte value="%s"><input type=hidden name=x value="%s"><input type=hidden name=y value="%s"><label>%s<select name=art required><option value="">Bitte wählen</option>`,
		esc(mitte), esc(f.X.ID), esc(f.Y.ID), esc(f.X.Name+" ist …"))
	for _, a := range append(slices.Clone(arten), artFremd) {
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, a, map[bool]string{true: " selected"}[a == f.Vorschlag], esc(artWort(a)))
	}
	b.WriteString(`</select></label>`)
	if f.Weil != "" {
		fmt.Fprintf(&b, `<p class=k>%s</p>`, esc("Vorschlag, weil "+f.Weil+"."))
	}
	b.WriteString(`<div class=knoepfe><button name=antwort value=ja data-laeuft="Speichert …">Speichern</button>` +
		`<button class=k name=antwort value=keine formnovalidate data-laeuft="Merkt es sich …">Keine Beziehung</button>` +
		`<button class=k name=antwort value=spaeter formnovalidate data-laeuft="Legt es nach hinten …">Überspringen</button></div></form></article>`)
	return b.String()
}

// umfeldAntwort takes a question's answer (x, y, antwort ja|keine|spaeter,
// art) or an answer's Vergessen (paar), and says how it ended; a kind
// with its Übernehmen right there.
func (w *Web) umfeldAntwort(rw http.ResponseWriter, r *http.Request) {
	innen, ok := w.Tresor.Innen()
	if !ok {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
		return
	}
	mitte, antwort := r.PostFormValue("mitte"), r.PostFormValue("antwort")
	if antwort == "vergessen" {
		if err := w.Umfeld.Vergessen(innen, r.PostFormValue("paar")); err != nil {
			w.antwortFehler(rw, r, mitte, err)
			return
		}
		w.umfeldSeite(rw, r, http.StatusOK, mitte, "Vergessen. Das Archiv fragt vielleicht später wieder danach.", "")
		return
	}
	art, erlaubt := map[string]string{"ja": r.PostFormValue("art"), "keine": artKeine, "spaeter": artSpaeter}[antwort]
	if !erlaubt || art == "" {
		w.umfeldSeite(rw, r, http.StatusBadRequest, mitte, "", saysFrageOhneWahl)
		return
	}
	f, err := w.Umfeld.Beantworten(innen, r.PostFormValue("x"), r.PostFormValue("y"), art)
	if err != nil {
		w.antwortFehler(rw, r, mitte, err)
		return
	}
	du := duOder(f.Y, f.Ich)
	switch art {
	case artKeine:
		w.umfeldSeite(rw, r, http.StatusOK, mitte, "Gemerkt: keine Beziehung. Das Archiv fragt danach nicht wieder.", "")
	case artSpaeter:
		w.umfeldSeite(rw, r, http.StatusOK, mitte, "Übersprungen. Die Frage kommt später noch einmal.", "")
	case artFremd:
		w.umfeldSeite(rw, r, http.StatusOK, mitte, "Gemerkt: Die beiden kennen sich nicht. Das Archiv zeigt keine Verbindung mehr zwischen ihnen und fragt nicht wieder.", "")
	default:
		satz := fmt.Sprintf("Gespeichert: %s ist %s von %s. Das steht nur in deinem Tresor.", f.X.Name, kurzWort(art), du)
		w.umfeldSeite(rw, r, http.StatusOK, mitte, satz, "", w.uebernehmenKnopf(w.Umfeld.Netz().Uebernahme(f.X.ID, f.Y.ID, art)))
	}
}

func (w *Web) antwortFehler(rw http.ResponseWriter, r *http.Request, mitte string, err error) {
	if errors.Is(err, errFrageWeg) || errors.Is(err, errUmfeldZu) {
		w.umfeldSeite(rw, r, http.StatusConflict, mitte, "", saysFrageWeg)
		return
	}
	log.Printf("archiv: Umfeld: eine Antwort nicht gespeichert: %v", err)
	w.umfeldSeite(rw, r, http.StatusInternalServerError, mitte, "", saysUmfeldVerbergen)
}

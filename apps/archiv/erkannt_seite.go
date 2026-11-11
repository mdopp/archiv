package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// The pages of what was recognised (archiv#42): the workplace's line
// (erkanntZeile), the suggestions' page /tresor/erkannt, and the
// workplace's tab „Gruppen“, where the person names any group at any
// time, with her own words too. Every confirmation is one tap, and the
// same Rückgängig as everywhere.

const (
	erkanntPfad      = "/tresor/erkannt"
	gruppenTabPfad   = "/tresor?tab=gruppen"
	gruppenZeigen    = 30 // groups the tab shows before „alle“
	erkanntZeigen    = 20 // rows of a section of the suggestions' page
	saysGruppenTab   = "Wie du eine Gruppe nennst, hilft bei den Fragen zu ihren Mitgliedern. Es bleibt in deinem Archiv und kommt in kein Adressbuch."
	saysGruppenLeer  = "Noch keine Gruppen: Gruppen-Chats mit Namen und mindestens zwei Mitgliedern aus deinen Telefonbüchern erscheinen hier, sobald das Archiv sie in deinen Nachrichten gelesen hat."
	saysErkanntLeer  = "Gerade gibt es nichts zu bestätigen. Was dein Archiv neu erkennt, erscheint hier."
	saysErkanntRegel = "Alles hier ist nur ein Vorschlag: Eingetragen wird erst, was du bestätigst. Gelesen wird nur in deinem Tresor; Nachrichten gehen dazu nur an das Modell deiner Box."
	saysGebBleibt    = "Bestätigte Geburtstage bleiben in deinem Archiv; ins Adressbuch kommen sie noch nicht."
)

// erkanntZeile is the workplace's one line of what was recognised, a
// link to the suggestions; nothing when there is nothing to say.
func (w *Web) erkanntZeile(b *strings.Builder, n *Netz) {
	e := n.Erkannt(w.Umfeld.Stand(), w.Umfeld.modellDa())
	var teile []string
	switch {
	case e.Modell && e.Gezaehlt && e.Offen > 0:
		teile = append(teile, fmt.Sprintf("%s von %s Nachrichten gelesen", tausender(e.Gelesen), tausender(e.Gelesen+e.Offen)))
	case e.Modell && e.Gelesen > 0:
		teile = append(teile, anzahlT(e.Gelesen, "Nachricht", "Nachrichten")+" gelesen")
	}
	if v := e.Vorschlaege(); v > 0 {
		teile = append(teile, anzahl(v, "Vorschlag", "Vorschläge"))
	}
	if e.GruppenAlle > 0 {
		teile = append(teile, fmt.Sprintf("%d von %s eingeordnet", e.GruppenEingeordnet, anzahl(e.GruppenAlle, "Gruppe", "Gruppen")))
	}
	if len(teile) == 0 {
		return
	}
	fmt.Fprintf(b, `<p class=weiter-in id=erkannt-zeile data-live><a href="%s">Erkannt: %s</a></p>`, erkanntPfad, esc(strings.Join(teile, " · ")))
}

// bezugSatz is a suggested relation as a question: „Kim ist Mutter/Vater von dir?“
func bezugSatz(e ErkannterBezug) string {
	return fmt.Sprintf("%s ist %s von %s?", e.X.Name, kurzWort(e.Art), duOder(e.Y, e.Ich))
}

// erkanntGet is /tresor/erkannt: the open suggestions, each with one tap.
func (w *Web) erkanntGet(rw http.ResponseWriter, r *http.Request) {
	w.erkanntSeite(rw, r, http.StatusOK, meldungen[r.URL.Query().Get("m")], "")
}

func (w *Web) erkanntSeite(rw http.ResponseWriter, r *http.Request, code int, meldung, fehler string) {
	if _, auf := w.Tresor.Innen(); !auf {
		http.Redirect(rw, r, "/tresor", http.StatusSeeOther)
		return
	}
	var b strings.Builder
	meldungenText(&b, meldung, fehler)
	n := w.Umfeld.Netz()
	if n == nil {
		w.umfeldFehlt(&b)
		seite(rw, code, "Erkannt", b.String(), "")
		return
	}
	e := n.Erkannt(w.Umfeld.Stand(), w.Umfeld.modellDa())
	switch {
	case e.Modell && e.Gezaehlt && e.Offen > 0:
		fmt.Fprintf(&b, `<p class=k>Das Modell deiner Box hat %s von %s Nachrichten mit Beziehungswörtern gelesen. Es liest weiter, solange dein Archiv offen ist und niemand sonst das Modell braucht.</p>`,
			tausender(e.Gelesen), tausender(e.Gelesen+e.Offen))
	case e.Modell && e.Gelesen > 0:
		fmt.Fprintf(&b, `<p class=k>Das Modell deiner Box hat %s mit Beziehungswörtern gelesen.</p>`, esc(anzahlT(e.Gelesen, "Nachricht", "Nachrichten")))
	}
	bs, gs, gebs := n.ErkannteBeziehungen(), GruppenVorschlaege(n.Gruppenliste()), n.GeburtstagVorschlaege()
	if len(bs)+len(gs)+len(gebs) == 0 {
		fmt.Fprintf(&b, `<div class=gut><span class=punkt></span><p>%s</p></div>`, esc(saysErkanntLeer))
	}
	if len(bs) > 0 {
		fmt.Fprintf(&b, `<section class=karte><h2>Beziehungen (%d)</h2><ul>`, len(bs))
		for _, x := range bs[:min(len(bs), erkanntZeigen)] {
			kante := kanteAdresse(x.X.ID, x.Y.ID)
			fmt.Fprintf(&b, `<li class=tu><div>%s<span class=k>%s</span></div><form method=post action="%s" class=knoepfe><input type=hidden name=x value="%s"><input type=hidden name=y value="%s"><input type=hidden name=zurueck value="%s"><button name=art value="%s" data-laeuft="Trägt ein …">Stimmt</button><a class=k href="%s">Ansehen</a></form></li>`,
				esc(bezugSatz(x)), esc("Vorschlag, weil "+x.Weil+"."), antwortPfad, esc(x.X.ID), esc(x.Y.ID), esc(kante), esc(x.Art), esc(kante))
		}
		b.WriteString(`</ul></section>`)
	}
	if len(gs) > 0 {
		fmt.Fprintf(&b, `<section class=karte><h2>Gruppen (%d)</h2><ul>`, len(gs))
		for _, g := range gs[:min(len(gs), erkanntZeigen)] {
			warum := "„" + g.Wort + "“ im Namen"
			if g.Modell {
				warum = "das Modell deiner Box hat Namen, Mitglieder und ein paar Nachrichten gelesen"
			}
			fmt.Fprintf(&b, `<li class=tu><div>%s<span class=k>%s</span></div><form method=post action="%s" class=knoepfe><input type=hidden name=gruppe value="%s"><input type=hidden name=liste value=1><input type=hidden name=zurueck value="%s"><button name=art value="%s" data-laeuft="Merkt es …">Stimmt</button><a class=k href="%s">Anders nennen</a></form></li>`,
				esc("„"+g.Titel+"“: "+kategorieWorte[g.Vorschlag]+"?"), esc("Vorschlag, weil "+warum+"."), antwortPfad, esc(g.ID), erkanntPfad, esc(g.Vorschlag), esc(gruppenTabPfad+"#g-"+g.ID))
		}
		b.WriteString(`</ul></section>`)
	}
	if len(gebs) > 0 {
		fmt.Fprintf(&b, `<section class=karte><h2>Geburtstage (%d)</h2><ul>`, len(gebs))
		for _, g := range gebs {
			wer := g.Wer.Name
			if g.Ich {
				wer = "Dein Geburtstag"
			}
			fmt.Fprintf(&b, `<li class=tu><div>%s<span class=k>%s</span></div><form method=post action="%s/geburtstag" class=knoepfe><input type=hidden name=id value="%s"><input type=hidden name=tag value="%s"><button name=ja value=1 data-laeuft="Merkt es …">Stimmt</button><button name=ja value=0 class=leise data-laeuft="Merkt es …">Stimmt nicht</button></form></li>`,
				esc(wer+": "+tagText(g.Tag)+"?"), esc(gebGrund(g)), erkanntPfad, esc(g.Wer.ID), esc(g.Tag))
		}
		b.WriteString(`</ul></section>`)
	}
	if bs := n.Geburtstage(); len(bs) > 0 {
		fmt.Fprintf(&b, `<details class=karte><summary>Bestätigte Geburtstage (%d)</summary><ul>`, len(bs))
		for _, g := range bs {
			fmt.Fprintf(&b, `<li class=tu><div>%s</div><form method=post action="%s/geburtstag" class=knoepfe><input type=hidden name=id value="%s"><button name=tu value=vergessen class=leise data-laeuft="Vergisst …">Vergessen</button></form></li>`,
				esc(g.Wer.Name+": "+tagText(g.Tag)), erkanntPfad, esc(g.Wer.ID))
		}
		fmt.Fprintf(&b, `</ul><p class=k>%s</p></details>`, esc(saysGebBleibt))
	}
	b.WriteString(w.toast(n, r.URL.Query().Get("getan"), erkanntPfad))
	fmt.Fprintf(&b, `<p class=hinweis>%s</p>`, esc(saysErkanntRegel))
	seite(rw, code, "Erkannt", b.String(), "")
}

// gebGrund is why a birthday is suggested: „3 Glückwünsche, 2023 bis 2025“.
func gebGrund(g GebVorschlag) string {
	s := anzahl(g.Belege, "Glückwunsch", "Glückwünsche") + " an diesem Tag"
	switch len(g.Jahre) {
	case 0:
	case 1:
		s += fmt.Sprintf(", %d", g.Jahre[0])
	default:
		s += fmt.Sprintf(", %d bis %d", g.Jahre[0], g.Jahre[len(g.Jahre)-1])
	}
	return s
}

// erkanntGeburtstag takes „Stimmt“, „Stimmt nicht“ or „Vergessen“ of a birthday.
func (w *Web) erkanntGeburtstag(rw http.ResponseWriter, r *http.Request) {
	innen, ok := w.Tresor.Innen()
	if !ok {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
		return
	}
	vergessen := r.PostFormValue("tu") == "vergessen"
	err := w.Umfeld.GeburtstagSagen(innen, r.PostFormValue("id"), r.PostFormValue("tag"), r.PostFormValue("ja") == "1", vergessen)
	switch {
	case errors.Is(err, errFrageWeg) || errors.Is(err, errUmfeldZu):
		w.erkanntSeite(rw, r, http.StatusConflict, "", saysFrageWeg)
		return
	case err != nil:
		log.Printf("archiv: Umfeld: einen Geburtstag nicht gespeichert: %v", err)
		w.erkanntSeite(rw, r, http.StatusInternalServerError, "", saysUmfeldVerbergen)
		return
	}
	m := map[bool]string{true: "geb-weg", false: map[bool]string{true: "geb-ja", false: "geb-nein"}[r.PostFormValue("ja") == "1"]}[vergessen]
	http.Redirect(rw, r, erkanntPfad+"?"+url.Values{"m": {m}}.Encode(), http.StatusSeeOther)
}

// gruppenTab is the workplace's „Gruppen“: every group the person may
// name, the active first, each folded: its chips (the current one
// pressed, the suggestion marked), her own words as chips, and a field
// for a new one. alle shows the old ones too.
func gruppenTab(b *strings.Builder, n *Netz, alle bool) {
	zs := n.Gruppenliste()
	if len(zs) == 0 {
		fmt.Fprintf(b, `<p class=ruhig><span class="punkt grau"></span>%s</p>`, esc(saysGruppenLeer))
		return
	}
	fmt.Fprintf(b, `<p class=k>%s</p><section class=karte><ul>`, esc(saysGruppenTab))
	eigene := n.EigeneKategorien()
	gezeigt, alt := 0, 0
	for _, z := range zs {
		if !alle && (!z.Aktiv || gezeigt >= gruppenZeigen) {
			alt++
			continue
		}
		gezeigt++
		b.WriteString(gruppenZeile(z, eigene))
	}
	b.WriteString(`</ul></section>`)
	if alt > 0 {
		fmt.Fprintf(b, `<p class=weiter-in><a class=leise href="/tresor?tab=gruppen&amp;alle=1">%s</a></p>`, esc("auch "+anzahl(alt, "ältere Gruppe", "ältere Gruppen")+" zeigen"))
	}
}

// gruppenZeile is one group of the tab, folded.
func gruppenZeile(z GruppenZeile, eigene []string) string {
	var b strings.Builder
	stand := "noch nicht eingeordnet"
	switch {
	case z.Kategorie != "":
		stand = kategorieName(z.Kategorie, z.Eigen)
	case z.Modell:
		stand = "Vorschlag des Modells: " + kategorieWorte[z.Vorschlag]
	case z.Vorschlag != "":
		stand = "Vorschlag: " + kategorieWorte[z.Vorschlag]
	}
	fmt.Fprintf(&b, `<li id="g-%s"><details><summary>%s</summary><span class=k>%s · %s</span>`, esc(z.ID), esc(z.Titel), esc(stand), esc(anzahl(z.Mitglieder, "Mitglied", "Mitglieder")+" aus deinen Telefonbüchern"))
	formular := func() {
		fmt.Fprintf(&b, `<form method=post action="%s" class=sofort><input type=hidden name=gruppe value="%s"><input type=hidden name=liste value=1><input type=hidden name=zurueck value="%s">`, antwortPfad, esc(z.ID), gruppenTabPfad)
	}
	formular()
	b.WriteString(`<div class=chips>`)
	for _, k := range kategorien {
		klasse := map[bool]string{true: " vorschlag"}[z.Kategorie == "" && k == z.Vorschlag]
		if z.Kategorie == k {
			klasse = " gewaehlt"
		}
		b.WriteString(chip(k, kategorieWorte[k], klasse))
	}
	for _, e := range eigene {
		klasse := map[bool]string{true: " gewaehlt"}[z.Kategorie == kategorieEigen && z.Eigen == e]
		fmt.Fprintf(&b, `<button name=art value="%s" class="chip%s" data-laeuft="%s">%s</button>`, esc(kategorieEigen+":"+e), klasse, esc(e+" …"), esc(e))
	}
	b.WriteString(`</div></form>`)
	formular()
	fmt.Fprintf(&b, `<label class=k>Eigener Name<input name=eigen maxlength=%d autocomplete=off placeholder="zum Beispiel Doppelkopf"></label><button name=art value=%s data-laeuft="Merkt es …">Speichern</button></form></details></li>`, eigeneLaenge, kategorieEigen)
	return b.String()
}

// GruppeSetzen keeps the person's category of a group of the list in the open vault (gruppen.go).
func (u *Umfeld) GruppeSetzen(innen Innen, id, kat, eigen string) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	if name, ok := strings.CutPrefix(kat, kategorieEigen+":"); ok { // one of her own words as a chip
		kat, eigen = kategorieEigen, name
	}
	alle, err := n.GruppeSetzen(id, kat, eigen)
	if err != nil {
		return err
	}
	return jsonImTresor(innen.umfeldGruppen(), alle)
}

// modellDa: the box gave the archive its model.
func (u *Umfeld) modellDa() bool { return u != nil && u.Modell.eingerichtet() }

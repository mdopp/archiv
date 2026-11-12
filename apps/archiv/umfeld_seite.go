package main

import (
	"cmp"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// The Umfeld pages (archiv#27, step 1): on the vault page a card with
// the Immich key and how far the round is; under /tresor/umfeld „Steht
// nahe“: a radial picture around one person (?mitte=, the person by
// default), the closest in rings by strength, thin lines for the ties
// among them, each a link that puts it in the middle; under it the same
// as a ranked list with the reasons (the phone's and the screen reader's
// way), each a link to its detail, where Ausblenden and Nicht berechnen are; then the hidden ones,
// restorable. Only while the vault is open; closed, the locked page.

const (
	umfeldZeigen = 30 // in the picture; the list has everybody

	saysUmfeldGespeichert = "Gespeichert. Das Archiv liest jetzt deine Fotos aus Immich und rechnet dein Umfeld; bei vielen Fotos dauert das ein paar Minuten."
	saysSucheNiemand      = "Niemanden mit diesem Namen gefunden. Zu finden ist, wer in deinen Telefonbüchern steht oder auf deinen Fotos einen Namen hat."
	saysUmfeldVerbergen   = "Das ließ sich nicht speichern. Versuch es gleich noch einmal."
	saysUmfeldHinweis     = "Das Archiv zählt, an wie vielen Tagen zwei Menschen zusammen auf deinen Fotos sind; ein Tag an einem Ort zählt einmal, ein großes Gruppenfoto wenig, Altes weniger als Neues. Dazu kommt, was die Telefonbücher sagen: dieselbe Adresse, eine eingetragene Beziehung. Dieselbe Firma allein verbindet nicht, dann fragt das Archiv nach. Gemeinsame Bekannte nennt es, sie verbinden aber nie. „Nicht berechnen“ (bei der Person selbst) nimmt jemanden ganz heraus, etwa Freunde der Kinder. Alles liegt in deinem Tresor; nur du siehst diese Seite, und nur solange dein Archiv offen ist."
)

// umfeldKarte is the card on the open vault page.
func (w *Web) umfeldKarte(b *strings.Builder, qa quelleAuf) {
	u := w.Umfeld
	innen, auf := w.Tresor.Innen()
	am, hat := HatImmichSchluessel(innen)
	st := u.Stand()
	zeile := "aus " + anzahlT(st.Fotos, "Foto", "Fotos") + " gerechnet"
	switch {
	case !u.Eingerichtet():
		zeile = "nicht eingerichtet"
	case !hat:
		zeile = "Immich-Schlüssel fehlt"
	case st.Fehler != "":
		zeile = "gestört"
	case st.Laeuft:
		zeile = "liest gerade deine Fotos …"
	}
	quelleKopf(b, qa, "umfeld", "Umfeld", zeile)
	b.WriteString(`<p class=k>Wer dir nahe steht und wer wem: aus deinen Fotos in Immich und aus den Telefonbüchern. Das Archiv rechnet es nur, solange es offen ist, und hebt es im Tresor auf.</p>`)
	if !u.Eingerichtet() {
		fmt.Fprintf(b, `<p class=zugang><span class=fehlt>%s</span></p>`+quelleEnde, esc(saysImmichNichtEingerichtet))
		return
	}
	if !auf {
		b.WriteString(quelleEnde)
		return
	}
	if hat {
		fmt.Fprintf(b, `<p class=zugang>Immich-Schlüssel hinterlegt am %s</p>`, datum(am))
	} else {
		b.WriteString(`<p class=zugang><span class=fehlt>Immich-Schlüssel fehlt</span></p>`)
	}
	umfeldStandText(b, st, hat)
	if n := u.Netz(); n != nil {
		b.WriteString(`<p><a class=gross href="/tresor/umfeld">Umfeld ansehen</a></p>`)
	}
	summary, knopfText := "Immich-Schlüssel eintragen", "Prüfen und speichern"
	if hat {
		summary, knopfText = "Immich-Schlüssel ersetzen", "Prüfen und ersetzen"
	}
	var rechte []string
	for _, r := range umfeldRechte {
		rechte = append(rechte, "<b>"+r.Recht+"</b> ("+esc(r.Wofuer)+")")
	}
	fmt.Fprintf(b, `<details id=d-umfeld%s><summary>%s</summary>
<ol><li>Öffne Immich, oben rechts dein Bild → <b>Kontoeinstellungen</b> → <b>API-Schlüssel</b> → <b>Neuer API-Schlüssel</b>.</li>
<li>Gib ihm einen Namen, zum Beispiel „Archiv“, und kreuz diese Rechte an: %s. Mehr braucht das Archiv nicht.</li>
<li>Immich zeigt den Schlüssel nur einmal: Kopier ihn und füg ihn hier ein.</li></ol>
<form method=post action="/tresor/umfeld/schluessel" class=feld>
<label>Immich-API-Schlüssel<input name=schluessel type=password autocomplete=off spellcheck=false autocapitalize=off></label>
%s</form><p class=hinweis>Der Schlüssel liegt im Tresor: Ist das Archiv abgeschlossen, kann ihn niemand lesen, auch der Admin nicht. Das Archiv liest damit nur, es ändert in Immich nichts.</p></details>`+quelleEnde,
		offenWenn(true), summary, strings.Join(rechte, ", "), knopf(knopfText, "Prüft den Schlüssel …", ""))
}

// umfeldStandText says how far the round is, and what went wrong.
func umfeldStandText(b *strings.Builder, st UmfeldStand, hat bool) {
	if st.Laeuft {
		text := "Liest deine Fotos aus Immich …"
		if st.Gelesen > 0 {
			text = fmt.Sprintf("Bisher %s Fotos gelesen.", tausender(st.Gelesen))
		}
		fmt.Fprintf(b, `<p><b>Umfeld rechnen</b> <span class=k>seit %s</span><progress></progress><span class=k>%s</span></p>`, uhrzeit(st.Seit), esc(text))
	}
	if st.Fehler != "" {
		fmt.Fprintf(b, `<p class="meldung fehler">%s</p>`, esc(st.Fehler))
	}
	if st.Hinweis != "" {
		fmt.Fprintf(b, `<p class="meldung fehler">%s</p>`, esc(st.Hinweis))
	}
	if !st.Am.IsZero() && !st.Laeuft {
		fmt.Fprintf(b, `<p class=k>Zuletzt gerechnet um %s, aus %s mit Personen. Das Archiv rechnet jede Stunde neu, solange es offen ist.</p>`, uhrzeit(st.Am), esc(anzahlT(st.Fotos, "Foto", "Fotos")))
	}
	if !hat && !st.Laeuft {
		fmt.Fprintf(b, `<p class=k>%s</p>`, esc(saysImmichSchluesselFehlt))
	}
}

// umfeldSchluessel checks the key against Immich and stores it in the vault.
func (w *Web) umfeldSchluessel(rw http.ResponseWriter, r *http.Request) {
	innen, ok := w.Tresor.Innen()
	if !ok {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
		return
	}
	if !w.Umfeld.Eingerichtet() {
		w.antwort(rw, r, http.StatusConflict, "", saysImmichNichtEingerichtet)
		return
	}
	k, err := immichSchluessel(r.PostFormValue("schluessel"))
	if err != nil {
		w.antwort(rw, r, http.StatusBadRequest, "", saysImmichSchluesselFormat)
		return
	}
	defer nullen(k)
	rechte, err := (&Immich{URL: w.Umfeld.URL, Key: string(k)}).Rechte(r.Context())
	switch {
	case errors.Is(err, errImmichSchluessel):
		w.antwort(rw, r, http.StatusBadRequest, "", saysImmichSchluesselFalsch)
		return
	case err != nil:
		log.Printf("archiv: Immich-Schlüssel prüfen: %v", err)
		w.antwort(rw, r, http.StatusBadGateway, "", saysImmichWeg)
		return
	}
	if fehlen := fehlendeRechte(rechte); len(fehlen) > 0 {
		w.antwort(rw, r, http.StatusBadRequest, "", saysRechteFehlen(fehlen))
		return
	}
	if err := schreibenAtomar(innen.ImmichSchluessel(), k, 0o600); err != nil {
		log.Printf("archiv: Immich-Schlüssel speichern: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	w.Umfeld.Jetzt()
	if w.Geaendert != nil {
		w.Geaendert()
	}
	http.Redirect(rw, r, "/tresor/quellen?m=umfeld", http.StatusSeeOther)
}

// umfeldVerbergen takes a row's Ausblenden, Nicht berechnen or Zurückholen.
func (w *Web) umfeldVerbergen(rw http.ResponseWriter, r *http.Request) {
	innen, ok := w.Tresor.Innen()
	if !ok {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
		return
	}
	id, art, mitte := r.PostFormValue("id"), r.PostFormValue("art"), r.PostFormValue("mitte")
	gesagt := map[string]string{verbergenAus: "%s ausgeblendet", verbergenNicht: "%s wird nicht mehr berechnet", verbergenRuhe: "An %s erinnert das Archiv nicht mehr", "": "%s ist wieder da."}
	satz, erlaubt := gesagt[art]
	n := w.Umfeld.Netz()
	if !erlaubt || id == "" || n == nil {
		w.umfeldSeite(rw, r, http.StatusBadRequest, mitte, "", saysUmfeldVerbergen)
		return
	}
	k, bekannt := n.Person(id)
	err := w.Umfeld.Ruhen(innen, id, art == verbergenRuhe) // Rückgängig and Zurückholen name her again
	if err == nil && art != verbergenRuhe {
		err = w.Umfeld.Verbergen(innen, id, art)
	}
	if err != nil {
		log.Printf("archiv: Umfeld verbergen: %v", err)
		w.umfeldSeite(rw, r, http.StatusInternalServerError, mitte, "", saysUmfeldVerbergen)
		return
	}
	if !bekannt {
		k.Name = id
	}
	if art == "" {
		w.umfeldSeite(rw, r, http.StatusOK, id, fmt.Sprintf(satz, k.Name), "")
		return
	}
	// hidden: back to the person in the middle, with Rückgängig for ten seconds (owner 2026-11-11: never for good on one tap)
	w.umfeldSeiteMit(rw, r, http.StatusOK, "", "", "", verbergenToast(id, fmt.Sprintf(satz, k.Name)))
}

// verbergenToast is „✓ Ben Berg ausgeblendet · Rückgängig“: Rückgängig shows id again.
func verbergenToast(id, satz string) string {
	return fmt.Sprintf(`<div class=toast role=status><span>✓ %s</span><form method=post action="/tresor/umfeld/verbergen"><input type=hidden name=id value="%s"><input type=hidden name=art value=""><button data-laeuft="Holt zurück …">Rückgängig</button></form></div>`,
		esc(satz), esc(id))
}

func (w *Web) umfeldGet(rw http.ResponseWriter, r *http.Request) {
	if q := strings.TrimSpace(r.URL.Query().Get("suche")); q != "" && w.Umfeld.Netz() != nil {
		if _, auf := w.Tresor.Innen(); auf {
			w.personenSuche(rw, r, w.Umfeld.Netz(), q)
			return
		}
	}
	w.umfeldSeite(rw, r, http.StatusOK, r.URL.Query().Get("mitte"), meldungen[r.URL.Query().Get("m")], "")
}

// umfeldSeite is the page; mitte "" puts the person (or whoever has the
// most ties) in the middle; ?getan= shows that answer's toast.
func (w *Web) umfeldSeite(rw http.ResponseWriter, r *http.Request, code int, mitte, meldung, fehler string) {
	w.umfeldSeiteMit(rw, r, code, mitte, meldung, fehler, "")
}

// umfeldSeiteMit is umfeldSeite with a toast (Ausblenden's Rückgängig).
func (w *Web) umfeldSeiteMit(rw http.ResponseWriter, r *http.Request, code int, mitte, meldung, fehler, toast string) {
	innen, auf := w.Tresor.Innen()
	if !auf {
		http.Redirect(rw, r, "/tresor", http.StatusSeeOther)
		return
	}
	var b strings.Builder
	meldungenText(&b, meldung, fehler)
	u := w.Umfeld
	if !u.Eingerichtet() {
		fmt.Fprintf(&b, `<p>%s</p>`, esc(saysImmichNichtEingerichtet))
		seite(rw, code, "Umfeld", b.String(), "")
		return
	}
	_, hat := HatImmichSchluessel(innen)
	if n := u.Netz(); n != nil {
		b.WriteString(personSuchen(n, "", false))
	}
	b.WriteString(`<div id=umfeld-stand data-live aria-live=polite>`)
	umfeldStandText(&b, u.Stand(), hat)
	if !hat {
		b.WriteString(`<p><a href="/tresor/quellen#karte-umfeld">Immich-Schlüssel eintragen</a></p>`)
	}
	b.WriteString(`</div><div id=umfeld-netz data-live>`)
	n := u.Netz()
	if n != nil {
		w.umfeldNetz(&b, n, mitte)
	} else if hat {
		b.WriteString(`<p class=k>Noch kein Umfeld. Sobald das Archiv deine Fotos gelesen hat, erscheint es hier.</p>`)
	}
	b.WriteString(`</div>`)
	if n != nil {
		if jetzt, _ := n.Portion(u.Besuch(false)); len(jetzt) > 0 {
			fmt.Fprintf(&b, `<p class=weiter-in><a href="/tresor?tab=fragen">%s</a></p>`, esc(anzahl(len(jetzt), "offene Frage", "offene Fragen")))
		}
		w.antwortenListe(&b, n, "/tresor/umfeld")
		gruppenListe(&b, n, "/tresor/umfeld")
		b.WriteString(w.toast(n, r.URL.Query().Get("getan"), zurueckNach(umfeldMitte(mitte))))
		b.WriteString(toast)
	}
	fmt.Fprintf(&b, `<p class=hinweis>%s</p>`, esc(saysUmfeldHinweis))
	seite(rw, code, "Umfeld", b.String(), "")
}

// personSuchen is the one field that finds a person of the net by name
// (owner 2026-11-11: „Ich kann nicht nach einer Person suchen“): what is
// typed goes to /tresor/umfeld?suche=, which opens the person's page or
// offers those it could be. The page's script shows the names that match
// while typing, a tap goes on (data-weiter); liste adds the names for it,
// on a page that has no „+ Beziehung“ (which brings them).
func personSuchen(n *Netz, q string, liste bool) string {
	s := `<form method=get action="/tresor/umfeld" role=search class=person-suche data-weiter><input type=search name=suche list=namen data-namen autocomplete=off required aria-label="Person suchen" placeholder="Person suchen" value="` +
		esc(q) + `"><button data-laeuft="Sucht …">Finden</button></form>`
	if liste {
		s += namenListe(n)
	}
	return s
}

// personenSuche answers the field: one person (or one whose name is
// exactly that) is opened at once; several are offered, each with what
// she is to the person; nobody: the sentence, the field again and the
// ways on (the name in the messages, back to the Umfeld).
func (w *Web) personenSuche(rw http.ResponseWriter, r *http.Request, n *Netz, q string) {
	ts, mehr := n.Suchen(q, sucheHoechstens)
	if len(ts) == 1 || len(ts) > 1 && ts[0].Genau && !ts[1].Genau {
		http.Redirect(rw, r, umfeldMitte(ts[0].ID), http.StatusSeeOther)
		return
	}
	var b strings.Builder
	b.WriteString(personSuchen(n, q, true))
	if len(ts) == 0 {
		fmt.Fprintf(&b, `<p class=ruhig><span class="punkt grau"></span>%s</p><p class=weiter-in><a href="%s">In den Nachrichten nach „%s“ suchen</a></p>`,
			esc(saysSucheNiemand), esc(msgvaultSuche(q)), esc(q))
	} else {
		b.WriteString(`<section class=karte><h2>Wen meinst du?</h2>`)
		personenZeilen(&b, n, ts)
		b.WriteString(`</section>`)
		if mehr {
			fmt.Fprintf(&b, `<p class=k>Die ersten %d. Tipp mehr vom Namen ein, um die anderen zu finden.</p>`, len(ts))
		}
	}
	b.WriteString(`<p class=weiter-in><a href="/tresor/umfeld">Zurück zum Umfeld</a></p>`)
	seite(rw, http.StatusOK, "Person suchen", b.String(), "")
}

// personenZeilen are found persons as rows, each a link to its page: the
// face, the name, the phone book's name it was found by, and the tie to
// the person in one short line.
func personenZeilen(b *strings.Builder, n *Netz, ts []Treffer) {
	ich := n.Ich()
	b.WriteString(`<ul class=leute>`)
	for _, t := range ts {
		var k []string
		switch {
		case t.Spitz:
			k = append(k, "auch: "+t.Als)
		case t.Als != "":
			k = append(k, "im Telefonbuch: "+t.Als)
		}
		if kurz := n.Kurz(ich, n.KanteMit(ich, t.ID)); kurz != "" && t.ID != ich {
			k = append(k, kurz)
		}
		fmt.Fprintf(b, `<li><a href="%s">%s<span class=wer>%s<span class=k>%s</span></span></a></li>`, esc(umfeldMitte(t.ID)), gesicht(t.Name), esc(n.Anzeige(t.Knoten)), esc(strings.Join(k, " · ")))
	}
	b.WriteString(`</ul>`)
}

// umfeldNetz is „Steht nahe“ around mitte: the picture, the list, the hidden.
func (w *Web) umfeldNetz(b *strings.Builder, n *Netz, mitte string) {
	ich := n.Ich()
	mitte = cmp.Or(mitte, n.Mitte())
	k, ok := n.Person(mitte)
	vs := n.Nahe(mitte, 1<<30)
	if ok && ich != "" && mitte != ich {
		umfeldKopf(b, n, k)
	}
	switch {
	case mitte == "":
		b.WriteString(`<p class=k>Noch keine Verbindungen: Ein Paar zählt, sobald es an zwei Tagen im kleinen Kreis zusammen auf deinen Fotos ist oder die Telefonbücher etwas über die beiden sagen.</p>`)
	case !ok || len(vs) == 0:
		b.WriteString(`<p class=k>Im Umfeld steht diese Person noch nicht: Dafür braucht es zwei gemeinsame Fototage im kleinen Kreis, einen Eintrag im Telefonbuch oder eine Beziehung von dir.</p>`)
		if ok {
			b.WriteString(beziehungDazu(n, k, true, "", umfeldMitte(mitte)))
		}
		if ok && mitte != ich {
			b.WriteString(personAktionen(n, k))
		}
	default:
		titel := "Steht dir nahe"
		if mitte != ich {
			titel = "Steht " + k.Name + " nahe"
		}
		fmt.Fprintf(b, `<h3>%s</h3>`, esc(titel))
		if ich == "" {
			b.WriteString(`<p class=k>Deine eigene Karte hat das Archiv in den Telefonbüchern nicht gefunden (es sucht die E-Mail deines Immich-Kontos und deine Nummer); in der Mitte steht deshalb, wer die meisten Verbindungen hat.</p>`)
		}
		b.WriteString(umfeldBild(n, k, vs[:min(len(vs), umfeldZeigen)]))
		if len(vs) > umfeldZeigen {
			fmt.Fprintf(b, `<p class=k>Im Bild die nächsten %d, alle %d in der Liste.</p>`, umfeldZeigen, len(vs))
		}
		if mitte == ich {
			b.WriteString(stilleListe(n))
		}
		b.WriteString(umfeldListe(n, k, vs))
		b.WriteString(beziehungDazu(n, k, false, "", umfeldMitte(mitte)))
		if mitte != ich {
			b.WriteString(personAktionen(n, k))
			fmt.Fprintf(b, `<p class=weiter-in><a href="%s">Wieder dich in die Mitte</a></p>`, esc(umfeldMitte(ich)))
		}
	}
	if vb := n.Verborgene(); len(vb) > 0 {
		b.WriteString(`<h3>Ausgeblendet</h3><ul class=nahe>`)
		for _, v := range vb {
			art := ""
			if v.Art == verbergenNicht {
				art = ` <span class=k>nicht berechnet</span>`
			}
			fmt.Fprintf(b, `<li>%s%s%s</li>`, esc(v.Name), art, umfeldKnoepfe(mitte, v.ID, ""))
		}
		b.WriteString(`</ul>`)
	}
}

// umfeldMitte is the address that puts id in the middle.
func umfeldMitte(id string) string { return "/tresor/umfeld?" + url.Values{"mitte": {id}}.Encode() }

// umfeldKopf leads a person's page with who she is to the person (owner
// 2026-11-11): the face and name, the relation, how close (the picture's
// rings: the first fifth, up to half, the rest) and when the two were
// last together, then what they share in one sentence, and the one way
// on: the edge page, where the examples are and the relation is set.
func umfeldKopf(b *strings.Builder, n *Netz, k Knoten) {
	fmt.Fprintf(b, `<section class=person id=person><div class=wer-ist>%s<h2>%s</h2></div>`, gesicht(k.Name), esc(k.mitKreuz()))
	if sp := n.Spitznamen(k.ID); len(sp) > 0 {
		fmt.Fprintf(b, `<p class="k auch">auch: %s</p>`, esc(strings.Join(sp, ", ")))
	}
	p, ok := n.Paar(k.ID, n.Ich())
	if !ok {
		b.WriteString(verstorbenZeile(k) + `</section>`)
		return
	}
	teile, weiter := []string{"Für dich: noch nichts eingetragen"}, "Beziehung eintragen"
	if p.Art != "" {
		teile, weiter = []string{"Für dich: " + map[bool]string{true: fremdWort(true), false: artJetzt(p.Art)}[p.Art == artFremd]}, "Was verbindet euch?"
	}
	if platz, _ := n.Platz(k.ID); platz > 0 {
		teile = append(teile, p.Kante.Stufe()) // by what the archive holds, not by her place among von
	}
	if z := p.Gemeinsam.Zuletzt; !z.IsZero() {
		teile = append(teile, "zuletzt gemeinsam am "+z.In(cmp.Or(n.Loc, time.Local)).Format("02.01.2006"))
	}
	fmt.Fprintf(b, `<p class=ist>%s</p><p class=summe>%s</p>%s<p><a class=weiter href="%s">%s</a></p>`, esc(strings.Join(teile, " · ")),
		esc(cmp.Or(p.Satz, saysKanteNichts)), bekannteZeile(n, p.Bekannte), esc(kanteAdresse(k.ID, n.Ich())), weiter)
	if slices.ContainsFunc(n.Stille(), func(s Still) bool { return s.Mit.ID == k.ID }) {
		b.WriteString(`<p class=k>Lange nichts gehört.` + umfeldKnoepfe("", k.ID, verbergenRuhe) + `</p>`)
	}
	b.WriteString(verstorbenZeile(k) + `</section>`)
}

// umfeldListe is the ranked list: each row the person and the tie in one
// short line (Netz.Kurz). A name always opens that person's page (owner
// 2026-11-11), which leads with what she and the person share; on
// anybody else's list a small „mit Ben ›“ after it opens what connects
// the row's and the middle. No action on the
// row (owner 2026-11-11: „gefährlich platziert, dicke Finger“): Ausblenden
// and Nicht berechnen are at the end of the person's page (umfeldAktionen).
//
// The list is in the steps of naehe.go, each with its count: „Sehr nah“
// and „Nah“ open, „Bekannt“ folded (owner 2026-11-11: fewer, better
// statements over many weak ones).
func umfeldListe(n *Netz, mitte Knoten, vs []Verbindung) string {
	var b strings.Builder
	for _, stufe := range []int{stufeSehrNah, stufeNah, stufeBekannt} {
		var zeilen strings.Builder
		anzahl := 0
		for _, v := range vs {
			if v.Kante.stufe() != stufe {
				continue
			}
			anzahl++
			paar := ""
			if mitte.ID != n.Ich() {
				paar = fmt.Sprintf(`<a class=paar href="%s">mit %s ›</a>`, esc(kanteAdresse(mitte.ID, v.Mit.ID)), esc(rufname(mitte.Name)))
			}
			fmt.Fprintf(&zeilen, `<li><a class=zeile href="%s">%s <span class=k>%s</span></a>%s</li>`, esc(umfeldMitte(v.Mit.ID)), esc(n.Anzeige(v.Mit)), esc(n.Kurz(mitte.ID, v.Kante)), paar)
		}
		titel := fmt.Sprintf("%s (%d)", map[int]string{stufeSehrNah: "Sehr nah", stufeNah: "Nah", stufeBekannt: "Bekannt"}[stufe], anzahl)
		switch {
		case anzahl == 0:
		case stufe == stufeBekannt:
			fmt.Fprintf(&b, `<details class=stufe><summary>%s</summary><ul class=nahe>%s</ul></details>`, titel, zeilen.String())
		default:
			fmt.Fprintf(&b, `<h4 class=stufe>%s</h4><ul class=nahe>%s</ul>`, titel, zeilen.String())
		}
	}
	return b.String()
}

// stilleListe is „Lange nichts gehört“ (Netz.Stille): each a link to the
// person's page, where „Nicht mehr erinnern“ is; "" for nobody.
func stilleListe(n *Netz) string {
	ss := n.Stille()
	if len(ss) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<section class="karte still"><h4>Lange nichts gehört</h4><ul class=nahe>`)
	for _, s := range ss {
		fmt.Fprintf(&b, `<li><a class=zeile href="%s">%s <span class=k>%s</span></a></li>`, esc(umfeldMitte(s.Mit.ID)), esc(s.Mit.Name), esc(stillGrund(s, n.Loc)))
	}
	b.WriteString(`</ul></section>`)
	return b.String()
}

// umfeldAktionen are a person's two actions, folded at the end of its
// page: big targets, apart, each with Rückgängig after it (verbergenToast).
func umfeldAktionen(id string) string {
	knopf := func(art, text, laeuft, klasse string) string {
		return `<form method=post action="/tresor/umfeld/verbergen"><input type=hidden name=id value="` + esc(id) + `"><button` + klasse + ` name=art value="` + art + `" data-laeuft="` + esc(laeuft) + `">` + esc(text) + `</button></form>`
	}
	return `<details class=karte id=verbergen><summary>Ausblenden …</summary><div class=aktionen>` + knopf(verbergenAus, "Ausblenden", "Blendet aus …", "") + knopf(verbergenNicht, "Nicht berechnen", "Nimmt heraus …", " class=leise") +
		`</div><p class=k>Ausblenden zeigt die Person nicht mehr; Nicht berechnen nimmt sie auch aus den Verbindungen der anderen. Beides lässt sich zurückholen.</p></details>`
}

// personAktionen are the detail's actions on k: a person made by hand is
// renamed or deleted (and may be one of the phone book), any other hidden.
func personAktionen(n *Netz, k Knoten) string {
	if !istAngelegt(k.ID) {
		return umfeldAktionen(k.ID)
	}
	var b strings.Builder
	dieselbeAbschnitt(&b, n, k.ID, umfeldMitte(k.ID))
	return b.String() + angelegtAktionen(k)
}

// umfeldKnoepfe is a row's form: a button per art, posted with id.
func umfeldKnoepfe(mitte, id string, arten ...string) string {
	text := map[string][2]string{verbergenAus: {"Ausblenden", "Blendet aus …"}, verbergenNicht: {"Nicht berechnen", "Nimmt heraus …"}, verbergenRuhe: {"Nicht mehr erinnern", "Merkt es sich …"}, "": {"Zurückholen", "Holt zurück …"}}
	out := `<form method=post action="/tresor/umfeld/verbergen" class=knoepfe><input type=hidden name=mitte value="` + esc(mitte) + `"><input type=hidden name=id value="` + esc(id) + `">`
	for _, art := range arten {
		out += `<button class=k name=art value="` + art + `" data-laeuft="` + esc(text[art][1]) + `">` + esc(text[art][0]) + `</button>`
	}
	return out + `</form>`
}

// umfeldBild is the picture: the middle, the first fifth on the inner
// ring, up to half on the middle one, the rest outside (eight or fewer on
// one ring); a line to each, its width the tie's strength, and thin ones
// among them. It scales to what it holds (owner 2026-11-11): each ring
// as wide as its people need, a full ring staggered so neighbouring names
// do not meet, the frame cut to the people and their names, and never
// wider than the phone shows it readably; at most umfeldBildHoechstens
// (the list has the rest).
func umfeldBild(n *Netz, mitte Knoten, vs []Verbindung) string {
	vs = vs[:min(len(vs), umfeldBildHoechstens)]
	ids, xy := make([]string, len(vs)), make([][2]float64, len(vs))
	for i, v := range vs {
		ids[i] = v.Mit.ID
	}
	ringe := [][2]int{{0, len(vs)}}
	if len(vs) > bildEinRing {
		ringe = [][2]int{{0, len(vs) / 5}, {len(vs) / 5, len(vs) / 2}, {len(vs) / 2, len(vs)}}
	}
	staerkste := 0.0
	for _, v := range vs {
		staerkste = max(staerkste, v.Kante.Staerke)
	}
	r := 0.0
	for ring, rg := range ringe {
		m := rg[1] - rg[0]
		versetzt := m > bildVersetzt
		r = max(bildRadius[min(ring, len(bildRadius)-1)], r+bildAbstand, float64(m)*bildBogen/(2*math.Pi))
		for i := rg[0]; i < rg[1]; i++ {
			a := 2*math.Pi*float64(i-rg[0])/float64(m) + float64(ring)*0.5 - math.Pi/2
			ri := r
			if versetzt && (i-rg[0])%2 == 1 {
				ri += bildVersatz
			}
			xy[i] = [2]float64{ri * math.Cos(a), ri * math.Sin(a)}
		}
		if versetzt {
			r += bildVersatz
		}
	}
	punkte := append([]bildPunkt{{x: 0, y: 0, r: 9, name: kurzName(mitte.Name) + map[bool]string{true: verstorbenZeichen}[mitte.Tot]}}, make([]bildPunkt, len(vs))...)
	for i, v := range vs {
		punkte[i+1] = bildPunkt{x: xy[i][0], y: xy[i][1], r: 5, name: kurzName(v.Mit.Name) + map[bool]string{true: verstorbenZeichen}[v.Mit.Tot]}
	}
	rahmen := beschriften(punkte)
	rahmen.mindestens(bildMindestens)
	b, h := rahmen.x1-rahmen.x0, rahmen.y1-rahmen.y0
	var s strings.Builder
	fmt.Fprintf(&s, `<svg class=netzbild viewBox="%.0f %.0f %.0f %.0f" style="max-width:%.0fpx" role=img aria-label="%s">`, rahmen.x0, rahmen.y0, b, h, b*bildZoom, esc("Wer "+mitte.Name+" nahe steht"))
	for _, p := range n.Zwischen(ids) {
		fmt.Fprintf(&s, `<line class=zw x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, xy[p[0]][0], xy[p[0]][1], xy[p[1]][0], xy[p[1]][1])
	}
	for i, v := range vs {
		fmt.Fprintf(&s, `<line x1="0" y1="0" x2="%.1f" y2="%.1f" stroke-width="%.1f"/>`, xy[i][0], xy[i][1], 0.5+2.5*v.Kante.Staerke/max(staerkste, 1e-9))
	}
	for i, v := range vs {
		s.WriteString(umfeldPunkt(v.Mit, punkte[i+1]))
	}
	s.WriteString(`<g class=mitte>` + umfeldPunkt(mitte, punkte[0]) + `</g></svg>`)
	return s.String()
}

// The picture's measures, in its units (a label's 10px at bildZoom 1).
const (
	umfeldBildHoechstens = 40  // persons in the picture; „Mehr zeigen“ lists more
	bildEinRing          = 8   // up to this many stand on one ring
	bildAbstand          = 46  // from one ring to the next at least
	bildBogen            = 30  // along a ring, from one person to the next at least
	bildVersetzt         = 12  // a ring with more is staggered
	bildVersatz          = 20  // every second person of a staggered ring further out
	bildZeichen          = 5.8 // a label's letter, wide
	bildRand             = 4
	bildZoom             = 1.3 // a small picture is shown this much larger, no more
	bildMindestens       = 200 // the frame's width at least, its height 0.7 of it: one or two people are no sliver
)

// bildRadius are the rings' smallest radii.
var bildRadius = []float64{62, 108, 150}

// bildRahmen is a box in the picture: the frame, a label's, a point's.
type bildRahmen struct{ x0, y0, x1, y1 float64 }

func (f bildRahmen) trifft(o bildRahmen) bool {
	return f.x0 < o.x1 && o.x0 < f.x1 && f.y0 < o.y1 && o.y0 < f.y1
}

func (f *bildRahmen) dazu(o bildRahmen) {
	f.x0, f.y0, f.x1, f.y1 = min(f.x0, o.x0), min(f.y0, o.y0), max(f.x1, o.x1), max(f.y1, o.y1)
}

// bildPunkt is a person in the picture and where its label stands: tx, ty
// the text's anchor, anker its text-anchor.
type bildPunkt struct {
	x, y, r, tx, ty float64
	name, anker     string
}

// beschriften places each label where it meets no point and no label
// placed before (the middle's first, then the closest): below or above
// the point, the outer side first, else beside it; the first place when
// none is free. It answers the frame around all of them.
func beschriften(ps []bildPunkt) bildRahmen {
	var belegt []bildRahmen
	for _, p := range ps {
		belegt = append(belegt, bildRahmen{p.x - p.r - 1, p.y - p.r - 1, p.x + p.r + 1, p.y + p.r + 1})
	}
	rahmen := belegt[0]
	for i := range ps {
		p := &ps[i]
		w := float64(len([]rune(p.name))) * bildZeichen
		unten := [3]float64{p.x, p.y + p.r + 11, 0}
		oben := [3]float64{p.x, p.y - p.r - 4, 0}
		rechts, links := [3]float64{p.x + p.r + 3, p.y + 3.5, 1}, [3]float64{p.x - p.r - 3, p.y + 3.5, -1}
		orte := [][3]float64{unten, oben, rechts, links}
		if p.y < -1 {
			orte[0], orte[1] = oben, unten
		}
		if p.x < -1 {
			orte[2], orte[3] = links, rechts
		}
		box := func(o [3]float64) bildRahmen {
			x0 := o[0] - w/2 + o[2]*w/2
			return bildRahmen{x0 - 1, o[1] - 9, x0 + w + 1, o[1] + 2.5}
		}
		wahl := orte[0]
		for _, o := range orte {
			if !slices.ContainsFunc(belegt, box(o).trifft) {
				wahl = o
				break
			}
		}
		p.tx, p.ty, p.anker = wahl[0], wahl[1], [3]string{"end", "", "start"}[int(wahl[2])+1]
		belegt = append(belegt, box(wahl))
		rahmen.dazu(box(wahl))
		rahmen.dazu(belegt[i])
	}
	rahmen.x0, rahmen.y0, rahmen.x1, rahmen.y1 = rahmen.x0-bildRand, rahmen.y0-bildRand, rahmen.x1+bildRand, rahmen.y1+bildRand
	return rahmen
}

// mindestens widens the frame evenly to at least m wide and 0.7 m high.
func (f *bildRahmen) mindestens(m float64) {
	if d := m - (f.x1 - f.x0); d > 0 {
		f.x0, f.x1 = f.x0-d/2, f.x1+d/2
	}
	if d := m*0.7 - (f.y1 - f.y0); d > 0 {
		f.y0, f.y1 = f.y0-d/2, f.y1+d/2
	}
}

// umfeldPunkt is one person in the picture, a link that puts it in the
// middle, with its label where beschriften put it.
func umfeldPunkt(k Knoten, p bildPunkt) string {
	anker := ""
	if p.anker != "" {
		anker = ` style="text-anchor:` + p.anker + `"`
	}
	return fmt.Sprintf(`<a href="%s" aria-label="%s"><circle cx="%.1f" cy="%.1f" r="%.0f"/><text x="%.1f" y="%.1f"%s>%s</text></a>`, esc(umfeldMitte(k.ID)),
		esc(k.Name+" in die Mitte"), p.x, p.y, p.r, p.tx, p.ty, anker, esc(p.name))
}

// kurzName is a name in the picture: the first name and the last one's initial.
func kurzName(name string) string {
	ws := strings.Fields(name)
	if len(ws) < 2 {
		return bisZu(name, 12)
	}
	return bisZu(ws[0], 12) + " " + string([]rune(ws[len(ws)-1])[:1]) + "."
}

// rufname is a name's first word, as a row says „mit Ben“.
func rufname(name string) string {
	if ws := strings.Fields(name); len(ws) > 0 {
		return ws[0]
	}
	return name
}

func bisZu(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// umfeldAm is when the net was last computed, for the status (a time only).
func (u *Umfeld) umfeldAm() *time.Time {
	if !u.Eingerichtet() {
		return nil
	}
	if am := u.Stand().Am; !am.IsZero() {
		return &am
	}
	return nil
}

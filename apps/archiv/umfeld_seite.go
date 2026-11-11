package main

import (
	"cmp"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
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
	umfeldZeigen = 30 // in the picture; „Mehr zeigen“ doubles it

	saysUmfeldGespeichert = "Gespeichert. Das Archiv liest jetzt deine Fotos aus Immich und rechnet dein Umfeld; bei vielen Fotos dauert das ein paar Minuten."
	saysSucheNiemand      = "Niemanden mit diesem Namen gefunden. Zu finden ist, wer in deinen Telefonbüchern steht oder auf deinen Fotos einen Namen hat."
	saysUmfeldVerbergen   = "Das ließ sich nicht speichern. Versuch es gleich noch einmal."
	saysUmfeldHinweis     = "Das Archiv zählt, an wie vielen Tagen zwei Menschen zusammen auf deinen Fotos sind; ein Tag an einem Ort zählt einmal, ein großes Gruppenfoto wenig, Altes weniger als Neues. Dazu kommt, was die Telefonbücher sagen: dieselbe Adresse, dieselbe Firma, eine eingetragene Beziehung. „Nicht berechnen“ (bei der Person selbst) nimmt jemanden ganz heraus, etwa Freunde der Kinder. Alles liegt in deinem Tresor; nur du siehst diese Seite, und nur solange dein Archiv offen ist."
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
	gesagt := map[string]string{verbergenAus: "%s ausgeblendet", verbergenNicht: "%s wird nicht mehr berechnet", "": "%s ist wieder da."}
	satz, erlaubt := gesagt[art]
	n := w.Umfeld.Netz()
	if !erlaubt || id == "" || n == nil {
		w.umfeldSeite(rw, r, http.StatusBadRequest, mitte, "", saysUmfeldVerbergen)
		return
	}
	k, bekannt := n.Person(id)
	if err := w.Umfeld.Verbergen(innen, id, art); err != nil {
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
		w.umfeldNetz(&b, n, mitte, r.URL.Query().Get("mehr") != "")
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
		if t.Als != "" {
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
func (w *Web) umfeldNetz(b *strings.Builder, n *Netz, mitte string, mehr bool) {
	ich := n.Ich()
	mitte = cmp.Or(mitte, n.Mitte())
	zeigen := umfeldZeigen
	if mehr {
		zeigen *= 2
	}
	k, ok := n.Person(mitte)
	vs := n.Nahe(mitte, zeigen)
	if ok && ich != "" && mitte != ich {
		umfeldKopf(b, n, k)
	}
	switch {
	case mitte == "":
		b.WriteString(`<p class=k>Noch keine Verbindungen: Ein Paar zählt, sobald es an zwei Tagen zusammen auf deinen Fotos ist oder die Telefonbücher etwas über die beiden sagen.</p>`)
	case !ok || len(vs) == 0:
		b.WriteString(`<p class=k>Im Umfeld steht diese Person noch nicht: Dafür braucht es zwei gemeinsame Fototage, einen Eintrag im Telefonbuch oder eine Beziehung von dir.</p>`)
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
		b.WriteString(umfeldBild(n, k, vs))
		b.WriteString(umfeldListe(n, k, vs))
		if len(vs) == zeigen && !mehr {
			fmt.Fprintf(b, `<p><a href="%s">Mehr zeigen</a></p>`, esc("/tresor/umfeld?"+url.Values{"mitte": {mitte}, "mehr": {"1"}}.Encode()))
		}
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
	fmt.Fprintf(b, `<section class=person id=person><div class=wer-ist>%s<h2>%s</h2></div>`, gesicht(k.Name), esc(k.Name))
	p, ok := n.Paar(k.ID, n.Ich())
	if !ok {
		b.WriteString(`</section>`)
		return
	}
	teile, weiter := []string{"Für dich: noch nichts eingetragen"}, "Beziehung eintragen"
	if p.Art != "" {
		teile, weiter = []string{"Für dich: " + map[bool]string{true: fremdWort(true), false: artJetzt(p.Art)}[p.Art == artFremd]}, "Was verbindet euch?"
	}
	if platz, von := n.Platz(k.ID); platz > 0 {
		wie := "lose verbunden"
		switch {
		case platz <= max(1, von/5):
			wie = "sehr nah"
		case platz <= max(2, von/2):
			wie = "nah"
		}
		teile = append(teile, fmt.Sprintf("%s (Nr. %d von %d)", wie, platz, von))
	}
	if z := p.Gemeinsam.Zuletzt; !z.IsZero() {
		teile = append(teile, "zuletzt gemeinsam am "+z.In(cmp.Or(n.Loc, time.Local)).Format("02.01.2006"))
	}
	fmt.Fprintf(b, `<p class=ist>%s</p><p class=summe>%s</p><p><a class=weiter href="%s">%s</a></p></section>`, esc(strings.Join(teile, " · ")),
		esc(cmp.Or(p.Satz, saysKanteNichts)), esc(kanteAdresse(k.ID, n.Ich())), weiter)
}

// umfeldListe is the ranked list: each row the person and the tie in one
// short line (Netz.Kurz), one link. From the person's own list it opens
// that person's page, which leads with what the two share; from anybody
// else's it opens what connects that one and the row's. No action on the
// row (owner 2026-11-11: „gefährlich platziert, dicke Finger“): Ausblenden
// and Nicht berechnen are at the end of the person's page (umfeldAktionen).
func umfeldListe(n *Netz, mitte Knoten, vs []Verbindung) string {
	var b strings.Builder
	b.WriteString(`<ol class=nahe>`)
	for _, v := range vs {
		ziel := umfeldMitte(v.Mit.ID)
		if mitte.ID != n.Ich() {
			ziel = kanteAdresse(mitte.ID, v.Mit.ID)
		}
		fmt.Fprintf(&b, `<li><a class=zeile href="%s">%s <span class=k>%s</span></a></li>`, esc(ziel), esc(n.Anzeige(v.Mit)), esc(n.Kurz(mitte.ID, v.Kante)))
	}
	b.WriteString(`</ol>`)
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
	text := map[string][2]string{verbergenAus: {"Ausblenden", "Blendet aus …"}, verbergenNicht: {"Nicht berechnen", "Nimmt heraus …"}, "": {"Zurückholen", "Holt zurück …"}}
	out := `<form method=post action="/tresor/umfeld/verbergen" class=knoepfe><input type=hidden name=mitte value="` + esc(mitte) + `"><input type=hidden name=id value="` + esc(id) + `">`
	for _, art := range arten {
		out += `<button class=k name=art value="` + art + `" data-laeuft="` + esc(text[art][1]) + `">` + esc(text[art][0]) + `</button>`
	}
	return out + `</form>`
}

// umfeldBild is the picture: the middle, the first fifth on the inner
// ring, up to half on the middle one, the rest outside; a line to each,
// its width the tie's strength, and thin ones among them.
func umfeldBild(n *Netz, mitte Knoten, vs []Verbindung) string {
	const c = 200.0
	ids, xy := make([]string, len(vs)), make([][2]float64, len(vs))
	ringe := [3][2]int{{0, max(1, len(vs)/5)}, {max(1, len(vs)/5), max(2, len(vs)/2)}, {max(2, len(vs)/2), len(vs)}}
	for ring, rg := range ringe {
		for i := rg[0]; i < min(rg[1], len(vs)); i++ {
			a := 2*math.Pi*float64(i-rg[0])/float64(rg[1]-rg[0]) + float64(ring)*0.5 - math.Pi/2
			r := []float64{70, 120, 165}[ring]
			ids[i], xy[i] = vs[i].Mit.ID, [2]float64{c + r*math.Cos(a), c + r*math.Sin(a)}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class=netzbild viewBox="0 0 400 400" role=img aria-label="%s">`, esc("Wer "+mitte.Name+" nahe steht"))
	for _, p := range n.Zwischen(ids) {
		fmt.Fprintf(&b, `<line class=zw x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, xy[p[0]][0], xy[p[0]][1], xy[p[1]][0], xy[p[1]][1])
	}
	for i, v := range vs {
		fmt.Fprintf(&b, `<line x1="200" y1="200" x2="%.1f" y2="%.1f" stroke-width="%.1f"/>`, xy[i][0], xy[i][1], 0.5+2.5*v.Kante.Staerke/vs[0].Kante.Staerke)
	}
	for i, v := range vs {
		b.WriteString(umfeldPunkt(v.Mit, xy[i][0], xy[i][1], 5, i%2 == 1 && xy[i][1] < c))
	}
	b.WriteString(`<g class=mitte>` + umfeldPunkt(mitte, c, c, 9, false) + `</g></svg>`)
	return b.String()
}

// umfeldPunkt is one person in the picture, a link that puts it in the
// middle; its label below it, or above (oben) where its neighbours' are.
func umfeldPunkt(k Knoten, x, y, r float64, oben bool) string {
	dy := r + 11
	if oben {
		dy = -r - 4
	}
	return fmt.Sprintf(`<a href="%s" aria-label="%s"><circle cx="%.1f" cy="%.1f" r="%.0f"/><text x="%.1f" y="%.1f">%s</text></a>`, esc(umfeldMitte(k.ID)),
		esc(k.Name+" in die Mitte"), x, y, r, x, y+dy, esc(kurzName(k.Name)))
}

// kurzName is a name in the picture: the first name and the last one's initial.
func kurzName(name string) string {
	ws := strings.Fields(name)
	if len(ws) < 2 {
		return bisZu(name, 12)
	}
	return bisZu(ws[0], 12) + " " + string([]rune(ws[len(ws)-1])[:1]) + "."
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

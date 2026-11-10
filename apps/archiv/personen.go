package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// „Ist das dieselbe Person?“ — msgvault's identity suggestions, put to the
// person on the open page with their evidence in plain German, and Ja /
// Nein, which accept or reject with the review token the person saw (a
// token gone stale means the evidence changed: the page fetches the
// suggestion again and says so). And two persons merged by hand: a
// preview of both (names, numbers, addresses), then the merge guarded by
// the two revisions the preview showed. msgvault records every merge and
// can split it again.

const (
	saysVorschlagVeraltet = "Der Vorschlag hat sich inzwischen geändert. Sieh ihn dir unten noch einmal an und entscheide neu."
	saysVorschlagWeg      = "Diesen Vorschlag gibt es nicht mehr; er ist schon entschieden."
	saysZweiPersonen      = "Die beiden sind im Archiv schon zwei gespeicherte Personen. Sieh sie dir mit „Zusammenführen ansehen“ an."
	saysEntscheidenFehler = "Das Archiv-Programm hat die Antwort nicht angenommen. Versuch es gleich noch einmal; hilft das nicht, sag dem Admin Bescheid."
	saysZusammenVeraltet  = "Eine der beiden Personen hat sich inzwischen geändert. Sieh dir die Vorschau noch einmal an."
	saysZusammenFehler    = "Die beiden ließen sich nicht zusammenführen. Sag dem Admin Bescheid."
	saysZusammenGleich    = "Wähl zwei verschiedene Personen."
	saysNichtBereit       = "Das Archiv ist nicht offen oder startet noch."
)

// Personenkarte keeps the suggestions as the page shows them, fetched
// again at most every 30 s (the live page re-renders often) and at once
// after a decision.
type Personenkarte struct {
	API *MsgvaultAPI
	Max int // suggestions shown at once (default 10)

	mu     sync.Mutex
	am     time.Time
	liste  []vorschlagText
	mehr   int // further open suggestions not shown
	andere int // open ones nobody can decide here (msgvault: endpoint_unsupported)
	fehler bool
	leute  []Person // the saved persons, for merging by hand
}

// vorschlagText is one suggestion, ready for the page.
type vorschlagText struct {
	ID       int64
	Token    string
	Links    string
	Rechts   string
	Warum    string
	Herkunft string
	Merge    [2]int64 // both sides are saved persons: merge instead (person_merge_required)
}

func (k *Personenkarte) Vergessen() {
	k.mu.Lock()
	k.am = time.Time{}
	k.mu.Unlock()
}

func (k *Personenkarte) holen(ctx context.Context) ([]vorschlagText, int, int, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.am.IsZero() && time.Since(k.am) < 30*time.Second {
		return k.liste, k.mehr, k.andere, k.fehler
	}
	max := k.Max
	if max <= 0 {
		max = 10
	}
	alle, err := k.API.Vorschlaege(ctx, 200)
	k.am, k.liste, k.mehr, k.andere, k.fehler = time.Now(), nil, 0, 0, err != nil
	k.leute, _ = k.API.Personen(ctx)
	if err != nil {
		if !errors.Is(err, errNichtBereit) {
			log.Printf("archiv: Vorschläge: %v", err)
		}
		return nil, 0, 0, true
	}
	namen := map[string]string{}
	for _, v := range alle {
		if !v.Offen() {
			continue
		}
		merge := v.Blocker != nil && *v.Blocker == "person_merge_required" && v.LeftPerson != nil && v.RightPerson != nil
		if !v.Actionable && !merge {
			k.andere++
			continue
		}
		if len(k.liste) >= max {
			k.mehr++
			continue
		}
		t := vorschlagText{ID: v.ID, Token: v.Token(), Warum: warum(v), Herkunft: herkunft(v.Source)}
		t.Links = k.name(ctx, namen, v.LeftKind, v.LeftID, v.LeftPerson)
		t.Rechts = k.name(ctx, namen, v.RightKind, v.RightID, v.RightPerson)
		if merge {
			t.Merge = [2]int64{v.LeftPerson.PersonID, v.RightPerson.PersonID}
		}
		k.liste = append(k.liste, t)
	}
	return k.liste, k.mehr, k.andere, false
}

// name says who one side of a suggestion is, as plainly as msgvault knows it.
func (k *Personenkarte) name(ctx context.Context, cache map[string]string, art string, id int64, person *personBindung) string {
	key := art + ":" + strconv.FormatInt(id, 10)
	if n, ok := cache[key]; ok {
		return n
	}
	n := ""
	switch art {
	case "participant":
		if person != nil {
			if p, err := k.API.Person(ctx, person.PersonID); err == nil && p.DisplayName != "" {
				n = p.DisplayName
			}
		}
		if t, err := k.API.Teilnehmer(ctx, id); err == nil {
			label := t.DisplayLabel
			if label == "" {
				label = t.DisplayName
			}
			adresse := ""
			for _, i := range t.Identifiers {
				if v := i.DisplayValue; v != "" {
					adresse = v
					break
				} else if i.Value != "" {
					adresse = i.Value
					break
				}
			}
			switch {
			case n != "" && adresse != "":
				n += " (" + adresse + ")"
			case n == "" && label != "" && adresse != "" && label != adresse:
				n = label + " (" + adresse + ")"
			case n == "" && label != "":
				n = label
			case n == "":
				n = adresse
			}
		}
		if n == "" {
			n = "ein Absender aus deinen Nachrichten"
		}
	case "person":
		if p, err := k.API.Person(ctx, id); err == nil && p.DisplayName != "" {
			n = p.DisplayName
		} else {
			n = "eine gespeicherte Person ohne Namen"
		}
	case "carddav_resource":
		n = "ein Kontakt aus deinem Handy-Adressbuch"
	default:
		n = "ein Eintrag im Archiv"
	}
	cache[key] = n
	return n
}

func warum(v Vorschlag) string {
	wert := ""
	if v.NormalizedValue != nil {
		wert = *v.NormalizedValue
	}
	dienst := "einem Dienst"
	if v.ServiceSlug != nil && *v.ServiceSlug != "" {
		dienst = *v.ServiceSlug
	}
	switch v.Basis {
	case "phone":
		if wert != "" {
			return "Beide haben die Telefonnummer " + wert + "."
		}
		return "Beide haben dieselbe Telefonnummer."
	case "email":
		if wert != "" {
			return "Beide haben die E-Mail-Adresse " + wert + "."
		}
		return "Beide haben dieselbe E-Mail-Adresse."
	case "display_name":
		if wert != "" {
			return "Beide heißen „" + wert + "“. Nur der gleiche Name: schau genau hin."
		}
		return "Beide haben denselben Namen. Nur der gleiche Name: schau genau hin."
	case "stable_provider_id":
		return "Beide sind dasselbe Konto bei " + dienst + "."
	case "service_scope_username":
		if wert != "" {
			return "Beide haben den Benutzernamen " + wert + " bei " + dienst + "."
		}
		return "Beide haben denselben Benutzernamen bei " + dienst + "."
	case "conversation_membership":
		return "Beide kommen in denselben Gesprächen vor."
	}
	return "Das Archiv hält sie für dieselbe Person."
}

func herkunft(source string) string {
	switch source {
	case "carddav_import":
		return "aus deinem Adressbuch"
	case "archive_observation":
		return "aus deinen Nachrichten"
	case "extraction", "enrichment":
		return "aus einer Auswertung deiner Nachrichten"
	}
	return ""
}

// --- the card on the open page

func (w *Web) personenKarte(b *strings.Builder) {
	if w.Personen == nil {
		return
	}
	b.WriteString(`<section class=quelle id=karte-personen data-live><h3>Ist das dieselbe Person?</h3>`)
	ctx, aus := context.WithTimeout(context.Background(), 8*time.Second)
	defer aus()
	liste, mehr, andere, fehler := w.Personen.holen(ctx)
	switch {
	case fehler:
		b.WriteString(`<p class=k>Die Vorschläge lassen sich gerade nicht abrufen. Die Seite versucht es gleich wieder.</p>`)
	case len(liste) == 0:
		b.WriteString(`<p class=k>Gerade gibt es keine Vorschläge. Wenn das Archiv zwei Einträge findet, die dieselbe Person sein könnten (etwa dieselbe Nummer bei WhatsApp und im Adressbuch), fragt es dich hier.</p>`)
	default:
		b.WriteString(`<p class=k>Das Archiv hat Einträge gefunden, die dieselbe Person sein könnten. Sagst du „Ja“, zeigt es sie künftig als eine Person; „Nein“ merkt es sich auch.</p>`)
		for _, v := range liste {
			fmt.Fprintf(b, `<div class=vorschlag><p><b>%s</b> und <b>%s</b></p><p class=k>%s`, esc(v.Links), esc(v.Rechts), esc(v.Warum))
			if v.Herkunft != "" {
				fmt.Fprintf(b, ` (gefunden %s)`, esc(v.Herkunft))
			}
			b.WriteString(`</p>`)
			if v.Merge != [2]int64{} {
				fmt.Fprintf(b, `<p class=k>Beide sind schon gespeicherte Personen; zusammen kommen sie nur, wenn du sie zusammenführst.</p><p><a href="/tresor/personen/vorschau?a=%d&amp;b=%d">Zusammenführen ansehen</a></p></div>`, v.Merge[0], v.Merge[1])
				continue
			}
			fmt.Fprintf(b, `<form method=post action="/tresor/personen/entscheiden" class=zeile><input type=hidden name=id value="%d"><input type=hidden name=token value="%s"><span>Dieselbe Person?</span><span class=knoepfe><button name=antwort value=ja data-laeuft="Merke …">Ja</button> <button name=antwort value=nein class=k data-laeuft="Merke …">Nein</button></span></form></div>`,
				v.ID, esc(v.Token))
		}
		if mehr > 0 {
			fmt.Fprintf(b, `<p class=k>%s danach.</p>`, esc(anzahl(mehr, "weiterer Vorschlag kommt", "weitere Vorschläge kommen")))
		}
	}
	if andere > 0 {
		fmt.Fprintf(b, `<p class=k>%s kann nur das Archiv-Programm selbst klären (unter „Archiv durchsuchen“ → People).</p>`, esc(anzahl(andere, "weiterer Vorschlag", "weitere Vorschläge")))
	}
	w.zusammenFormular(b)
	b.WriteString(`</section>`)
}

// zusammenFormular: two persons chosen by hand, shown first, merged after.
func (w *Web) zusammenFormular(b *strings.Builder) {
	w.Personen.mu.Lock()
	leute := append([]Person(nil), w.Personen.leute...)
	w.Personen.mu.Unlock()
	if len(leute) < 2 {
		return
	}
	sort.Slice(leute, func(i, j int) bool {
		return strings.ToLower(personName(leute[i])) < strings.ToLower(personName(leute[j]))
	})
	weg := w.geloeschteKarten()
	var opt strings.Builder
	for _, p := range leute {
		name := personName(p)
		if t, ok := p.GeloeschtAm(weg); ok {
			name += " (auf dem Handy gelöscht am " + t.Local().Format("02.01.2006") + ")"
		}
		fmt.Fprintf(&opt, `<option value="%d">%s</option>`, p.ID, esc(name))
	}
	fmt.Fprintf(b, `<details id=d-zusammen><summary>Zwei Personen von Hand zusammenführen</summary>
<p class=k>Wenn jemand zweimal im Archiv steht (etwa einmal mit Handynummer, einmal mit Mail-Adresse). Du siehst vorher, was zusammenkommt.</p>
<form method=get action="/tresor/personen/vorschau" class=feld>
<label>Diese Person bleibt<select name=a required><option value="">bitte wählen</option>%s</select></label>
<label>und diese geht in ihr auf<select name=b required><option value="">bitte wählen</option>%s</select></label>
<button class=k>Vorschau</button></form></details>`, opt.String(), opt.String())
}

// geloeschteKarten: the cards deleted on the phone, by UID (from the vault).
func (w *Web) geloeschteKarten() map[string]time.Time {
	innen, auf := w.Tresor.Innen()
	if !auf {
		return nil
	}
	return GeloeschtNachUID(innen)
}

func personName(p Person) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return "Person ohne Namen (Nr. " + strconv.FormatInt(p.ID, 10) + ")"
}

// --- the handlers

func (w *Web) personenBereit(rw http.ResponseWriter, r *http.Request) bool {
	if w.Personen == nil || w.Tresor.APIKey() == "" {
		w.antwort(rw, r, http.StatusLocked, "", saysNichtBereit)
		return false
	}
	return true
}

func (w *Web) entscheiden(rw http.ResponseWriter, r *http.Request) {
	if !w.personenBereit(rw, r) {
		return
	}
	id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	antwort, token := r.PostFormValue("antwort"), r.PostFormValue("token")
	if err != nil || id <= 0 || token == "" || (antwort != "ja" && antwort != "nein") {
		w.antwort(rw, r, http.StatusBadRequest, "", saysEntscheidenFehler)
		return
	}
	err = w.Personen.API.Entscheiden(r.Context(), id, antwort == "ja", token)
	w.Personen.Vergessen() // what the page shows next is fetched again, with fresh tokens
	if w.Geaendert != nil {
		w.Geaendert()
	}
	var f *apiFehler
	switch {
	case err == nil:
		m := "nein"
		if antwort == "ja" {
			m = "ja"
		}
		http.Redirect(rw, r, "/tresor?tab=fragen&m=person-"+m+"#karte-personen", http.StatusSeeOther)
	case errors.Is(err, errVeraltet):
		w.antwort(rw, r, http.StatusConflict, "", saysVorschlagVeraltet)
	case errors.Is(err, errZweiPersonen):
		w.antwort(rw, r, http.StatusConflict, "", saysZweiPersonen)
	case errors.As(err, &f) && f.Status == http.StatusNotFound:
		w.antwort(rw, r, http.StatusNotFound, "", saysVorschlagWeg)
	default:
		log.Printf("archiv: Vorschlag %d %s: %v", id, antwort, err)
		w.antwort(rw, r, http.StatusBadGateway, "", saysEntscheidenFehler)
	}
}

func zweiIDs(r *http.Request) (int64, int64, bool) {
	a, err1 := strconv.ParseInt(r.FormValue("a"), 10, 64)
	b, err2 := strconv.ParseInt(r.FormValue("b"), 10, 64)
	return a, b, err1 == nil && err2 == nil && a > 0 && b > 0
}

// vorschau shows both persons before a merge: who stays, who goes in,
// with every number and address msgvault knows for each.
func (w *Web) vorschau(rw http.ResponseWriter, r *http.Request) {
	if !w.personenBereit(rw, r) {
		return
	}
	a, b, ok := zweiIDs(r)
	if !ok || a == b {
		w.antwort(rw, r, http.StatusBadRequest, "", saysZusammenGleich)
		return
	}
	w.vorschauSeite(rw, r, a, b, "")
}

func (w *Web) vorschauSeite(rw http.ResponseWriter, r *http.Request, a, b int64, fehler string) {
	api := w.Personen.API
	pa, errA := api.Person(r.Context(), a)
	pb, errB := api.Person(r.Context(), b)
	if errA != nil || errB != nil {
		log.Printf("archiv: Vorschau %d/%d: %v %v", a, b, errA, errB)
		w.antwort(rw, r, http.StatusNotFound, "", "Eine der beiden Personen gibt es nicht mehr. Wähl noch einmal.")
		return
	}
	var s strings.Builder
	if fehler != "" {
		fmt.Fprintf(&s, `<p class="meldung fehler" role=alert>%s</p>`, esc(fehler))
	}
	s.WriteString(`<h2>Zwei Personen zusammenführen</h2><p>So sieht es aus. Erst „Ja, zusammenführen“ ändert etwas.</p>`)
	weg := w.geloeschteKarten()
	for i, p := range []Person{pa, pb} {
		rolle := "Bleibt (mit diesem Namen)"
		if i == 1 {
			rolle = "Geht in ihr auf"
		}
		fmt.Fprintf(&s, `<section class=quelle><h3>%s: %s</h3>`, rolle, esc(personName(p)))
		if t, ok := p.GeloeschtAm(weg); ok {
			fmt.Fprintf(&s, `<p class=k>Auf dem Handy gelöscht am %s; im Archiv bleibt sie.</p>`, t.Local().Format("02.01.2006"))
		}
		// what the messages used, then what the address book adds
		ids, err := api.Identitaeten(r.Context(), p.ID)
		punkte, err2 := api.Kontaktpunkte(r.Context(), p.ID)
		gesehen := map[string]bool{}
		var zeilen []string
		for _, i := range ids {
			gesehen[strings.ToLower(i.Value)] = true
			zeilen = append(zeilen, fmt.Sprintf(`<li>%s <span class=k>(%s, in deinen Nachrichten)</span></li>`, esc(i.Value), esc(identitaetArt(i.Kind))))
		}
		for _, i := range punkte {
			if i.Value == "" || gesehen[strings.ToLower(i.Value)] {
				continue
			}
			gesehen[strings.ToLower(i.Value)] = true
			zeilen = append(zeilen, fmt.Sprintf(`<li>%s <span class=k>(%s, im Adressbuch)</span></li>`, esc(i.Value), esc(identitaetArt(i.Kind))))
		}
		switch {
		case len(zeilen) > 0:
			s.WriteString(`<ul>` + strings.Join(zeilen, "") + `</ul>`)
		case err != nil || err2 != nil:
			s.WriteString(`<p class=k>Nummern und Adressen lassen sich gerade nicht abrufen.</p>`)
		default:
			s.WriteString(`<p class=k>Keine Nummer und keine Adresse bekannt.</p>`)
		}
		s.WriteString(`</section>`)
	}
	schluessel := make([]byte, 16)
	rand.Read(schluessel)
	fmt.Fprintf(&s, `<p class=k>Danach ist es eine Person mit dem Namen „%s“ und allen Nummern und Adressen von beiden. Das Archiv-Programm merkt sich das Zusammenführen und kann es wieder trennen.</p>
<form method=post action="/tresor/personen/zusammen"><input type=hidden name=a value="%d"><input type=hidden name=ar value="%d"><input type=hidden name=b value="%d"><input type=hidden name=br value="%d"><input type=hidden name=schluessel value="%s">%s</form>
<p><a href="/tresor/personen/vorschau?a=%d&amp;b=%d">Andersherum: „%s“ soll bleiben</a></p><p><a href="/tresor#karte-personen">Abbrechen</a></p>`,
		esc(personName(pa)), pa.ID, pa.Revision, pb.ID, pb.Revision, hex.EncodeToString(schluessel), knopf("Ja, zusammenführen", "Führe zusammen …", ""),
		pb.ID, pa.ID, esc(personName(pb)))
	code := http.StatusOK
	if fehler != "" {
		code = http.StatusConflict
	}
	seite(rw, code, "Zusammenführen", s.String(), "")
}

func identitaetArt(k string) string {
	switch strings.ToLower(k) {
	case "email":
		return "E-Mail"
	case "phone":
		return "Telefon"
	case "whatsapp":
		return "WhatsApp"
	case "signal":
		return "Signal"
	}
	return k
}

func (w *Web) zusammen(rw http.ResponseWriter, r *http.Request) {
	if !w.personenBereit(rw, r) {
		return
	}
	a, b, ok := zweiIDs(r)
	ar, err1 := strconv.ParseInt(r.PostFormValue("ar"), 10, 64)
	br, err2 := strconv.ParseInt(r.PostFormValue("br"), 10, 64)
	schluessel := r.PostFormValue("schluessel")
	if !ok || a == b || err1 != nil || err2 != nil || len(schluessel) < 16 || len(schluessel) > 128 {
		w.antwort(rw, r, http.StatusBadRequest, "", saysZusammenGleich)
		return
	}
	err := w.Personen.API.Zusammenfuehren(r.Context(), a, ar, b, br, schluessel)
	w.Personen.Vergessen()
	switch {
	case err == nil:
		log.Printf("archiv: Person %d in Person %d zusammengeführt (von Hand)", b, a)
		if w.Geaendert != nil {
			w.Geaendert()
		}
		http.Redirect(rw, r, "/tresor?tab=fragen&m=zusammen#karte-personen", http.StatusSeeOther)
	case errors.Is(err, errVeraltet):
		w.vorschauSeite(rw, r, a, b, saysZusammenVeraltet)
	default:
		log.Printf("archiv: zusammenführen %d+%d: %v", a, b, err)
		w.antwort(rw, r, http.StatusBadGateway, "", saysZusammenFehler)
	}
}

// abgleichJetzt: „Jetzt abgleichen“ on the page.
func (w *Web) abgleichJetzt(rw http.ResponseWriter, r *http.Request) {
	if _, auf := w.Tresor.Innen(); !auf || !w.Abgleich.Eingerichtet() {
		w.antwort(rw, r, http.StatusLocked, "", saysNichtBereit)
		return
	}
	w.Abgleich.Jetzt()
	http.Redirect(rw, r, "/tresor/quellen?m=abgleich#karte-kontakte", http.StatusSeeOther)
}

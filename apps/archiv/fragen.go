package main

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// „Wie steht X zu Y?“ (archiv#27 step 2; moved here from Homes'
// internal/netz/fragen.go with the same suggestions, homes ADR 0002): a
// pair that no phone book and no answer names a relation for may be
// asked, with a kind suggested from the phone books and the photos
// (vorschlag). The answers live only in the open vault
// (umfeld/antworten.json); a kind becomes a relation of the pair in place
// of the phone books', „Keine Beziehung“ and „Kennen sich nicht“ are
// never asked again, Später last. Nothing of it leaves the vault except a
// kind the person tapped, which Homes writes at once (uebernahme.go); the
// vault keeps what was written (Eingetragen).
//
// What is asked (owner 2026-11-11, „Das sind verdammt viele Fragen“;
// rigoros einfach): only what points to family. Photo days, group chats
// and mail threads together are enough for „kennen sich“: a tie on
// „Steht nahe“ and its reasons, never a question and never written to an
// address book. Asked are, in this order: pairs with the person („du“),
// the derived relations between others (abgeleitet.go), the family
// circle (familienkreis: the person, her relatives, and everyone with
// her family name or a relative's), then two others with the same family
// name, or whose shared address suggests family (vorschlag). Two of the
// family circle with one family name and many photo days together are
// asked with the person's own: a couple or siblings nobody wrote down. And never
// all at once: fragenJeBesuch a visit (Portion), the rest waits for
// „weitere Fragen“. An answer takes with it what follows from it by
// definition (folgern): written in the same tap, undone with it.

const (
	freundTage     = 5  // photo days from which a tie without a sign of family suggests Freund/in
	fragenJeBesuch = 10 // the questions of one visit's portion, „10 zum Nachbessern“
)

// The answers that are no kind; Fremd: the two do not know each other,
// their tie leaves the net.
const (
	artKeine   = "keine"
	artFremd   = "fremd"
	artSpaeter = "spaeter"
)

// arten are the kinds a question offers, in its order; „X ist <Art> von Y“.
var arten = []string{"partner", "expartner", "eltern", "kind", "geschwister", "grosseltern", "enkel", "onkel", "neffe", "cousin",
	"schwiegereltern", "schwiegerkind", "schwager", "freund", "kollege", "nachbar", "bekannt", "verwandt"}

// artWorte are the kinds as the family reads them (Homes' netz.art.<kind>).
var artWorte = map[string]string{"partner": "Partner/in", "expartner": "Ex-Partner/in", "eltern": "Mutter/Vater (Eltern)", "kind": "Kind",
	"geschwister": "Geschwister", "grosseltern": "Großeltern", "enkel": "Enkel/in", "onkel": "Onkel/Tante", "neffe": "Neffe/Nichte",
	"cousin": "Cousin/e", "schwiegereltern": "Schwiegereltern", "schwiegerkind": "Schwiegerkind", "schwager": "Schwager/Schwägerin",
	"freund": "Freund/in", "kollege": "Kollege/Kollegin", "nachbar": "Nachbar/in", "bekannt": "Bekannte/r", "verwandt": "Sonstige verwandt",
	artKeine: "Keine Beziehung", artFremd: "Kennen sich nicht"}

// familienArten are the kinds that make a tie between two others worth a
// question: a family's. Freund/in, Kollege/Kollegin, Nachbar/in and
// Bekannte/r between two others are not asked.
var familienArten = []string{"partner", "expartner", "eltern", "kind", "geschwister", "grosseltern", "enkel", "onkel", "neffe", "cousin",
	"schwiegereltern", "schwiegerkind", "schwager", "verwandt"}

// gegen is a kind seen from the other end; one not here is its own.
var gegen = map[string]string{"eltern": "kind", "kind": "eltern", "grosseltern": "enkel", "enkel": "grosseltern", "onkel": "neffe",
	"neffe": "onkel", "schwiegereltern": "schwiegerkind", "schwiegerkind": "schwiegereltern"}

// artWort is a kind with its explanation; kurzWort without it: „Mutter/Vater“.
func artWort(art string) string { return cmp.Or(artWorte[art], art) }
func kurzWort(art string) string {
	w, _, _ := strings.Cut(artWort(art), " (")
	return w
}

// antwort is the person's answer about a pair: X is Art of the other.
// Aus: it follows by definition from the answer about that pair
// (folgern), Weil says why, and it goes when that one goes. Trotz: the
// kind a derivation says X is, which the person answered otherwise; that
// conflict is not asked again.
type antwort struct {
	X           string       `json:"x"`
	Art         string       `json:"art"`
	Am          time.Time    `json:"am"`
	Eingetragen *Eingetragen `json:"eingetragen,omitempty"`
	Aus         string       `json:"aus,omitempty"`
	Weil        string       `json:"weil,omitempty"`
	Trotz       string       `json:"trotz,omitempty"`
	// Vorher: what the edge page's Festlegen replaced (kante.go); Vergessen brings it back
	Vorher *vorherStand `json:"vorher,omitempty"`
}

// merkmale are what a node's contacts and Immich say for the suggestions.
type merkmale struct {
	family   string
	geb      time.Time
	strassen []string
}

// Frage is „Wie steht X zu Y?“; Ich: Y is the person. Vorschlag is the
// kind suggested ("" none), Weil why. Abgeleitet: the kind follows from
// relations already known (abgeleitet.go), Weil names them; such a pair
// may have no tie (Kante then holds only A and B). Konflikt: the
// derivation disagrees with an answer or with another derivation.
// Gruppe: no pair but „Wie würdest du die Gruppe … nennen?“ (gruppen.go).
type Frage struct {
	X, Y                 Knoten
	Ich                  bool
	Kante                Kante
	Vorschlag, Weil      string
	Abgeleitet, Konflikt bool
	Gruppe               *GruppenFrage
	regel                int
}

// key is the question's: the pair's, or the group's.
func (f Frage) key() string {
	if f.Gruppe != nil {
		return gruppeKey(f.Gruppe.ID)
	}
	return paarKey(f.X.ID, f.Y.ID)
}

var errFrageWeg = errors.New("Umfeld: diese Frage gibt es nicht mehr")

func paarKey(a, b string) string { return min(a, b) + "|" + max(a, b) }

func datumVon(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }

// nachname is a family name compared: N's, else the name's last word;
// none for „Oma Hilde“ (a given name).
func nachname(family, name string) string {
	ws := strings.Fields(name)
	if _, muster := woerter.imMuster(name); muster { // „Mama von Lena“: no family name in it
		return klar(family)
	}
	if wort, _ := woerter.imNamen(name); family == "" && len(ws) > 1 && wort == "" {
		family = ws[len(ws)-1]
	}
	return klar(family)
}

// merkmaleBauen gathers each node's merkmale, a contact's over Immich's; n.mu is held or n is new.
func (n *Netz) merkmaleBauen() {
	n.merkmale = map[string]merkmale{}
	for id, name := range n.st.Namen {
		n.merkmale[id] = merkmale{family: nachname("", name), geb: datumVon(n.st.Geb[id])}
	}
	for _, k := range n.st.Kontakte {
		id := cmp.Or(k.Person, k.Key)
		m := n.merkmale[id]
		m.family = cmp.Or(nachname(k.Family, k.Name), m.family)
		if d := datumVon(k.Geb); !d.IsZero() {
			m.geb = d
		}
		m.strassen = append(m.strassen, k.Strassen...)
		n.merkmale[id] = m
	}
}

// gesagtDazu puts the person's answers into alle, each a relation in
// place of what the phone books say of the pair; Fremd takes the tie out.
func (n *Netz) gesagtDazu(alle map[[2]string]*Kante, nicht map[string]bool) {
	for key, a := range n.antworten {
		x, y, _ := strings.Cut(key, "|")
		if a.X == y {
			x, y = y, x
		}
		p := [2]string{min(x, y), max(x, y)}
		if a.Art == artFremd {
			delete(alle, p)
		}
		_, dx := n.knoten[x]
		if _, dy := n.knoten[y]; !dx || !dy || nicht[x] || nicht[y] || !slices.Contains(arten, a.Art) {
			continue
		}
		k := alle[p]
		if k == nil {
			k = &Kante{A: p[0], B: p[1]}
			alle[p] = k
		}
		if !slices.ContainsFunc(k.Fest, func(f festBand) bool { return f.Art == bandBeziehung }) {
			k.Staerke, k.Rang = k.Staerke+festGewicht[bandBeziehung], k.Rang+festGewicht[bandBeziehung]
		}
		k.Fest = append(slices.DeleteFunc(k.Fest, func(f festBand) bool { return f.Art == bandBeziehung }), festBand{A: y, B: x, Art: bandBeziehung, Wie: a.Art, Gesagt: true})
	}
}

// gesagtGrund is an answer as a reason seen from mitte: „Kind (von dir angegeben)“.
func gesagtGrund(mitte string, f festBand) string {
	art := f.Wie
	if f.A != mitte {
		art = cmp.Or(gegen[art], art)
	}
	return kurzWort(art) + " (von dir angegeben)"
}

// Fragen are all open questions in their order (offen); the page shows
// only a visit's portion of them (Portion).
func (n *Netz) Fragen() []Frage {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.offen()
}

// Portion is a visit's share of the open questions, the visit begun at
// seit: the first fragenJeBesuch less what the person answered since
// (Später included, what followed from an answer not), and weitere the
// rest. A question put back in this visit (Später) waits among weitere.
func (n *Netz) Portion(seit time.Time) (jetzt, weitere []Frage) {
	if n == nil {
		return nil, nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	getan := 0
	for _, a := range n.antworten {
		if a.Aus == "" && !a.Am.Before(seit) {
			getan++
		}
	}
	for _, a := range n.gruppen {
		if !a.Am.Before(seit) {
			getan++
		}
	}
	for _, f := range n.offen() {
		a, da := n.antworten[f.key()]
		if f.Gruppe != nil {
			g, gda := n.gruppen[f.Gruppe.ID]
			a, da = antwort{Art: g.Kategorie, Am: g.Am}, gda
		}
		if len(jetzt) < fragenJeBesuch-getan && !(da && a.Art == artSpaeter && !a.Am.Before(seit)) {
			jetzt = append(jetzt, f)
		} else {
			weitere = append(weitere, f)
		}
	}
	return jetzt, weitere
}

// sichtbar: id is a node and not hidden; n.mu is held.
func (n *Netz) sichtbar(id string) bool {
	_, da := n.knoten[id]
	return da && n.verborgen[id] == ""
}

// gleicherName: both have a family name, the same (klar: umlauts alike).
func (n *Netz) gleicherName(a, b string) bool {
	return n.merkmale[a].family != "" && n.merkmale[a].family == n.merkmale[b].family
}

// familienkreis is the person, her relatives (an answer of a family kind
// or a phone book's relation, from relative to relative) and everyone
// with the family name of one of them; empty without the person. n.mu is held.
func (n *Netz) familienkreis() map[string]bool {
	kreis := map[string]bool{}
	if n.ich == "" || !n.sichtbar(n.ich) {
		return kreis
	}
	nachbarn := map[string][]string{}
	verbinden := func(a, b string) {
		if a != "" && b != "" && n.sichtbar(a) && n.sichtbar(b) {
			nachbarn[a], nachbarn[b] = append(nachbarn[a], b), append(nachbarn[b], a)
		}
	}
	for key, a := range n.antworten {
		if slices.Contains(familienArten, a.Art) {
			x, y, _ := strings.Cut(key, "|")
			verbinden(x, y)
		}
	}
	for _, f := range n.st.Fest {
		if f.Art == bandBeziehung && relWie[klar(f.Wie)] != "" {
			verbinden(n.von[f.A], n.von[f.B])
		}
	}
	namen := map[string]bool{}
	for weg := []string{n.ich}; len(weg) > 0; weg = weg[1:] {
		if kreis[weg[0]] {
			continue
		}
		kreis[weg[0]] = true
		if f := n.merkmale[weg[0]].family; f != "" {
			namen[f] = true
		}
		weg = append(weg, nachbarn[weg[0]]...)
	}
	for id := range n.knoten {
		if n.sichtbar(id) && namen[n.merkmale[id].family] {
			kreis[id] = true
		}
	}
	for _, id := range n.familienGruppen() { // a group the person named Familie
		kreis[id] = true
	}
	return kreis
}

// offen are the open questions, none about a hidden person, in the order
// of „What is asked“: the person's, the active groups (gruppen.go), the
// derived between others, the family circle, then the rest; within each a
// derived one first, then the strongest tie; the skipped last. A pair a
// name's word or the box's model points to family (hinweise.go) is asked
// with or without a tie. n.mu is held.
func (n *Netz) offen() (out []Frage) {
	offenesPaar := func(a, b string) bool { // shown, no answer but Später, no relation in a phone book
		return a != b && n.fragbar(a) && n.fragbar(b) && !n.imBuchBeziehung(a, b) &&
			cmp.Or(n.antworten[paarKey(a, b)].Art, artSpaeter) == artSpaeter
	}
	gesehen := map[string]bool{}
	for _, a := range n.abgeleitete() {
		x, y, art := a.X, a.Y, a.Art
		if !n.fragbar(x) || !n.fragbar(y) {
			continue
		}
		if x == n.ich { // „Wie steht Carla zu dir?“
			x, y, art = y, x, cmp.Or(gegen[art], art)
		}
		gesehen[paarKey(x, y)] = true
		weil := a.Weil
		if alt, da := n.antworten[paarKey(x, y)]; a.Konflikt && da && alt.Art != artSpaeter {
			wie := alt.Art
			if alt.X != x {
				wie = cmp.Or(gegen[wie], wie)
			}
			weil += "; du hattest „" + kurzWort(wie) + "“ angegeben"
		}
		out = append(out, Frage{X: n.knoten[x], Y: n.knoten[y], Ich: y == n.ich, Kante: n.kanteZwischen(x, y), Vorschlag: art, Weil: weil, Abgeleitet: true, Konflikt: a.Konflikt, regel: a.regel})
	}
	kreis := n.familienkreis()
	fragen := func(x, y string, k Kante, nurFamilie bool) {
		if x == n.ich || y != n.ich && n.musterVon(y, x) { // „Sabine ist für Lena …“, as her card says
			x, y = y, x
		}
		art, weil := n.vorschlag(x, y, &k)
		if nurFamilie && !slices.Contains(familienArten, art) {
			return // they know each other: a reason on „Steht nahe“, no question
		}
		out = append(out, Frage{X: n.knoten[x], Y: n.knoten[y], Ich: y == n.ich, Kante: k, Vorschlag: art, Weil: weil})
	}
	for _, ks := range n.kanten {
		for _, k := range ks {
			key := paarKey(k.A, k.B)
			if gesehen[key] || !k.belegt() || !offenesPaar(k.A, k.B) {
				gesehen[key] = true
				continue
			}
			gesehen[key] = true
			muster, _ := n.musterFuer(k.A, k.B) // „Freundin von Lena“ is asked, as a family kind is
			fragen(k.A, k.B, *k, !n.gleicherName(k.A, k.B) && !(kreis[k.A] && kreis[k.B]) && muster == "")
		}
	}
	ids := slices.Sorted(maps.Keys(kreis))
	for i, a := range ids { // the family circle, ties or not
		for _, b := range ids[i+1:] {
			if !gesehen[paarKey(a, b)] && offenesPaar(a, b) {
				gesehen[paarKey(a, b)] = true
				fragen(a, b, n.kanteZwischen(a, b), false)
			}
		}
	}
	for _, p := range n.hinweisPaare() { // a name's word, the model's reading: ties or not
		if !gesehen[paarKey(p[0], p[1])] && offenesPaar(p[0], p[1]) {
			gesehen[paarKey(p[0], p[1])] = true
			fragen(p[0], p[1], n.kanteZwischen(p[0], p[1]), true)
		}
	}
	out = append(out, n.gruppenFragen()...)
	spaeter := func(f Frage) int {
		art := n.antworten[f.key()].Art
		if f.Gruppe != nil {
			art = n.gruppen[f.Gruppe.ID].Kategorie
		}
		if art == artSpaeter {
			return 1
		}
		return 0
	}
	gruppe := func(f Frage) int {
		switch {
		case f.Ich:
			return 0
		case f.Gruppe == nil && kreis[f.X.ID] && kreis[f.Y.ID] && n.gleicherName(f.X.ID, f.Y.ID) && f.Kante.Tage >= freundTage:
			return 0 // one family name, often together, in the family: likely a couple or siblings (owner 2026-11-11, „Sabine fehlt als Partnerin“)
		case f.Gruppe != nil:
			return 1
		case f.Abgeleitet:
			return 2
		case f.Gruppe == nil && n.ausMuster(f):
			return 2 // a card's name says it (owner 2026-11-11: before the model's)
		case kreis[f.X.ID] && kreis[f.Y.ID]:
			return 3
		}
		return 4
	}
	abgeleitetZuerst := func(f Frage) int {
		if f.Abgeleitet {
			return 0
		}
		return 1
	}
	slices.SortFunc(out, func(a, b Frage) int {
		return cmp.Or(cmp.Compare(spaeter(a), spaeter(b)), cmp.Compare(gruppe(a), gruppe(b)), cmp.Compare(abgeleitetZuerst(a), abgeleitetZuerst(b)),
			cmp.Compare(a.regel, b.regel), cmp.Compare(b.Kante.Rang, a.Kante.Rang), cmp.Compare(gruppenZeit(b), gruppenZeit(a)), strings.Compare(a.key(), b.key()))
	})
	return out
}

// kanteZwischen is the tie of x and y; one with only A and B when there
// is none. n.mu is held.
func (n *Netz) kanteZwischen(x, y string) Kante {
	for _, k := range n.kanten[x] {
		if andererVon(k, x) == y {
			return *k
		}
	}
	return Kante{A: min(x, y), B: max(x, y), Gruppen: n.paarGruppen[paarKey(x, y)]}
}

// gruppenZeit orders group questions, the newest first; 0 for a pair.
func gruppenZeit(f Frage) int64 {
	if f.Gruppe == nil {
		return 0
	}
	return f.Gruppe.zuletzt.Unix()
}

// vorschlag is the kind X likely is of Y, and why; "" none. No gender
// guessed: a relation word a name begins with toward the person („Oma
// Hilde“, beziehungswoerter.yml); what the box's model read in their
// messages (hinweise.go); the same family name and address make family,
// the age gap its kind; a group named Familie; the same address and a
// similar age Partner/in; the same firm Kollege/Kollegin; a group named
// Arbeit, Nachbarschaft or Freundeskreis; the same street, another house
// Nachbar/in; many photo days without a sign of family (both family names
// known and different) Freund/in. One family name and many photo days
// with both birthdays known make family by the age gap, as an address
// does (a small gap between adults Partner/in); without them, no guess.
func (n *Netz) vorschlag(x, y string, k *Kante) (string, string) {
	mx, my := n.merkmale[x], n.merkmale[y]
	fest := func(art string) bool {
		return slices.ContainsFunc(k.Fest, func(f festBand) bool { return f.Art == art })
	}
	familie := mx.family != "" && mx.family == my.family
	jahre := -1.0
	if !mx.geb.IsZero() && !my.geb.IsZero() {
		jahre = math.Abs(my.geb.Sub(mx.geb).Hours()) / 24 / 365.25
	}
	aelter := mx.geb.Before(my.geb)
	abstand := fmt.Sprintf("%d Jahre Altersunterschied", int(math.Round(jahre)))
	wort, wortArten := n.namensWort(x)
	h, hinweis := n.hinweise[paarKey(x, y)]
	if hinweis && h.X != x {
		h.Art = cmp.Or(gegen[h.Art], h.Art)
	}
	gArt, gWeil := n.gruppenVorschlag(k)
	mArt, mWeil := n.musterFuer(x, y)
	const weilFamilie = "gleicher Nachname und gleiche Adresse"
	switch {
	case mArt != "":
		return mArt, mWeil
	case y == n.ich && wort != "":
		return wortArten[0], "„" + wort + "“ im Namen"
	case hinweis:
		return h.Art, h.Weil
	case fest(bandAdresse) && familie && jahre < 0:
		return "verwandt", weilFamilie
	case fest(bandAdresse) && familie:
		return familienArt(jahre, aelter, n.now(), mx.geb, my.geb), weilFamilie + ", " + abstand
	case familie && k.Tage >= freundTage && jahre >= 0:
		return familienArt(jahre, aelter, n.now(), mx.geb, my.geb), "gleicher Nachname, " + anzahlT(k.Tage, "Tag", "Tage") + " zusammen auf deinen Fotos, " + abstand
	case gArt == "verwandt":
		return gArt, gWeil
	case fest(bandAdresse) && jahre >= 0 && jahre < 12:
		return "partner", "gleiche Adresse, " + abstand
	case fest(bandFirma):
		return "kollege", "gleiche Firma"
	case gArt != "":
		return gArt, gWeil
	case !fest(bandAdresse) && slices.ContainsFunc(mx.strassen, func(s string) bool { return slices.Contains(my.strassen, s) }):
		return "nachbar", "gleiche Straße, anderes Haus"
	case mx.family != "" && my.family != "" && !familie && k.Tage >= freundTage:
		return "freund", anzahlT(k.Tage, "Tag", "Tage") + " zusammen auf deinen Fotos, kein Zeichen von Familie"
	}
	return "", ""
}

// namensWort is the relation word one of x's names begins with („Oma
// Hilde“: Immich's name or a contact's), and its kinds; n.mu is held.
func (n *Netz) namensWort(x string) (string, []string) {
	namen := []string{n.knoten[x].Name}
	for _, k := range n.st.Kontakte {
		if cmp.Or(k.Person, k.Key) == x {
			namen = append(namen, k.Name)
		}
	}
	for _, name := range namen {
		if wort, arten := woerter.imNamen(name); wort != "" {
			return wort, arten
		}
	}
	return "", nil
}

// familienArt is the kind within one family at one address, by the age gap.
func familienArt(jahre float64, aelter bool, jetzt, gx, gy time.Time) string {
	juenger := jetzt.Sub(map[bool]time.Time{true: gy, false: gx}[aelter]).Hours() / 24 / 365.25
	switch {
	case jahre < 12 && juenger < 25:
		return "geschwister"
	case jahre < 12:
		return "partner"
	case jahre < 16:
		return "verwandt"
	case jahre <= 45:
		return map[bool]string{true: "eltern", false: "kind"}[aelter]
	}
	return map[bool]string{true: "grosseltern", false: "enkel"}[aelter]
}

// gueltig: art is an answer one may give.
func gueltig(art string) bool {
	return art == artKeine || art == artFremd || art == artSpaeter || slices.Contains(arten, art)
}

// Beantworten keeps the person's answer about x and y: a kind (x is that
// of y), Keine, Fremd or Spaeter; the question as it was asked, x first,
// and the answers as they now are, for the vault. A kind takes with it
// what follows from it by definition (folgern); an answer to a conflict
// keeps what the derivation said, so it is not asked again.
func (n *Netz) Beantworten(x, y, art string) (Frage, map[string]antwort, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	fs := n.offen()
	i := slices.IndexFunc(fs, func(f Frage) bool { return f.Gruppe == nil && paarKey(f.X.ID, f.Y.ID) == paarKey(x, y) })
	if i < 0 || !gueltig(art) {
		return Frage{}, nil, errFrageWeg
	}
	f := fs[i]
	if f.X.ID != x {
		f.X, f.Y = f.Y, f.X
		f.Ich = f.Y.ID == n.ich
		f.Vorschlag = cmp.Or(gegen[f.Vorschlag], f.Vorschlag)
	}
	vorher := map[string]bool{}
	for _, a := range n.abgeleitete() {
		vorher[paarKey(a.X, a.Y)] = true
	}
	a := antwort{X: x, Art: art, Am: n.now()}
	if f.Konflikt && art != artSpaeter {
		a.Trotz = f.Vorschlag
	}
	n.antworten[paarKey(x, y)] = a
	if slices.Contains(arten, art) {
		n.folgern(paarKey(x, y), vorher)
	}
	n.bauen()
	return f, maps.Clone(n.antworten), nil
}

// folgern writes what follows by definition (abgeleitet.go, definitorisch)
// from the answer about the pair aus and was not derived before it, each
// marked as from aus, and what follows from those in turn. It never
// writes over an answer (Später aside) and never a conflict: those stay
// questions. n.mu is held.
func (n *Netz) folgern(aus string, vorher map[string]bool) {
	for neu := true; neu; {
		neu = false
		for _, a := range n.abgeleitete() {
			key := paarKey(a.X, a.Y)
			if alt, da := n.antworten[key]; !definitorisch(a.regel) || a.Konflikt || vorher[key] || da && alt.Art != artSpaeter {
				continue
			}
			n.antworten[key] = antwort{X: a.X, Art: a.Art, Am: n.now(), Aus: aus, Weil: a.Weil}
			neu = true
		}
	}
}

// Vergessen drops the answer about the pair and what followed from it;
// its question may come again. An answer the edge page set over another
// (Vorher) gives that one back, and what had followed from it.
func (n *Netz) Vergessen(key string) (map[string]antwort, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, da := n.antworten[key]; !da {
		return nil, errFrageWeg
	}
	alt := n.antworten[key]
	delete(n.antworten, key)
	maps.DeleteFunc(n.antworten, func(_ string, a antwort) bool { return a.Aus == key })
	if v := alt.Vorher; v != nil { // the edge page set it over another: that one comes back, with what followed from it
		if v.Antwort != nil {
			n.antworten[key] = *v.Antwort
		}
		for k, a := range v.Folgen {
			if _, da := n.antworten[k]; !da {
				n.antworten[k] = a
			}
		}
	}
	n.bauen()
	return maps.Clone(n.antworten), nil
}

// Eintragen keeps in the answer about the pair what Homes wrote (nil: nothing).
func (n *Netz) Eintragen(key string, e *Eingetragen) (map[string]antwort, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	a, da := n.antworten[key]
	if !da {
		return nil, errFrageWeg
	}
	a.Eingetragen = e
	n.antworten[key] = a
	return maps.Clone(n.antworten), nil
}

// Gegeben is an answer as the page lists it: the pair (X is Art of Y),
// its key, what the phone books already say of the two ("" nothing),
// where it would go (Karte) and what Homes wrote (nil nothing); Aus and
// Weil for one that followed from the answer about the pair Aus.
type Gegeben struct {
	Key         string
	X, Y        Knoten
	Ich         bool
	Art         string
	Am          time.Time
	Buch        string
	Karte       Uebernahme
	Eingetragen *Eingetragen
	Aus, Weil   string
}

// Gegebenes is the answer about the pair key, as Gegebene lists it.
func (n *Netz) Gegebenes(key string) (Gegeben, bool) {
	gs := n.gegebene(key)
	if key == "" || len(gs) == 0 {
		return Gegeben{}, false
	}
	return gs[0], true
}

// Gefolgerte are the answers that followed from the answer key (folgern).
func (n *Netz) Gefolgerte(key string) (out []Gegeben) {
	if key == "" {
		return nil
	}
	for _, g := range n.Gegebene() {
		if g.Aus == key {
			out = append(out, g)
		}
	}
	return out
}

// Gegebene are the person's answers that are no Später, the newest first.
func (n *Netz) Gegebene() []Gegeben { return n.gegebene("") }

// gegebene are the answers, only key's when key is set.
func (n *Netz) gegebene(nur string) (out []Gegeben) {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for key, a := range n.antworten {
		if nur != "" && key != nur {
			continue
		}
		x, y, _ := strings.Cut(key, "|")
		if a.X == y {
			x, y = y, x
		}
		kx, dx := n.knoten[x]
		ky, dy := n.knoten[y]
		if a.Art == artSpaeter || !dx || !dy {
			continue
		}
		if y != n.ich && x == n.ich { // „Anna ist Kind von dir“ reads better than „du bist …“
			x, y, kx, ky, a.Art = y, x, ky, kx, cmp.Or(gegen[a.Art], a.Art)
		}
		g := Gegeben{Key: key, X: kx, Y: ky, Ich: y == n.ich, Art: a.Art, Am: a.Am, Buch: n.imBuch(x, y), Eingetragen: a.Eingetragen, Aus: a.Aus, Weil: a.Weil}
		if slices.Contains(arten, a.Art) {
			g.Karte = n.uebernahme(x, y, a.Art)
		}
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b Gegeben) int { return cmp.Or(b.Am.Compare(a.Am), strings.Compare(a.Key, b.Key)) })
	return out
}

// imBuch is what the phone books say of x and y, as a sentence („Anna
// Berg ist Kind von Ben Berg“); "" nothing. A card's relation says its
// other is the card's Wie. n.mu is held.
func (n *Netz) imBuch(x, y string) string {
	for _, f := range n.st.Fest {
		a, b := n.von[f.A], n.von[f.B]
		if f.Art == bandBeziehung && f.Wie != "" && (a == x && b == y || a == y && b == x) {
			return n.knoten[b].Name + " ist " + f.Wie + " von " + n.knoten[a].Name
		}
	}
	return ""
}

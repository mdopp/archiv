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
// tie among either end's closest that no phone book and no answer names
// a relation for is asked, the strongest first, with a kind suggested
// from the phone books and the photos (vorschlag). The answers live only
// in the open vault (umfeld/antworten.json); a kind becomes a relation of
// the pair in place of the phone books', „Keine Beziehung“ and „Kennen
// sich nicht“ are never asked again, Später last. Nothing of it leaves
// the vault except a kind the person tapped, which Homes writes at once
// (uebernahme.go); the vault keeps what was written (Eingetragen).

const (
	fragenJe   = 5 // a tie is asked about while among either end's closest five
	freundTage = 5 // photo days from which a tie without a sign of family suggests Freund/in
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
type antwort struct {
	X           string       `json:"x"`
	Art         string       `json:"art"`
	Am          time.Time    `json:"am"`
	Eingetragen *Eingetragen `json:"eingetragen,omitempty"`
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
// may have no tie (Kante then holds only A and B).
type Frage struct {
	X, Y            Knoten
	Ich             bool
	Kante           Kante
	Vorschlag, Weil string
	Abgeleitet      bool
	regel           int
}

var errFrageWeg = errors.New("Umfeld: diese Frage gibt es nicht mehr")

func paarKey(a, b string) string { return min(a, b) + "|" + max(a, b) }

func datumVon(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }

// nachname is a family name compared: N's, else the name's last word;
// none for „Oma Hilde“ (a given name).
func nachname(family, name string) string {
	ws := strings.Fields(name)
	if family == "" && len(ws) > 1 && rufnamen[strings.ToLower(ws[0])] == "" {
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

// Fragen are the open questions: the derived ones first (abgeleitet.go),
// then the strongest tie first, the skipped last; none about a hidden person.
func (n *Netz) Fragen() []Frage {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.offen()
}

// offen are the open questions; n.mu is held.
func (n *Netz) offen() (out []Frage) {
	gefragt := map[*Kante]bool{}
	for _, ks := range n.kanten {
		ks = slices.DeleteFunc(slices.Clone(ks), func(k *Kante) bool { return n.verborgen[k.A] != "" || n.verborgen[k.B] != "" })
		slices.SortFunc(ks, func(a, b *Kante) int { return cmp.Compare(b.Rang, a.Rang) })
		for _, k := range ks[:min(fragenJe, len(ks))] {
			gefragt[k] = !slices.ContainsFunc(k.Fest, func(f festBand) bool { return f.Art == bandBeziehung }) &&
				cmp.Or(n.antworten[paarKey(k.A, k.B)].Art, artSpaeter) == artSpaeter
		}
	}
	abg := n.abgeleitete()
	abgeleitet := map[string]bool{}
	for _, a := range abg {
		x, y, art := a.X, a.Y, a.Art
		if x == n.ich { // „Wie steht Carla zu dir?“
			x, y, art = y, x, cmp.Or(gegen[art], art)
		}
		abgeleitet[paarKey(x, y)] = true
		out = append(out, Frage{X: n.knoten[x], Y: n.knoten[y], Ich: y == n.ich, Kante: n.kanteZwischen(x, y), Vorschlag: art, Weil: a.Weil, Abgeleitet: true, regel: a.regel})
	}
	for k, ja := range gefragt {
		if ja && !abgeleitet[paarKey(k.A, k.B)] {
			x, y := k.A, k.B
			if x == n.ich {
				x, y = y, x
			}
			art, weil := n.vorschlag(x, y, k)
			out = append(out, Frage{X: n.knoten[x], Y: n.knoten[y], Ich: y == n.ich, Kante: *k, Vorschlag: art, Weil: weil})
		}
	}
	spaeter := func(f Frage) int {
		if n.antworten[paarKey(f.X.ID, f.Y.ID)].Art == artSpaeter {
			return 1
		}
		return 0
	}
	abgeleitetZuerst := func(f Frage) int {
		if f.Abgeleitet {
			return 0
		}
		return 1
	}
	slices.SortFunc(out, func(a, b Frage) int {
		return cmp.Or(cmp.Compare(spaeter(a), spaeter(b)), cmp.Compare(abgeleitetZuerst(a), abgeleitetZuerst(b)), cmp.Compare(a.regel, b.regel),
			cmp.Compare(b.Kante.Rang, a.Kante.Rang), strings.Compare(paarKey(a.X.ID, a.Y.ID), paarKey(b.X.ID, b.Y.ID)))
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
	return Kante{A: min(x, y), B: max(x, y)}
}

// rufnamen are a name's first words that say the relation to the person.
var rufnamen = map[string]string{"oma": "grosseltern", "opa": "grosseltern", "tante": "onkel", "onkel": "onkel"}

// vorschlag is the kind X likely is of Y, and why; "" none. Deterministic,
// no gender guessed: a „Oma“ in the name toward the person; the same
// family name and address make family, the age gap its kind; the same
// address and a similar age Partner/in; the same firm Kollege/Kollegin;
// the same street, another house Nachbar/in; many photo days without a
// sign of family (both family names known and different) Freund/in.
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
	erstes, _, _ := strings.Cut(n.knoten[x].Name, " ")
	const weilFamilie = "gleicher Nachname und gleiche Adresse"
	switch {
	case y == n.ich && rufnamen[strings.ToLower(erstes)] != "":
		return rufnamen[strings.ToLower(erstes)], "„" + erstes + "“ im Namen"
	case fest(bandAdresse) && familie && jahre < 0:
		return "verwandt", weilFamilie
	case fest(bandAdresse) && familie:
		return familienArt(jahre, aelter, n.now(), mx.geb, my.geb), weilFamilie + ", " + abstand
	case fest(bandAdresse) && jahre >= 0 && jahre < 12:
		return "partner", "gleiche Adresse, " + abstand
	case fest(bandFirma):
		return "kollege", "gleiche Firma"
	case !fest(bandAdresse) && slices.ContainsFunc(mx.strassen, func(s string) bool { return slices.Contains(my.strassen, s) }):
		return "nachbar", "gleiche Straße, anderes Haus"
	case mx.family != "" && my.family != "" && !familie && k.Tage >= freundTage:
		return "freund", anzahlT(k.Tage, "Tag", "Tage") + " zusammen auf deinen Fotos, kein Zeichen von Familie"
	}
	return "", ""
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
// and the answers as they now are, for the vault.
func (n *Netz) Beantworten(x, y, art string) (Frage, map[string]antwort, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	fs := n.offen()
	i := slices.IndexFunc(fs, func(f Frage) bool { return paarKey(f.X.ID, f.Y.ID) == paarKey(x, y) })
	if i < 0 || !gueltig(art) {
		return Frage{}, nil, errFrageWeg
	}
	f := fs[i]
	if f.X.ID != x {
		f.X, f.Y = f.Y, f.X
		f.Ich = f.Y.ID == n.ich
	}
	n.antworten[paarKey(x, y)] = antwort{X: x, Art: art, Am: n.now()}
	n.bauen()
	return f, maps.Clone(n.antworten), nil
}

// Vergessen drops the answer about the pair; its question may come again.
func (n *Netz) Vergessen(key string) (map[string]antwort, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, da := n.antworten[key]; !da {
		return nil, errFrageWeg
	}
	delete(n.antworten, key)
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
// where it would go (Karte) and what Homes wrote (nil nothing).
type Gegeben struct {
	Key         string
	X, Y        Knoten
	Ich         bool
	Art         string
	Am          time.Time
	Buch        string
	Karte       Uebernahme
	Eingetragen *Eingetragen
}

// Gegebenes is the answer about the pair key, as Gegebene lists it.
func (n *Netz) Gegebenes(key string) (Gegeben, bool) {
	gs := n.gegebene(key)
	if key == "" || len(gs) == 0 {
		return Gegeben{}, false
	}
	return gs[0], true
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
		if y != n.ich && x == n.ich && slices.Contains(arten, a.Art) { // „Anna ist Kind von dir“ reads better than „du bist …“
			x, y, kx, ky, a.Art = y, x, ky, kx, cmp.Or(gegen[a.Art], a.Art)
		}
		g := Gegeben{Key: key, X: kx, Y: ky, Ich: y == n.ich, Art: a.Art, Am: a.Am, Buch: n.imBuch(x, y), Eingetragen: a.Eingetragen}
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

package main

import (
	"cmp"
	"maps"
	"slices"
	"strings"
)

// What was recognised (archiv#42, owner 2026-11-11: „eine Zeile auf dem
// Arbeitsplatz, was erkannt wurde, und von dort zu den Vorschlägen“): the
// suggestions the archive made by itself, each waiting for the person's
// tap: relations the box's model read in messages (hinweise.go) or a
// card's name says (namensmuster.go), groups' categories the model or a
// title suggests (gruppenmodell.go, gruppen.go), birthdays from wishes
// (geburtstage.go). Erkannt counts them for the workplace's line; the
// page /tresor/erkannt lists them (erkannt_seite.go).

// Erkannt is the workplace's line: messages the model read and still
// waiting (Gezaehlt: counted since the opening), the open suggestions,
// the groups the person named of all.
type Erkannt struct {
	Modell                   bool
	Gelesen, Offen           int
	Gezaehlt                 bool
	Beziehungen, Geburtstage int
	GruppenVorschlaege       int
	GruppenEingeordnet       int
	GruppenAlle              int
}

// Vorschlaege are all the open suggestions.
func (e Erkannt) Vorschlaege() int { return e.Beziehungen + e.Geburtstage + e.GruppenVorschlaege }

// ErkannterBezug is a suggested relation: X is Art of Y, Weil why, Quelle
// where it came from („modell“, „name“).
type ErkannterBezug struct {
	X, Y      Knoten
	Ich       bool
	Art, Weil string
	Quelle    string
}

// ErkannteBeziehungen are the relations suggested by the model and the
// cards' names whose pair the person has not answered (Später aside) and
// no phone book names, the name's first, then by name.
func (n *Netz) ErkannteBeziehungen() (out []ErkannterBezug) {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	gesehen := map[string]bool{}
	dazu := func(x, y, art, weil, quelle string) {
		key := paarKey(x, y)
		if gesehen[key] || x == y || !n.fragbar(x) || !n.fragbar(y) || n.imBuchBeziehung(x, y) ||
			cmp.Or(n.antworten[key].Art, artSpaeter) != artSpaeter {
			return
		}
		gesehen[key] = true
		if x == n.ich {
			x, y, art = y, x, cmp.Or(gegen[art], art)
		}
		out = append(out, ErkannterBezug{X: n.knoten[x], Y: n.knoten[y], Ich: y == n.ich, Art: art, Weil: weil, Quelle: quelle})
	}
	for _, p := range n.musterVorschlaege() {
		art, weil := n.musterFuer(p[0], p[1])
		dazu(p[0], p[1], art, weil, "name")
	}
	for _, key := range slices.Sorted(maps.Keys(n.hinweise)) {
		h := n.hinweise[key]
		a, b, _ := strings.Cut(key, "|")
		y := b
		if h.X == b {
			y = a
		}
		dazu(h.X, y, h.Art, h.Weil, "modell")
	}
	return out
}

// GruppenVorschlaege are the groups of the list the person has not named
// whose category the model or the title suggests.
func GruppenVorschlaege(zs []GruppenZeile) (out []GruppenZeile) {
	for _, z := range zs {
		if z.Kategorie == "" && z.Vorschlag != "" {
			out = append(out, z)
		}
	}
	return out
}

// Erkannt counts what was recognised, with the round's count of what waits.
func (n *Netz) Erkannt(st UmfeldStand, modell bool) Erkannt {
	if n == nil {
		return Erkannt{}
	}
	zs := n.Gruppenliste()
	e := Erkannt{Modell: modell, Beziehungen: len(n.ErkannteBeziehungen()), Geburtstage: len(n.GeburtstagVorschlaege()),
		GruppenVorschlaege: len(GruppenVorschlaege(zs)), GruppenAlle: len(zs), Gezaehlt: st.ModellGezaehlt, Offen: st.ModellOffen}
	for _, z := range zs {
		if z.Kategorie != "" {
			e.GruppenEingeordnet++
		}
	}
	n.mu.Lock()
	e.Gelesen = len(n.gelesen)
	n.mu.Unlock()
	return e
}

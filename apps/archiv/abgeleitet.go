package main

import (
	"cmp"
	"maps"
	"slices"
	"strings"
)

// Derived relations (archiv#27 step 6; owner 2026-11-10: „wenn zwei
// Partner sind und ein Kind haben, sind wahrscheinlich beide Eltern …
// gleiche Eltern, wahrscheinlich Geschwister“). From what is known — the
// person's answers and the relations the phone books name (RELATED) —
// follow likely relations of other pairs. Each is a „Wie steht X zu Y?“
// with the kind chosen and the reason („weil Anna und Ben Partner sind
// und Carla Bens Kind ist“), asked before the plain suggestions — unless
// an answer just made it follow by definition (definitorisch; owner
// 2026-11-11): then it is written with that answer (fragen.go, folgern)
// and goes when it goes. A step-family is why the first two rules stay
// questions: Ben's partner need not be Carla's mother, a shared parent
// may make half-siblings.
//
// The rules (X is the kind of Y):
//
//	partner of a parent             → Eltern
//	a shared parent                 → Geschwister
//	parent of a parent              → Großeltern
//	sibling of a parent             → Onkel/Tante
//	child of an uncle or aunt       → Cousin/e
//	parent of a partner             → Schwiegereltern
//	sibling of a partner,
//	partner of a sibling            → Schwager/Schwägerin
//
// Only what is known derives: a derived relation becomes a source once
// the person answered it (or it followed from an answer), never before.
// A pair with a relation in a phone book is never asked, one with an
// answer only when a rule that holds by definition says otherwise
// (Konflikt), once; one put back (Später) is asked last.

// The relations the rules read, each as „X is <rel> of Y“.
const (
	relEltern      = "eltern"      // X is a parent of Y
	relPartner     = "partner"     // symmetric
	relGeschwister = "geschwister" // symmetric
	relOnkel       = "onkel"       // X is an uncle or aunt of Y
)

// relWie are the phone books' words (beziehungsWort, and as people write
// them) as the relations the rules read; X's card names Y as the word,
// so Y is that of X.
var relWie = map[string]string{
	"partnerin": "partner", "partner": "partner", "ehepartnerin": "partner", "ehepartner": "partner", "ehemann": "partner", "ehefrau": "partner",
	"lebenspartnerin": "partner", "lebenspartner": "partner", "spouse": "partner", "husband": "partner", "wife": "partner",
	"kind": "kind", "sohn": "kind", "tochter": "kind", "child": "kind", "son": "kind", "daughter": "kind",
	"elternteil": "eltern", "eltern": "eltern", "mutter": "eltern", "vater": "eltern", "mother": "eltern", "father": "eltern", "parent": "eltern",
	"geschwister": "geschwister", "bruder": "geschwister", "schwester": "geschwister", "sibling": "geschwister", "brother": "geschwister", "sister": "geschwister",
	"onkeltante": "onkel", "onkel": "onkel", "tante": "onkel", "uncle": "onkel", "aunt": "onkel",
	"neffenichte": "neffe", "neffe": "neffe", "nichte": "neffe", "nephew": "neffe", "niece": "neffe",
}

// bekannt is one known relation: X is Rel of Y.
type bekannt struct {
	X, Y, Rel string
}

// Abgeleitet is a derived question: X is likely Art of Y, Weil the
// reason without its „weil“.
type Abgeleitet struct {
	X, Y, Art, Weil string
	regel           int  // the rule's place, for the order
	Konflikt        bool // an answer or another rule says otherwise
}

// definitorisch: the rule holds by definition (owner 2026-11-11), so an
// answer takes it with it (folgern): Großeltern, Onkel/Tante, Cousin/e,
// Schwiegereltern, Schwager/Schwägerin. A partner of a parent (a
// step-family) and a shared parent (half-siblings) stay questions.
func definitorisch(regel int) bool { return regel >= 3 }

// bekannte are the relations known among the shown nodes: an answer is
// the pair's relation in place of the phone books'; n.mu is held.
func (n *Netz) bekannte() (out []bekannt) {
	sichtbar := func(id string) bool { _, da := n.knoten[id]; return da && n.verborgen[id] == "" }
	dazu := func(x, y, art string) {
		switch art {
		case "kind":
			x, y, art = y, x, relEltern
		case "neffe":
			x, y, art = y, x, relOnkel
		}
		if x == y || !sichtbar(x) || !sichtbar(y) || !slices.Contains([]string{relEltern, relPartner, relGeschwister, relOnkel}, art) {
			return
		}
		if art == relPartner || art == relGeschwister {
			x, y = min(x, y), max(x, y)
		}
		if b := (bekannt{x, y, art}); !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	for key, a := range n.antworten {
		x, y, _ := strings.Cut(key, "|")
		if a.X == y {
			x, y = y, x
		}
		dazu(x, y, a.Art)
	}
	for _, f := range n.st.Fest {
		a, b := n.von[f.A], n.von[f.B]
		if f.Art != bandBeziehung || a == "" || b == "" {
			continue
		}
		if g, gesagt := n.antworten[paarKey(a, b)]; gesagt && g.Art != artSpaeter {
			continue
		}
		if art := relWie[klar(f.Wie)]; art != "" {
			dazu(b, a, art) // a's card names b as Wie: b is that of a
		}
	}
	slices.SortFunc(out, func(p, q bekannt) int {
		return cmp.Or(strings.Compare(p.Rel, q.Rel), strings.Compare(p.X, q.X), strings.Compare(p.Y, q.Y))
	})
	return out
}

// abgeleitete are the derived questions still open: the pair is shown,
// has no answer but Später and no relation the phone books name; the
// first rule that derives a pair gives its kind. n.mu is held.
func (n *Netz) abgeleitete() []Abgeleitet {
	bs := n.bekannte()
	eltern, kinder, partner, geschwister, onkel := map[string][]string{}, map[string][]string{}, map[string][]string{}, map[string][]string{}, map[string][]string{}
	bekanntePaare := map[string]bool{}
	for _, b := range bs {
		bekanntePaare[paarKey(b.X, b.Y)] = true
		switch b.Rel {
		case relEltern:
			eltern[b.Y], kinder[b.X] = append(eltern[b.Y], b.X), append(kinder[b.X], b.Y)
		case relPartner:
			partner[b.X], partner[b.Y] = append(partner[b.X], b.Y), append(partner[b.Y], b.X)
		case relGeschwister:
			geschwister[b.X], geschwister[b.Y] = append(geschwister[b.X], b.Y), append(geschwister[b.Y], b.X)
		case relOnkel:
			onkel[b.Y] = append(onkel[b.Y], b.X)
		}
	}
	for _, m := range []map[string][]string{eltern, kinder, partner, geschwister, onkel} {
		for k := range m {
			slices.Sort(m[k])
		}
	}
	gefunden := map[string]Abgeleitet{}
	// von is art (x of y) as seen from z, one of the two
	von := func(z, x, art string) string {
		if z == x {
			return art
		}
		return cmp.Or(gegen[art], art)
	}
	vorschlagen := func(regel int, x, y, art, weil string) {
		key := paarKey(x, y)
		a, da := n.antworten[key]
		beantwortet := da && a.Art != artSpaeter
		if x == y || !beantwortet && (bekanntePaare[key] || n.imBuchBeziehung(x, y)) {
			return
		}
		konflikt := false
		if beantwortet { // a kind answered otherwise, by a rule that holds by definition: asked once more
			if gesehen := von(a.X, x, art); !definitorisch(regel) || !slices.Contains(arten, a.Art) || a.Art == gesehen || a.Trotz == gesehen {
				return
			}
			konflikt = true
		}
		if alt, da := gefunden[key]; da {
			anders := alt.Art != von(alt.X, x, art)
			if alt.regel <= regel {
				alt.Konflikt = alt.Konflikt || anders
				gefunden[key] = alt
				return
			}
			konflikt = konflikt || anders || alt.Konflikt
		}
		gefunden[key] = Abgeleitet{X: x, Y: y, Art: art, Weil: weil, regel: regel, Konflikt: konflikt}
	}
	s := n.saetze()
	for _, b := range slices.Sorted(maps.Keys(partner)) {
		for _, a := range partner[b] {
			for _, c := range kinder[b] { // 1: partner of a parent
				vorschlagen(1, a, c, "eltern", s.paar(a, b, "Partner")+" und "+s.kind(c, b))
			}
			for _, p := range eltern[b] { // 6: parent of a partner
				vorschlagen(6, p, a, "schwiegereltern", s.kind(b, p)+" und "+s.paar(b, a, "Partner"))
			}
			for _, g := range geschwister[b] { // 7: sibling of a partner, partner of a sibling
				vorschlagen(7, g, a, "schwager", s.paar(g, b, "Geschwister")+" und "+s.paar(b, a, "Partner"))
			}
		}
	}
	for _, p := range slices.Sorted(maps.Keys(kinder)) {
		ks := kinder[p]
		for i, a := range ks { // 2: a shared parent
			for _, b := range ks[i+1:] {
				vorschlagen(2, a, b, "geschwister", s.kinder(a, b, p))
			}
		}
		for _, g := range eltern[p] { // 3: parent of a parent
			for _, c := range ks {
				vorschlagen(3, g, c, "grosseltern", s.kind(p, g)+" und "+s.kind(c, p))
			}
		}
		for _, o := range geschwister[p] { // 4: sibling of a parent
			for _, c := range ks {
				vorschlagen(4, o, c, "onkel", s.paar(o, p, "Geschwister")+" und "+s.kind(c, p))
			}
		}
	}
	for _, x := range slices.Sorted(maps.Keys(onkel)) {
		for _, o := range onkel[x] { // 5: child of an uncle or aunt
			for _, c := range kinder[o] {
				vorschlagen(5, c, x, "cousin", s.onkel(o, x)+" und "+s.kind(c, o))
			}
		}
	}
	out := slices.Collect(maps.Values(gefunden))
	slices.SortFunc(out, func(a, b Abgeleitet) int {
		return cmp.Or(cmp.Compare(a.regel, b.regel), strings.Compare(paarKey(a.X, a.Y), paarKey(b.X, b.Y)))
	})
	return out
}

// imBuchBeziehung: a phone book names a relation between x and y; n.mu is held.
func (n *Netz) imBuchBeziehung(x, y string) bool {
	return slices.ContainsFunc(n.st.Fest, func(f festBand) bool {
		a, b := n.von[f.A], n.von[f.B]
		return f.Art == bandBeziehung && (a == x && b == y || a == y && b == x)
	})
}

// saetze writes a reason's parts, „du“ for the person: „Anna und Ben
// Partner sind“, „du und Ben Partner seid“, „Carla Bens Kind ist“,
// „Carla dein Kind ist“, „du Bens Kind bist“.
type saetze struct {
	name func(string) string
	ich  string
}

func (n *Netz) saetze() saetze {
	return saetze{name: func(id string) string { return cmp.Or(n.knoten[id].Name, id) }, ich: n.ich}
}

// wer is id as a subject.
func (s saetze) wer(id string) string {
	if id == s.ich {
		return "du"
	}
	return s.name(id)
}

// des is id's genitive („Bens“, „Klaus’“), du for the person („dein“).
func (s saetze) des(id, du string) string {
	if id == s.ich {
		return du
	}
	name := s.name(id)
	if strings.HasSuffix(name, "s") || strings.HasSuffix(name, "ß") || strings.HasSuffix(name, "x") || strings.HasSuffix(name, "z") {
		return name + "’"
	}
	return name + "s"
}

// ist is the verb for one subject; sind for two.
func (s saetze) ist(id string) string {
	return map[bool]string{true: "bist", false: "ist"}[id == s.ich]
}
func (s saetze) sind(a, b string) string {
	return map[bool]string{true: "seid", false: "sind"}[a == s.ich || b == s.ich]
}

// paar: „Anna und Ben Partner sind“; the person first.
func (s saetze) paar(a, b, was string) string {
	if b == s.ich {
		a, b = b, a
	}
	return s.wer(a) + " und " + s.wer(b) + " " + was + " " + s.sind(a, b)
}

// kind: „Carla Bens Kind ist“.
func (s saetze) kind(c, p string) string {
	return s.wer(c) + " " + s.des(p, "dein") + " Kind " + s.ist(c)
}

// kinder: „Anna und Ben beide Petras Kinder sind“.
func (s saetze) kinder(a, b, p string) string {
	if b == s.ich {
		a, b = b, a
	}
	return s.wer(a) + " und " + s.wer(b) + " beide " + s.des(p, "deine") + " Kinder " + s.sind(a, b)
}

// onkel: „Ben Annas Onkel oder Tante ist“, „Ben dein Onkel oder deine Tante ist“.
func (s saetze) onkel(o, x string) string {
	wer := s.des(x, "") + " Onkel oder Tante"
	if x == s.ich {
		wer = "dein Onkel oder deine Tante"
	}
	return s.wer(o) + " " + wer + " " + s.ist(o)
}

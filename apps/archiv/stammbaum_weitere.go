package main

import (
	"cmp"
	"slices"
)

// The relatives a step away in the Stammbaum (owner 2026-11-11: „Wenn
// Hedwig meine Tante ist, wird das nicht im Stammbaum angezeigt.“): every
// family kind the archive knows is in the tree. Großeltern, Onkel/Tante,
// Cousin/e, Neffe/Nichte, Enkel/in, Schwiegereltern, Schwiegerkind and
// Schwager/Schwägerin each stand for a path over someone in between (an
// aunt is a sibling of a parent). Where the path is drawn, the relative
// stands at its place by it. Where it is not, the relative stands in the
// right generation on a side row („Tanten und Onkel“), and edit mode
// (stammbaum_frei.go) offers her first for each empty place a line to
// one in between would give her: an aunt for the free child of the
// grandparents. „Sonstige verwandt“ has no path: it stays on its side
// row until the edge page says how. Nothing is dropped.

// baumStufe is how many generations X stands above Y for „X is Art of Y“.
var baumStufe = map[string]int{"eltern": 1, "kind": -1, "grosseltern": 2, "enkel": -2, "onkel": 1, "neffe": -1, "schwiegereltern": 1, "schwiegerkind": -1}

// baumWeitere are the kinds a step away, drawn as a place, not a line.
var baumWeitere = []string{"grosseltern", "enkel", "onkel", "neffe", "cousin", "schwiegereltern", "schwiegerkind", "schwager", "verwandt"}

// umweg is a path for „X is Art of Y“: M is Ueber of Y (as the drawn
// lines say), and X is Antwort of M, the answer that places X.
type umweg struct{ Ueber, Antwort string }

var baumUmwege = map[string][]umweg{
	"grosseltern":     {{"eltern", "eltern"}},
	"enkel":           {{"kind", "kind"}},
	"onkel":           {{"eltern", "geschwister"}},
	"neffe":           {{"geschwister", "kind"}},
	"cousin":          {{"onkel", "kind"}},
	"schwiegereltern": {{"partner", "eltern"}},
	"schwiegerkind":   {{"kind", "partner"}},
	"schwager":        {{"partner", "geschwister"}, {"geschwister", "partner"}},
}

// baumSeitenWort is a side row's label by kind.
var baumSeitenWort = map[string]string{"grosseltern": "Großeltern", "enkel": "Enkel", "onkel": "Tanten und Onkel", "neffe": "Nichten und Neffen",
	"cousin": "Cousins und Cousinen", "schwiegereltern": "Schwiegereltern", "schwiegerkind": "Schwiegerkinder", "schwager": "Schwager und Schwägerinnen",
	"verwandt": "Weitere Verwandte"}

// baumWahl is a line that would place X: X is Art of M.
type baumWahl struct{ M, Art string }

// BaumFrage is a relative a step away the drawn lines do not place: X is
// Art of Y; Wahl the lines to the ones in between that would.
type BaumFrage struct {
	X, Y, Art string
	Wahl      []baumWahl
}

// baumNahe answers who is rel of id among the drawn lines.
// onkel: an aunt or uncle a step away, placed or not, is one in between
// for a cousin.
type baumNahe struct{ eltern, kinder, partner, geschw, onkel map[string][]string }

func neueBaumNahe(linien, weitere []baumLinie) baumNahe {
	b := baumNahe{map[string][]string{}, map[string][]string{}, map[string][]string{}, map[string][]string{}, map[string][]string{}}
	for _, l := range weitere {
		if l.Art == "onkel" {
			b.onkel[l.B] = append(b.onkel[l.B], l.A)
		}
	}
	for _, l := range linien {
		switch l.Art {
		case "eltern":
			b.eltern[l.B], b.kinder[l.A] = append(b.eltern[l.B], l.A), append(b.kinder[l.A], l.B)
		case "partner":
			b.partner[l.A], b.partner[l.B] = append(b.partner[l.A], l.B), append(b.partner[l.B], l.A)
		case "geschwister":
			b.geschw[l.A], b.geschw[l.B] = append(b.geschw[l.A], l.B), append(b.geschw[l.B], l.A)
		}
	}
	return b
}

// von are the ones who are rel of id: id's parents for eltern, and so on.
func (b baumNahe) von(id, rel string) (out []string) {
	dazu := func(ids ...string) {
		for _, s := range ids {
			if s != id && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	switch rel {
	case "eltern":
		dazu(b.eltern[id]...)
	case "kind":
		dazu(b.kinder[id]...)
	case "partner":
		dazu(b.partner[id]...)
	case "geschwister":
		dazu(b.geschw[id]...)
		for _, e := range b.eltern[id] {
			dazu(b.kinder[e]...)
		}
	case "onkel":
		dazu(b.onkel[id]...)
		for _, e := range b.eltern[id] {
			dazu(b.von(e, "geschwister")...)
		}
	}
	return out
}

// baumFragen are the lines a step away the drawn lines do not place,
// each asked from the end that stands in the tree (the person's first).
func baumFragen(weitere, linien []baumLinie, ich string) (out []BaumFrage) {
	nahe := neueBaumNahe(linien, weitere)
	steht := map[string]bool{}
	for _, l := range linien {
		steht[l.A], steht[l.B] = true, true
	}
	for _, l := range weitere {
		x, y, art := l.A, l.B, l.Art
		if x == ich || !steht[y] && steht[x] {
			x, y, art = y, x, cmp.Or(gegen[art], art)
		}
		f := BaumFrage{X: x, Y: y, Art: art}
		if f.platz(nahe) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// platz: the drawn lines place X; else the lines that would.
func (f *BaumFrage) platz(nahe baumNahe) bool {
	for _, u := range baumUmwege[f.Art] {
		for _, m := range nahe.von(f.Y, u.Ueber) {
			if m == f.X {
				continue
			}
			if slices.Contains(nahe.von(m, u.Antwort), f.X) {
				return true
			}
			f.Wahl = append(f.Wahl, baumWahl{M: m, Art: u.Antwort})
		}
	}
	return false
}

// seitenWort is the label of a side row with these kinds.
func seitenWort(arten []string) string {
	slices.Sort(arten)
	if arten = slices.Compact(arten); len(arten) == 1 {
		return baumSeitenWort[arten[0]]
	}
	return "Verwandte"
}

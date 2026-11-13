package main

import (
	"maps"
	"slices"
)

// Who is what, derived (owner 2026-11-12: „Was mache ich mit Partnern von
// Geschwistern? … Großonkel, Partner von Cousins … Wir müssen die nicht
// alle explizit benennen, können auch implizit ins Netz aufnehmen.“): the
// person says only the plain lines, Mutter/Vater and Kind, Partner/in,
// Geschwister. Everything else is read from them on the way: the shortest
// way from one to the other over those lines (E up to a parent, K down to
// a child, P to the partner, G to a sibling) has a name where German has
// one („EG“ Onkel/Tante, „EEG“ Großonkel/Großtante, „PG“ Schwager/
// Schwägerin) and is described where it has none: „Partner/in von Jonas
// (Cousin/e)“; a parent's partner is just that, never a step-parent by a
// guess. Nothing of it is stored: antworten.json holds what the
// person said, as before, and a kind she said without its way („Hedwig
// ist meine Tante“) stays her answer until a line places it
// (stammbaum_weitere.go). The names stand in the Stammbaum's boxes, in
// „Wer ist wer“, on a person's page, as a reason on „Steht nahe“
// (verwandtDazu: a relative by the lines is a tie), and a pair they name
// is not asked (fragen.go, offen).

const verwandtWeit = 6 // steps a way may have

// verwandtWort names a way, read from the one it starts at.
var verwandtWort = map[string]string{
	"E": "Mutter/Vater", "K": "Kind", "P": "Partner/in", "G": "Geschwister", "GG": "Geschwister",
	"EE": "Großeltern", "EEE": "Urgroßeltern", "EEEE": "Ururgroßeltern", "KK": "Enkel/in", "KKK": "Urenkel/in",
	"EG": "Onkel/Tante", "EGP": "Onkel/Tante (angeheiratet)", "EEG": "Großonkel/Großtante", "EEGP": "Großonkel/Großtante (angeheiratet)",
	"GK": "Neffe/Nichte", "PGK": "Neffe/Nichte (angeheiratet)", "GKK": "Großneffe/Großnichte",
	"EGK": "Cousin/e", "EEGK": "Onkel/Tante 2. Grades", "EGKK": "Neffe/Nichte 2. Grades", "EEGKK": "Cousin/e 2. Grades",
	"PE": "Schwiegereltern", "KP": "Schwiegerkind", "PG": "Schwager/Schwägerin", "GP": "Schwager/Schwägerin",
	"PGP": "Schwippschwager/-schwägerin", "GPG": "Schwippschwager/-schwägerin",
}

// sippe are the plain lines known, as steps from each one.
type sippe struct{ eltern, kinder, partner, geschw map[string][]string }

// sippe reads the plain lines the answers and the phone books say
// (bekannte); a sibling without parents of its own has its sibling's.
// n.mu is held.
func (n *Netz) sippe() sippe {
	s := sippe{map[string][]string{}, map[string][]string{}, map[string][]string{}, map[string][]string{}}
	eins := func(id string) string {
		if n.selbst[id] {
			return n.ich
		}
		return id
	}
	for _, b := range n.bekannte() {
		x, y := eins(b.X), eins(b.Y)
		switch {
		case x == y:
		case b.Rel == relEltern:
			s.eltern[y], s.kinder[x] = append(s.eltern[y], x), append(s.kinder[x], y)
		case b.Rel == relPartner:
			s.partner[x], s.partner[y] = append(s.partner[x], y), append(s.partner[y], x)
		case b.Rel == relGeschwister:
			s.geschw[x], s.geschw[y] = append(s.geschw[x], y), append(s.geschw[y], x)
		}
	}
	for neu := true; neu; {
		neu = false
		for _, a := range slices.Sorted(maps.Keys(s.geschw)) {
			for _, b := range s.geschw[a] {
				if len(s.eltern[a]) == 0 && len(s.eltern[b]) > 0 {
					s.eltern[a], neu = slices.Clone(s.eltern[b]), true
					for _, e := range s.eltern[a] {
						s.kinder[e] = append(s.kinder[e], a)
					}
				}
			}
		}
	}
	return s
}

// weg is a way over the lines: its steps and who stands after each.
type weg struct {
	Schritte string
	Ueber    []string
}

// schritte are the ones a step from id, by step, each in a fixed order.
func (s sippe) schritte(id string) [4][]string {
	g := slices.Clone(s.geschw[id])
	for _, e := range s.eltern[id] {
		for _, k := range s.kinder[e] {
			if k != id && !slices.Contains(g, k) {
				g = append(g, k)
			}
		}
	}
	out := [4][]string{slices.Clone(s.eltern[id]), slices.Clone(s.kinder[id]), slices.Clone(s.partner[id]), g}
	for i := range out {
		slices.Sort(out[i])
	}
	return out
}

// wege are the shortest ways from von to everyone within verwandtWeit
// steps (von itself left out), breadth first: the fewest steps name it.
func (s sippe) wege(von string) map[string]weg {
	out := map[string]weg{von: {}}
	for schlange := []string{von}; len(schlange) > 0; schlange = schlange[1:] {
		w := out[schlange[0]]
		if len(w.Schritte) == verwandtWeit {
			continue
		}
		for i, ids := range s.schritte(schlange[0]) {
			for _, id := range ids {
				if _, da := out[id]; !da {
					out[id] = weg{w.Schritte + "EKPG"[i:i+1], append(slices.Clone(w.Ueber), id)}
					schlange = append(schlange, id)
				}
			}
		}
	}
	delete(out, von)
	return out
}

// satz names the way: its word, or the last named stretch from the one
// who stands before it with what that one is: „Partner/in von Jonas
// (Cousin/e)“; "" a way nothing names.
func (w weg) satz(name func(string) string) string {
	if wort := verwandtWort[w.Schritte]; wort != "" {
		return wort
	}
	for i := len(w.Schritte) - 1; i > 0; i-- {
		vor, nach := verwandtWort[w.Schritte[:i]], verwandtWort[w.Schritte[i:]]
		if vor != "" && nach != "" {
			return nach + " von " + name(w.Ueber[i-1]) + " (" + vor + ")"
		}
	}
	return ""
}

// verwandte names everyone the lines tie to von, by id: what she is of
// von; direkt false leaves out the ones a single line names. n.mu is held.
func (n *Netz) verwandte(von string, direkt bool) map[string]string {
	out := map[string]string{}
	for id, w := range n.sippe().wege(von) {
		if s := w.satz(func(id string) string { return n.knoten[id].Name }); s != "" && (direkt || len(w.Schritte) > 1) {
			out[id] = s
		}
	}
	return out
}

// Verwandt is what x is of y by the lines known; "" nothing.
func (n *Netz) Verwandt(x, y string) string {
	if n == nil {
		return ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.verwandte(y, true)[x]
}

// verwandtDazu makes each relative of the person the lines name, and no
// phone book or answer does, a tie with that name as its reason: the net
// takes her in without a question. n.mu is held or n is new.
func (n *Netz) verwandtDazu(alle map[[2]string]*Kante, nicht map[string]bool) {
	if n.ich == "" || nicht[n.ich] {
		return
	}
	vs := n.verwandte(n.ich, false)
	for _, id := range slices.Sorted(maps.Keys(vs)) {
		p := [2]string{min(id, n.ich), max(id, n.ich)}
		k := alle[p]
		if _, da := n.knoten[id]; !da || nicht[id] || k != nil && slices.ContainsFunc(k.Fest, func(f festBand) bool { return f.Art == bandBeziehung }) {
			continue
		}
		if k == nil {
			k = &Kante{A: p[0], B: p[1]}
			alle[p] = k
		}
		k.Staerke, k.Rang = k.Staerke+festGewicht[bandName], k.Rang+festGewicht[bandName]
		k.Fest = append(k.Fest, festBand{A: n.ich, B: id, Art: bandBeziehung, Wie: vs[id], Abgeleitet: true})
	}
}

// benannt are the pairs the lines name over more than one step, by
// paarKey: nobody needs to say what they are. n.mu is held.
func (n *Netz) benannt() map[string]bool {
	out := map[string]bool{}
	s := n.sippe()
	for _, m := range []map[string][]string{s.eltern, s.partner, s.geschw} {
		for von := range m {
			for id, w := range s.wege(von) {
				if len(w.Schritte) > 1 && w.satz(func(string) string { return "" }) != "" {
					out[paarKey(von, id)] = true
				}
			}
		}
	}
	return out
}

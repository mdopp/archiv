package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Edit mode (owner 2026-11-13: „Warum nicht Show / edit Mode. Im Edit
// Mode zeigt das Modell die leeren Stellen an und man kann auf eine
// clicken und einen Namen (bestehend oder neu) hinzufügen. … immer ein
// Kind extra frei, ein Geschwister. Und nach oben Ahnen. Wenn man einen
// füllt wird ein neuer frei. Im View Modus dann nur die ausgefüllten.“):
// the same tree with its empty places (baumFrei) as dashed boxes in the
// same grid. Which places:
//
//   - upward: the person, her partner and each ancestor of the two have
//     two parents' places; the ones not filled are free. A parent named
//     there is a parent of every child of that family. Who married in
//     further away (an aunt's husband) gets none.
//   - one child more: every couple (two parents of a child, or two
//     partners; not two who parted) and every parent alone has one free
//     child, which is the free sibling of its children. The person has
//     one also without a partner or a child.
//   - the person without parents has a free sibling beside her; without
//     a partner a free partner.
//
// A place is not stored: it is a box and lines only the edit tree's
// layout knows, and filling it sets ordinary lines (stammbaum_bau.go),
// after which the tree has the next free child or the new one's parents
// free. Each place offers first the relatives without a place whom a
// line there would give one (Vor; stammbaum_weitere.go).

const freiPrefix = "~frei" // after every id: a free child stands last in its row

func istFrei(id string) bool { return strings.HasPrefix(id, freiPrefix) }

// freiWort is what the one named at a place is of Y.
var freiWort = map[string]string{"eltern": "Mutter oder Vater", "kind": "Kind", "geschwister": "Geschwister", "partner": "Partner/in"}

// baumFrei is an empty place: who is named there is Art of each of Y.
// Neben: the children already there, her siblings then. Vor: the
// relatives without a place this one would place.
type baumFrei struct {
	ID, Art string
	Y       []string
	Neben   []string
	Vor     []string
}

// freiBau collects the places and the lines that hold their boxes; platz
// adds one: eltern are who its box hangs from, kinder who hangs from it.
type freiBau struct {
	frei   []baumFrei
	linien []baumLinie
}

func (fb *freiBau) platz(f baumFrei, eltern, kinder []string) string {
	f.ID = fmt.Sprintf("%s%03d", freiPrefix, len(fb.frei))
	fb.frei = append(fb.frei, f)
	for _, e := range eltern {
		fb.linien = append(fb.linien, baumLinie{A: e, B: f.ID, Art: "eltern"})
	}
	for _, k := range kinder {
		fb.linien = append(fb.linien, baumLinie{A: f.ID, B: k, Art: "eltern"})
	}
	return f.ID
}

// baumFreie are the empty places of the laid-out tree and their lines.
func baumFreie(sb Stammbaum) ([]baumFrei, []baumLinie) {
	ich, fb := sb.ich, &freiBau{}
	if ich == "" {
		return nil, nil
	}
	fams, _ := baumFamilien(sb.Linien)
	nahe := neueBaumNahe(sb.Linien, nil)
	dazu := fb.ahnen(ich, fams, nahe)
	getrennt, paar := map[string]bool{}, map[string]bool{}
	for _, l := range sb.Linien {
		getrennt[paarKey(l.A, l.B)] = l.Art == "expartner"
	}
	hat := map[string]bool{} // a parent of a free child
	kind := func(eltern, mehr, neben []string) {
		fb.platz(baumFrei{Art: "kind", Y: eltern, Neben: neben}, append(slices.Clone(eltern), mehr...), nil)
		for _, e := range eltern {
			hat[e] = true
		}
	}
	for _, f := range fams {
		if len(f.Eltern) == 2 && !getrennt[paarKey(f.Eltern[0], f.Eltern[1])] {
			paar[paarKey(f.Eltern[0], f.Eltern[1])] = true
			kind(f.Eltern, dazu[f], f.Kinder)
		}
	}
	for _, l := range sb.Linien {
		if l.Art == "partner" && !paar[paarKey(l.A, l.B)] {
			kind([]string{l.A, l.B}, nil, nil)
		}
	}
	for _, f := range fams {
		if len(f.Eltern) == 1 && !hat[f.Eltern[0]] {
			kind(f.Eltern, dazu[f], f.Kinder)
		}
	}
	if !hat[ich] {
		kind([]string{ich}, nil, nil)
	}
	if len(nahe.partner[ich]) == 0 {
		id := fb.platz(baumFrei{Art: "partner", Y: []string{ich}}, nil, nil)
		fb.linien = append(fb.linien, baumLinie{A: ich, B: id, Art: "partner"})
	}
	return fb.frei, fb.linien
}

// ahnen adds the free parents of the person, her partners and their
// ancestors, a family once, and the person's free sibling; it answers
// the free parents by family.
func (fb *freiBau) ahnen(ich string, fams []*baumFamilie, nahe baumNahe) map[*baumFamilie][]string {
	von := map[string]*baumFamilie{}
	for _, f := range fams {
		for _, k := range f.Kinder {
			von[k] = f
		}
	}
	wer := append([]string{ich}, nahe.partner[ich]...)
	for i := 0; i < len(wer); i++ {
		for _, e := range nahe.eltern[wer[i]] {
			if !slices.Contains(wer, e) {
				wer = append(wer, e)
			}
		}
	}
	dazu, fertig := map[*baumFamilie][]string{}, map[*baumFamilie]bool{}
	for _, x := range wer {
		f := von[x]
		kinder, da := []string{x}, 0
		if f != nil && fertig[f] {
			continue
		}
		if f != nil && len(f.Eltern) > 0 { // her siblings by these parents; one who only hangs on the bar follows
			da = len(f.Eltern)
			kinder = slices.DeleteFunc(slices.Clone(f.Kinder), func(k string) bool { return f.Fuer[k][0].Art != "eltern" })
		}
		var neu []string
		for ; da < 2; da++ {
			neu = append(neu, fb.platz(baumFrei{Art: "eltern", Y: kinder}, nil, kinder))
		}
		if f != nil {
			dazu[f], fertig[f] = neu, true
		}
		if x == ich && len(neu) == 2 {
			fb.platz(baumFrei{Art: "geschwister", Y: []string{ich}}, neu, nil)
		}
	}
	return dazu
}

// zumBearbeiten is the tree laid out anew with its empty places, from
// what sb was laid out from. The strokes to a place stand for no
// line: they are quiet and open nothing.
func (sb Stammbaum) zumBearbeiten(linien []baumLinie, knoten map[string]Knoten, geb map[string]string, selbst Knoten) Stammbaum {
	frei, mehr := baumFreie(sb)
	if len(frei) == 0 {
		return Stammbaum{}
	}
	knoten = maps.Clone(knoten)
	knoten[sb.ich] = selbst
	plaetze := map[string]baumFrei{}
	for _, f := range frei {
		passt := func(w baumWahl) bool { // the line that would place her is set at this place
			return w.Art == f.Art && slices.Contains(f.Y, w.M) || w.Art == "geschwister" && f.Art == "kind" && slices.Contains(f.Neben, w.M)
		}
		for _, o := range sb.Fragen {
			if slices.ContainsFunc(o.Wahl, passt) && !slices.Contains(f.Vor, o.X) {
				f.Vor = append(f.Vor, o.X)
			}
		}
		knoten[f.ID], plaetze[f.ID] = Knoten{ID: f.ID}, f
	}
	eb := baumLegen(append(slices.Clone(linien), mehr...), knoten, geb, sb.ich)
	eb.Frei, eb.Wer = plaetze, sb.Wer
	for i, s := range eb.Striche {
		fuer := slices.DeleteFunc(slices.Clone(s.Fuer), func(l baumLinie) bool { return istFrei(l.A) || istFrei(l.B) })
		switch {
		case len(fuer) > 0 && len(fuer) < len(s.Fuer):
			s.Fuer, s.Fest = fuer, alleFest(fuer)
		case len(fuer) < len(s.Fuer) || istFrei(s.A) || istFrei(s.B):
			s.Fuer, s.A, s.B, s.Fest, s.Frei = nil, "", "", false, true
		}
		eb.Striche[i] = s
	}
	return eb
}

// text says a place: „Kind von Jutta und Rolf“, „Mutter oder Vater von dir“.
func (f baumFrei) text(n *Netz) string {
	var wer []string
	for _, y := range f.Y {
		k, _ := n.Person(y)
		if name := strings.Fields(n.Anzeige(k)); y == n.Ich() {
			wer = append([]string{"dir"}, wer...)
		} else if len(name) > 0 {
			wer = append(wer, name[0])
		}
	}
	if len(wer) > 1 {
		wer = []string{strings.Join(wer[:len(wer)-1], ", ") + " und " + wer[len(wer)-1]}
	}
	return freiWort[f.Art] + " von " + strings.Join(wer, "")
}

// svg is a place's box: dashed, a plus and what it is; the script opens
// the sheet from its data.
func (f baumFrei) svg(b *strings.Builder, n *Netz, p baumPerson) {
	text := f.text(n)
	fmt.Fprintf(b, `<a href="#dazu" class=frei role=button data-art="%s" data-y="%s" data-vor="%s" data-text="%s" aria-label="%s"><rect x="%.1f" y="%.1f" width="%.0f" height="%.0f" rx="10"/><text x="%.1f" y="%.1f" class=plus>+</text><text x="%.1f" y="%.1f" class=k>%s</text></a>`,
		esc(f.Art), esc(strings.Join(f.Y, "|")), esc(strings.Join(f.Vor, "|")), esc(text), esc(text+": frei, eintragen"), p.X, p.Y, baumBreite, baumHoehe,
		p.X+baumBreite/2, p.Y+27, p.X+baumBreite/2, p.Y+44, esc(kurzWort(f.Art)))
}

package main

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"
)

// breitNetz is Nora Falk's family (b/nora is the person), shaped like a
// big family seen from its youngest branch: her father's parents Agnes
// and Bruno Falk have eight children, seven of them with partners and
// children of their own, the eighth her father Hugo with Ines; Nora's
// partner Sami Kern, their child Lio, Sami's parents Olga and Paul and
// his siblings Rosa and Timo.
func breitNetz() *Netz {
	st := netzTabelle{Namen: map[string]string{}, Ich: "b/nora"}
	as := map[string]antwort{}
	am := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	wer := func(id, name string) { st.Kontakte = append(st.Kontakte, netzKontakt{Key: "b/" + id, Name: name}) }
	ist := func(x, art, y string) { as[paarKey("b/"+x, "b/"+y)] = antwort{X: "b/" + x, Art: art, Am: am} }
	for _, p := range [][2]string{{"agnes", "Agnes Falk"}, {"bruno", "Bruno Falk"}, {"hugo", "Hugo Falk"}, {"ines", "Ines Falk"}, {"nora", "Nora Falk"}, {"sami", "Sami Kern"},
		{"lio", "Lio Kern"}, {"olga", "Olga Kern"}, {"paul", "Paul Kern"}, {"rosa", "Rosa Kern"}, {"timo", "Timo Kern"}} {
		wer(p[0], p[1])
	}
	ist("agnes", "partner", "bruno")
	for i, name := range []string{"Carl", "Dora", "Egon", "Frida", "Gerd", "Hanne", "Ingo"} {
		k, p, e := fmt.Sprintf("k%d", i), fmt.Sprintf("p%d", i), fmt.Sprintf("e%d", i)
		wer(k, name+" Falk")
		ist(k, "kind", "agnes")
		ist(k, "kind", "bruno")
		if i < 6 {
			wer(p, []string{"Mona", "Jens", "Lara", "Olaf", "Tina", "Udo"}[i]+" Falk")
			ist(p, "partner", k)
			wer(e, []string{"Finn", "Jana", "Kai", "Lena", "Malte", "Nina"}[i]+" Falk")
			ist(e, "kind", k)
			ist(e, "kind", p)
		}
	}
	for _, l := range [][3]string{{"hugo", "kind", "agnes"}, {"hugo", "kind", "bruno"}, {"ines", "partner", "hugo"}, {"nora", "kind", "hugo"}, {"nora", "kind", "ines"},
		{"sami", "partner", "nora"}, {"lio", "kind", "nora"}, {"lio", "kind", "sami"}, {"sami", "kind", "olga"}, {"sami", "kind", "paul"}, {"olga", "partner", "paul"},
		{"rosa", "kind", "olga"}, {"rosa", "kind", "paul"}, {"timo", "kind", "olga"}, {"timo", "kind", "paul"}} {
		ist(l[0], l[1], l[2])
	}
	return neuesNetz(st, nil, as, time.UTC, func() time.Time { return am })
}

// baumLuecke is the widest stretch of the picture, from left to right,
// in which no box stands in any row.
func baumLuecke(sb Stammbaum) (weit float64) {
	ps := slices.SortedFunc(slices.Values(sb.Personen), func(a, b baumPerson) int { return cmp.Compare(a.X, b.X) })
	rechts := baumRand
	for _, p := range ps {
		weit, rechts = max(weit, p.X-rechts), max(rechts, p.X+baumBreite)
	}
	return max(weit, sb.Breite-baumRand-rechts)
}

// The gap (owner 2026-11-13, his tree on the phone: two clusters and half
// the width empty between them, so the whole tree fitted to the screen
// was tiny): in a big family seen from its youngest branch, to look at
// and to edit, no stretch wider than a box and its gaps is empty, every
// child of the grandparents stands in their row in one run, each
// cousin under its parents, Nora under hers beside Sami, and the picture
// is as clean as any (strichFehler), without a crossing. The rows keep
// what the sweeps found: Hugo stands at the end of his siblings where
// Nora and her in-laws are, not where his id would sort him.
func TestStammbaumOhneLuecke(t *testing.T) {
	sb, eb := breitNetz().Stammbaeume()
	for name, b := range map[string]Stammbaum{"to look at": sb, "to edit": eb} {
		if l := baumLuecke(b); l > baumSpalte+baumLuft+0.5 || len(strichFehler(b)) > 0 {
			t.Errorf("%s: %v of empty width in %v, %v", name, l, b.Breite, strichFehler(b))
		}
		p, um := baumPos(b), 0.5 // to edit, a child shares its parents' middle with its free sibling
		if name == "to edit" {
			um = baumSpalte
		}
		for i := range 6 {
			k, pa, e := p[fmt.Sprintf("b/k%d", i)], p[fmt.Sprintf("b/p%d", i)], p[fmt.Sprintf("b/e%d", i)]
			if math.Abs(k.X-pa.X) != baumSpalte || e.X < min(k.X, pa.X)-um || e.X > max(k.X, pa.X)+um {
				t.Errorf("%s: %s %v and %s %v, their child %v", name, k.Name, k.X, pa.Name, pa.X, e.X)
			}
		}
		if n, h, i := p["b/nora"], p["b/hugo"], p["b/ines"]; math.Abs(n.X-p["b/sami"].X) != baumSpalte || n.X < min(h.X, i.X)-um || n.X > max(h.X, i.X)+um {
			t.Errorf("%s: Nora %v, Sami %v, Hugo %v, Ines %v", name, n.X, p["b/sami"].X, h.X, i.X)
		}
	}
	if kreuzungen(sb) != 0 || len(sb.Personen) != 30 || sb.Breite > 19*(baumSpalte+baumLuft) {
		t.Fatalf("to look at: %d people, %d crossings, %v wide", len(sb.Personen), kreuzungen(sb), sb.Breite)
	}
	// the check sees a gap: Hugo's branch moved away
	weg := sb
	weg.Personen = slices.Clone(sb.Personen)
	for i, p := range weg.Personen {
		if p.X > baumPos(sb)["b/agnes"].X {
			weg.Personen[i].X += 3 * baumSpalte
		}
	}
	weg.Breite += 3 * baumSpalte
	if baumLuecke(weg) < 2*baumSpalte {
		t.Fatalf("the check is blind: %v", baumLuecke(weg))
	}
}

package main

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"strings"
)

// Where everyone stands (owner 2026-11-12: „Die Darstellung des Stammbaums
// ist nicht ganz perfekt, das kannst du besser.“). The tree is drawn the
// way a family tree is: the children of one set of parents are a family
// (baumFamilie); partners stand side by side, one line between them; one
// line goes down from the middle of that line to a bar, and each child
// hangs from the bar. So a row is ordered to keep families together
// (baumOrdnung: the children of one family side by side, two families a
// couple ties end to end, in sweeps down and up each such chain where
// its parents or its children stand on average), and the columns
// are pushed right until parents stand over their children and children
// under their parents. Nothing here draws; stammbaum_striche.go does.

// baumFamilie are the children of one set of parents. A sibling without
// parents of its own hangs on its sibling's bar; siblings who all have
// none are a family without parents, a bar alone. Fuer: the lines a
// child's drop stands for, by child.
type baumFamilie struct {
	Eltern, Kinder []string
	Fuer           map[string][]baumLinie
}

// baumFamilien sorts the drawn lines into families; quer are the sibling
// lines between two families (half-siblings), drawn as a bracket.
func baumFamilien(linien []baumLinie) (fams []*baumFamilie, quer []baumLinie) {
	eltern, fuer := map[string][]string{}, map[string][]baumLinie{}
	var geschw []baumLinie
	for _, l := range linien {
		switch l.Art {
		case "eltern":
			eltern[l.B], fuer[l.B] = append(eltern[l.B], l.A), append(fuer[l.B], l)
		case "geschwister":
			geschw = append(geschw, l)
		}
	}
	nach, von := map[string]*baumFamilie{}, map[string]*baumFamilie{}
	dazu := func(key string, es []string, kind string, ls []baumLinie) {
		f := nach[key]
		if f == nil {
			f = &baumFamilie{Eltern: es, Fuer: map[string][]baumLinie{}}
			nach[key] = f
			fams = append(fams, f)
		}
		f.Kinder, f.Fuer[kind], von[kind] = append(f.Kinder, kind), ls, f
	}
	for _, k := range slices.Sorted(maps.Keys(eltern)) {
		es := slices.Sorted(slices.Values(eltern[k]))
		dazu(strings.Join(es, "|"), es, k, fuer[k])
	}
	haengen := func() { // onto a sibling's bar, as far as it goes
		for neu := true; neu; {
			neu = false
			for _, l := range geschw {
				a, b := l.A, l.B
				if von[a] == nil {
					a, b = b, a
				}
				if f := von[a]; f != nil && von[b] == nil {
					f.Kinder, f.Fuer[b], von[b] = append(f.Kinder, b), []baumLinie{l}, f
					neu = true
				}
			}
		}
	}
	haengen()
	for _, l := range geschw {
		if von[l.A] == nil && von[l.B] == nil {
			dazu("~"+l.A, nil, l.A, nil)
			haengen()
		}
	}
	for _, l := range geschw {
		if von[l.A] != von[l.B] {
			quer = append(quer, l)
		}
	}
	return fams, quer
}

// anker is where the family's line down starts, as a column: the middle
// of its parents. mitte is the middle of its children.
func (f *baumFamilie) anker(x map[string]float64) float64 { return baumMitte(f.Eltern, x) }
func (f *baumFamilie) mitte(x map[string]float64) float64 { return baumMitte(f.Kinder, x) }

func baumMitte(ids []string, x map[string]float64) float64 {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, id := range ids {
		lo, hi = min(lo, x[id]), max(hi, x[id])
	}
	return (lo + hi) / 2
}

// baumOrdnung orders the rows of one component and sets its columns.
type baumOrdnung struct {
	reihen map[int][]string
	hoch   int
	fams   []*baumFamilie // this component's
	// eltern and kinder as the families say them; paar the partners, mit
	// the other parents of one's children; familie the family one is a
	// child of
	eltern, kinder, paar, mit map[string][]string
	familie                   map[string]*baumFamilie
	pos                       map[string]float64 // the place in the row
}

// neueBaumOrdnung orders the rows: first by erst (birthday, name), then
// by sweeps down and up.
func neueBaumOrdnung(reihen map[int][]string, hoch int, linien []baumLinie, fams []*baumFamilie, erst func(a, b string) int) *baumOrdnung {
	o := &baumOrdnung{reihen: reihen, hoch: hoch, eltern: map[string][]string{}, kinder: map[string][]string{}, paar: map[string][]string{},
		mit: map[string][]string{}, familie: map[string]*baumFamilie{}, pos: map[string]float64{}}
	for g, ids := range reihen {
		slices.SortFunc(ids, erst)
		for i, id := range ids {
			o.pos[id] = float64(i)
		}
		reihen[g] = ids
	}
	for _, l := range linien {
		if _, hier := o.pos[l.A]; hier && (l.Art == "partner" || l.Art == "expartner") {
			o.paar[l.A], o.paar[l.B] = append(o.paar[l.A], l.B), append(o.paar[l.B], l.A)
		}
	}
	for _, f := range fams {
		if _, hier := o.pos[f.Kinder[0]]; !hier {
			continue
		}
		o.fams = append(o.fams, f)
		for _, k := range f.Kinder {
			o.eltern[k], o.familie[k] = f.Eltern, f
		}
		for i, e := range f.Eltern {
			o.kinder[e] = append(o.kinder[e], f.Kinder...)
			for _, m := range f.Eltern[i+1:] {
				o.mit[e], o.mit[m] = append(o.mit[e], m), append(o.mit[m], e)
			}
		}
	}
	for range 3 {
		for g := 0; g <= hoch; g++ {
			o.ordnen(g, o.eltern)
		}
		for g := hoch - 1; g >= 0; g-- {
			o.ordnen(g, o.kinder)
		}
	}
	for g := 0; g <= hoch; g++ {
		o.ordnen(g, o.eltern)
	}
	return o
}

// bloecke are the row's couples and those alone, in the row's order: a
// block is everyone tied by partner lines or shared children.
func (o *baumOrdnung) bloecke(g int) (out [][]string) {
	in := map[string]bool{}
	for _, id := range o.reihen[g] {
		if in[id] {
			continue
		}
		b := []string{id}
		in[id] = true
		for i := 0; i < len(b); i++ {
			for _, p := range append(slices.Clone(o.paar[b[i]]), o.mit[b[i]]...) {
				if !in[p] && slices.Contains(o.reihen[g], p) {
					in[p] = true
					b = append(b, p)
				}
			}
		}
		out = append(out, b)
	}
	return out
}

// baumReihe is one row as its chains are made of it: who stands in it,
// its families, who married in (anhang, by the partner who has a family
// in the row) and who has no family in the row at all (lose).
type baumReihe struct {
	o      *baumOrdnung
	wer    []string
	fams   []*baumFamilie
	anhang map[string][]string
	lose   map[string]bool
}

func (o *baumOrdnung) reihe(g int) *baumReihe {
	r := &baumReihe{o: o, wer: o.reihen[g], anhang: map[string][]string{}, lose: map[string]bool{}}
	for _, id := range r.wer {
		if f := o.familie[id]; f != nil {
			if !slices.Contains(r.fams, f) {
				r.fams = append(r.fams, f)
			}
			continue
		}
		ps := r.partner(id)
		if i := slices.IndexFunc(ps, func(p string) bool { return o.familie[p] != nil }); i >= 0 {
			r.anhang[ps[i]] = append(r.anhang[ps[i]], id)
		} else {
			r.lose[id] = true
		}
	}
	return r
}

// partner are id's partners and the other parents of her children, in the row.
func (r *baumReihe) partner(id string) (ps []string) {
	for _, p := range append(slices.Clone(r.o.paar[id]), r.o.mit[id]...) {
		if slices.Contains(r.wer, p) && !slices.Contains(ps, p) {
			ps = append(ps, p)
		}
	}
	return ps
}

// verbunden is who of family a is the partner of one of b; "" nobody.
func (r *baumReihe) verbunden(a, b *baumFamilie) string {
	for _, x := range a.Kinder {
		if slices.ContainsFunc(r.partner(x), func(y string) bool { return r.o.familie[y] == b }) {
			return x
		}
	}
	return ""
}

// kette are the families a couple ties to f, one after the other, from
// one end: grown at the back, then at the front.
func (r *baumReihe) kette(f *baumFamilie, fertig map[*baumFamilie]bool) []*baumFamilie {
	kette := []*baumFamilie{f}
	fertig[f] = true
	naechste := func(ende *baumFamilie) *baumFamilie {
		for _, h := range r.fams {
			if !fertig[h] && r.verbunden(ende, h) != "" {
				fertig[h] = true
				return h
			}
		}
		return nil
	}
	for h := naechste(f); h != nil; h = naechste(h) {
		kette = append(kette, h)
	}
	for h := naechste(f); h != nil; h = naechste(h) {
		kette = append([]*baumFamilie{h}, kette...)
	}
	return kette
}

// glied are the children of h in order, each with who married in: vor
// and nach, who tie h to the family before and after it, at its ends
// with their partners outside; between them by ort; the first one's
// partner outside too, so the siblings stay side by side; of several who
// married in, she in the middle.
func (r *baumReihe) glied(h *baumFamilie, vor, nach string, ort func(string) float64) (ids []string) {
	wer := slices.DeleteFunc(slices.Clone(h.Kinder), func(id string) bool { return !slices.Contains(r.wer, id) })
	rang := func(id string) int {
		switch id {
		case nach:
			return 2
		case vor:
			return 0
		}
		return 1
	}
	slices.SortStableFunc(wer, func(a, b string) int { return cmp.Or(cmp.Compare(rang(a), rang(b)), cmp.Compare(ort(a), ort(b))) })
	for j, id := range wer {
		an := r.anhang[id]
		switch m := len(an) / 2; {
		case id == nach:
			ids = append(append(ids, an...), id)
		case id == vor:
			ids = append(append(ids, id), an...)
		case len(an) > 1:
			ids = append(append(append(ids, an[:m]...), id), an[m:]...)
		case j == 0 && len(wer) > 1:
			ids = append(append(ids, an...), id)
		default:
			ids = append(append(ids, id), an...)
		}
	}
	return ids
}

// ketten are row g's chains, each the people who must stand together, in
// order: the children of one family side by side, one who married in
// beside her partner, and two families tied by a couple end to end with
// that couple where they meet. People without a family in the row are a
// chain with their partners. wert is where one's parents or children
// stand; siblings between the ends are ordered by it.
func (o *baumOrdnung) ketten(g int, wert func(string) (float64, bool)) (out [][]string) {
	r := o.reihe(g)
	ort := func(id string) float64 {
		if v, ok := wert(id); ok {
			return v
		}
		return o.pos[id]
	}
	fertig := map[*baumFamilie]bool{}
	for _, f := range r.fams {
		if fertig[f] {
			continue
		}
		var ids []string
		kette := r.kette(f, fertig)
		for i, h := range kette {
			vor, nach := "", ""
			if i > 0 {
				vor = r.verbunden(h, kette[i-1])
			}
			if i+1 < len(kette) {
				nach = r.verbunden(h, kette[i+1])
			}
			ids = append(ids, r.glied(h, vor, nach, ort)...)
		}
		out = append(out, ids)
	}
	for _, id := range r.wer {
		if !r.lose[id] {
			continue
		}
		b := []string{id}
		delete(r.lose, id)
		for i := 0; i < len(b); i++ {
			for _, p := range r.partner(b[i]) {
				if r.lose[p] {
					delete(r.lose, p)
					b = append(b, p)
				}
			}
		}
		out = append(out, o.kette(b, wert))
	}
	slices.SortStableFunc(out, func(a, b []string) int { return cmp.Compare(o.pos[a[0]], o.pos[b[0]]) })
	return out
}

// ordnen orders row g by where each chain's nach (its parents, or its
// children) stand on average, and turns a chain round when its people
// stand the other way than theirs; a chain with neither stays behind
// the one before it.
func (o *baumOrdnung) ordnen(g int, nach map[string][]string) {
	wert := func(id string) (float64, bool) {
		s := 0.0
		for _, m := range nach[id] {
			s += o.pos[m]
		}
		return s / float64(max(1, len(nach[id]))), len(nach[id]) > 0
	}
	ks := o.ketten(g, wert)
	ort, vorher := make([]float64, len(ks)), -1.0
	for i, k := range ks {
		var is, vs []float64
		for j, m := range k {
			if v, ok := wert(m); ok {
				is, vs = append(is, float64(j)), append(vs, v)
			}
		}
		if len(vs) > 0 {
			vorher = summe(vs) / float64(len(vs))
			gleich := 0.0 // do the places and theirs run the same way?
			for j := range vs {
				gleich += (is[j] - summe(is)/float64(len(is))) * (vs[j] - vorher)
			}
			if gleich < 0 {
				slices.Reverse(k)
			}
		}
		ort[i] = vorher
	}
	idx := make([]int, len(ks))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(ort[a], ort[b]) })
	var neu []string
	for _, i := range idx {
		neu = append(neu, ks[i]...)
	}
	o.reihen[g] = neu
	for i, id := range neu {
		o.pos[id] = float64(i)
	}
}

func summe(xs []float64) (s float64) {
	for _, x := range xs {
		s += x
	}
	return s
}

// kette orders people without a family in the row who are partners: by
// where each one's children stand; the one with most partners in the
// middle.
func (o *baumOrdnung) kette(b []string, wert func(string) (float64, bool)) []string {
	ort := func(id string) float64 {
		if v, ok := wert(id); ok {
			return v
		}
		return math.Inf(1)
	}
	b = slices.Clone(b)
	slices.SortStableFunc(b, func(x, y string) int { return cmp.Or(cmp.Compare(ort(x), ort(y)), cmp.Compare(o.pos[x], o.pos[y])) })
	if len(b) < 3 {
		return b
	}
	mitte := slices.MaxFunc(b, func(x, y string) int {
		return cmp.Or(cmp.Compare(len(o.paar[x]), len(o.paar[y])), cmp.Compare(len(o.mit[x]), len(o.mit[y])), strings.Compare(y, x))
	})
	rest := slices.DeleteFunc(b, func(s string) bool { return s == mitte })
	h := len(rest) / 2
	return append(append(slices.Clone(rest[:h]), mitte), rest[h:]...)
}

// abstand is the columns between two neighbours of a row.
func (o *baumOrdnung) abstand(a, b string) float64 {
	if slices.Contains(o.paar[a], b) || slices.Contains(o.mit[a], b) {
		return baumSpalte
	}
	return baumSpalte + baumLuft
}

// schieben moves row g's boxes right by schub, a couple by the most one
// of it asks, each at least its gap from the one before; true when one
// moved.
func (o *baumOrdnung) schieben(g int, x, schub map[string]float64) (bewegt bool) {
	for _, b := range o.bloecke(g) { // a couple moves as one
		d := 0.0
		for _, m := range b {
			d = max(d, schub[m])
		}
		for _, m := range b {
			if d > 0 {
				schub[m] = d
			}
		}
	}
	for i, id := range o.reihen[g] {
		soll := x[id] + schub[id]
		if i > 0 {
			v := o.reihen[g][i-1]
			soll = max(soll, x[v]+o.abstand(v, id))
		}
		bewegt = bewegt || soll > x[id]+0.5
		x[id] = soll
	}
	return bewegt
}

// unter is how far each child of row g must move right to stand under
// its parents' middle.
func (o *baumOrdnung) unter(g int, x map[string]float64, gen map[string]int) map[string]float64 {
	schub := map[string]float64{}
	for _, f := range o.fams {
		if d := f.anker(x) - f.mitte(x); len(f.Eltern) > 0 && gen[f.Kinder[0]] == g && d > 0.5 {
			for _, k := range f.Kinder {
				schub[k] = d
			}
		}
	}
	return schub
}

// ueber is how far each couple of row g must move right to stand over
// its children's middle: with several families as far as the first of
// them asks, so none of them is passed.
func (o *baumOrdnung) ueber(g int, x map[string]float64) map[string]float64 {
	schub := map[string]float64{}
	for _, b := range o.bloecke(g) {
		d := math.Inf(1)
		for _, f := range o.fams {
			if len(f.Eltern) > 0 && slices.Contains(b, f.Eltern[0]) {
				d = min(d, f.mitte(x)-f.anker(x))
			}
		}
		if d > 0.5 && !math.IsInf(d, 1) {
			schub[b[0]] = d
		}
	}
	return schub
}

// setzen gives the component's boxes their columns in x, from start on,
// and answers the right end: the rows as ordered, then pushed right
// until the children stand under their parents' middle and each couple
// over its children's, a bounded number of times.
func (o *baumOrdnung) setzen(x map[string]float64, start float64) float64 {
	gen := map[string]int{}
	for g, ids := range o.reihen {
		o.schieben(g, x, map[string]float64{})
		for _, id := range ids {
			gen[id] = g
		}
	}
	for bewegt, runde := true, 0; bewegt && runde < 12; runde++ {
		bewegt = false
		for g := 1; g <= o.hoch; g++ {
			bewegt = o.schieben(g, x, o.unter(g, x, gen)) || bewegt
		}
		for g := o.hoch - 1; g >= 0; g-- {
			bewegt = o.schieben(g, x, o.ueber(g, x)) || bewegt
		}
	}
	links, rechts := math.Inf(1), start
	for id := range o.pos {
		links = min(links, x[id])
	}
	for id := range o.pos {
		x[id] += start - links
		rechts = max(rechts, x[id])
	}
	return rechts
}

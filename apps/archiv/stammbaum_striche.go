package main

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
)

// What is drawn (owner 2026-11-12: no doubled lines, descent from the
// couple). A baumStrich is one stroke of the picture, straight pieces
// only: the line between two partners (paar; two parents nobody called
// partners get a thin one too, so their children have a place to hang
// from); the family's line down from the middle of it and the bar over
// the children (stamm); a child's drop from the bar (kind); and a bar
// over siblings without parents, or a bracket between half-siblings
// (geschwister). A stroke stands for the lines it shows (Fuer): a child's
// one drop for the line to each parent. No two strokes lie on each other:
// bars and rails that would share a height get a track of their own
// (baumSpuren), an upright that would share a column steps aside (frei).
// TestStammbaumStriche checks the data, not the pixels.

// baumStrich is one stroke: its straight pieces (x1, y1, x2, y2), the
// lines it stands for, or for two parents without a line the two (A, B).
// Fest: every line it stands for is confirmed. Frei: it leads to an
// empty place of the edit tree and stands for no line.
type baumStrich struct {
	Art  string
	Seg  [][4]float64
	Fuer []baumLinie
	A, B string
	Fest bool
	Frei bool
}

// baumZeichner draws the strokes of a laid-out tree.
type baumZeichner struct {
	wo        map[string]baumPerson
	reihe     map[int][]float64 // the boxes' columns by generation
	senkrecht [][3]float64      // every upright so far: x, y1, y2
	out       []baumStrich
}

func (z *baumZeichner) mitteX(id string) float64 { return z.wo[id].X + baumBreite/2 }

// zwischen: a box stands between a and b in their row.
func (z *baumZeichner) zwischen(a, b string) bool {
	lo, hi := min(z.wo[a].X, z.wo[b].X), max(z.wo[a].X, z.wo[b].X)
	return slices.ContainsFunc(z.reihe[z.wo[a].Gen], func(x float64) bool { return x > lo+1 && x < hi-1 })
}

// frei is x, or the nearest column beside it where no upright runs
// between y1 and y2; the upright there is taken.
func (z *baumZeichner) frei(x, y1, y2 float64) float64 {
	for _, d := range []float64{0, 8, -8, 16, -16, 24, -24, 32, -32} {
		if !slices.ContainsFunc(z.senkrecht, func(s [3]float64) bool { return math.Abs(s[0]-x-d) < 4 && min(s[2], y2)-max(s[1], y1) > 0.5 }) {
			x += d
			break
		}
	}
	z.senkrecht = append(z.senkrecht, [3]float64{x, y1, y2})
	return x
}

// baumSpuren gives each stretch (from, to) a track no overlapping
// stretch shares, the lowest free one, from the left.
func baumSpuren(von [][2]float64) []int {
	idx := make([]int, len(von))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(von[a][0], von[b][0]) })
	spur, ende := make([]int, len(von)), []float64{}
	for _, i := range idx {
		s := slices.IndexFunc(ende, func(e float64) bool { return e < von[i][0]-6 })
		if s < 0 {
			s, ende = len(ende), append(ende, 0)
		}
		spur[i], ende[s] = s, von[i][1]
	}
	return spur
}

// spanne is from the leftmost to the rightmost of xs.
func spanne(xs ...float64) [2]float64 { return [2]float64{slices.Min(xs), slices.Max(xs)} }

func alleFest(ls []baumLinie) bool {
	return !slices.ContainsFunc(ls, func(l baumLinie) bool { return !l.Fest })
}

// baumSchiene is a line under the boxes of ids, who do not stand side by
// side: two partners with someone between, or three parents of a child.
type baumSchiene struct {
	ids  []string
	fuer []baumLinie
}

// zeichnen fills sb.Striche from the boxes' places.
func (sb *Stammbaum) zeichnen(fams []*baumFamilie, quer []baumLinie) {
	z := &baumZeichner{wo: map[string]baumPerson{}, reihe: map[int][]float64{}}
	for _, p := range sb.Personen {
		z.wo[p.ID] = p
		z.reihe[p.Gen] = append(z.reihe[p.Gen], p.X)
	}
	anker := z.paare(sb.Linien, fams)
	z.familien(fams, anker)
	z.klammern(sb.Linien, fams, quer)
	sb.Striche = z.out
}

// paare draws the lines between partners and between the parents of a
// family, and answers where each family's line down starts.
func (z *baumZeichner) paare(linien []baumLinie, fams []*baumFamilie) map[*baumFamilie][2]float64 {
	schienen, hat := map[string]*baumSchiene{}, map[string]bool{}
	schiene := func(ids []string, l *baumLinie) {
		ids = slices.Sorted(slices.Values(ids))
		s := schienen[strings.Join(ids, "|")]
		if s == nil {
			s = &baumSchiene{ids: ids}
			schienen[strings.Join(ids, "|")] = s
		}
		if l != nil {
			s.fuer = append(s.fuer, *l)
		}
	}
	gerade := func(a, b string, s baumStrich) {
		l, r := z.wo[a], z.wo[b]
		if r.X < l.X {
			l, r = r, l
		}
		s.Seg = [][4]float64{{l.X + baumBreite, l.Y + baumHoehe/2, r.X, l.Y + baumHoehe/2}}
		z.out = append(z.out, s)
	}
	for _, l := range linien {
		if l.Art != "partner" && l.Art != "expartner" {
			continue
		}
		hat[paarKey(l.A, l.B)] = true
		if z.zwischen(l.A, l.B) {
			schiene([]string{l.A, l.B}, &l)
		} else {
			gerade(l.A, l.B, baumStrich{Art: l.Art, Fuer: []baumLinie{l}, Fest: l.Fest})
		}
	}
	anker := map[*baumFamilie][2]float64{}
	for _, f := range fams {
		switch es := f.Eltern; {
		case len(es) == 1:
			anker[f] = [2]float64{z.mitteX(es[0]), z.wo[es[0]].Y + baumHoehe}
		case len(es) == 2 && !z.zwischen(es[0], es[1]):
			if !hat[paarKey(es[0], es[1])] {
				hat[paarKey(es[0], es[1])] = true
				gerade(es[0], es[1], baumStrich{Art: "mit", A: es[0], B: es[1], Fest: true})
			}
			anker[f] = [2]float64{(z.mitteX(es[0]) + z.mitteX(es[1])) / 2, z.wo[es[0]].Y + baumHoehe/2}
		case len(es) > 1:
			schiene(es, nil)
		}
	}
	z.schienen(schienen, fams, anker)
	return anker
}

// schienen draws the lines under the boxes, each on a track of its row,
// with a tick up to each of its people, and sets the anchor of the
// family whose parents it joins to its middle.
func (z *baumZeichner) schienen(schienen map[string]*baumSchiene, fams []*baumFamilie, anker map[*baumFamilie][2]float64) {
	keys := slices.Sorted(maps.Keys(schienen))
	von := make([][2]float64, len(keys))
	for i, k := range keys {
		s := schienen[k]
		reihe := float64(z.wo[s.ids[0]].Gen) * 1e6 // a row's rails share no track with another row's: far apart
		xs := []float64{}
		for _, id := range s.ids {
			xs = append(xs, z.mitteX(id)+reihe)
		}
		von[i] = spanne(xs...)
	}
	for i, spur := range baumSpuren(von) {
		s := schienen[keys[i]]
		unten := z.wo[s.ids[0]].Y + baumHoehe
		y := unten + 8 + 7*float64(spur%4)
		st := baumStrich{Art: "mit", A: s.ids[0], B: s.ids[1], Fuer: s.fuer, Fest: alleFest(s.fuer)}
		if len(s.fuer) > 0 {
			st.Art = s.fuer[0].Art
		}
		xs := []float64{}
		for _, id := range s.ids {
			x := z.frei(z.mitteX(id), unten, y)
			st.Seg, xs = append(st.Seg, [4]float64{x, unten, x, y}), append(xs, x)
		}
		sp := spanne(xs...)
		st.Seg = append(st.Seg, [4]float64{sp[0], y, sp[1], y})
		z.out = append(z.out, st)
		for _, f := range fams {
			if slices.Equal(f.Eltern, s.ids) {
				anker[f] = [2]float64{(sp[0] + sp[1]) / 2, y}
			}
		}
	}
}

// familien draws each family with parents: the line down from its
// anchor, the bar, a drop a child.
func (z *baumZeichner) familien(fams []*baumFamilie, anker map[*baumFamilie][2]float64) {
	var mit []*baumFamilie
	var von [][2]float64
	for _, f := range fams {
		if a, da := anker[f]; da {
			xs := []float64{a[0]}
			for _, k := range f.Kinder {
				xs = append(xs, z.mitteX(k))
			}
			sp := spanne(xs...)
			reihe := z.wo[f.Kinder[0]].Y * 1e3 // by row, as the rails
			mit, von = append(mit, f), append(von, [2]float64{sp[0] + reihe, sp[1] + reihe})
		}
	}
	spuren := baumSpuren(von)
	for i, f := range mit { // the lines down first: a drop steps aside for them, not they for it
		oben := z.wo[f.Kinder[0]].Y
		y := oben - 22 - 9*float64(spuren[i]%4)
		a := anker[f]
		anker[f] = [2]float64{z.frei(a[0], a[1], y), a[1]}
	}
	for i, f := range mit {
		oben := z.wo[f.Kinder[0]].Y
		y := oben - 22 - 9*float64(spuren[i]%4)
		a := anker[f]
		stamm := baumStrich{Art: "stamm", Seg: [][4]float64{{a[0], a[1], a[0], y}}}
		lo, hi := a[0], a[0]
		for _, k := range f.Kinder {
			x := z.frei(z.mitteX(k), y, oben)
			lo, hi = min(lo, x), max(hi, x)
			ls := f.Fuer[k]
			z.out = append(z.out, baumStrich{Art: "kind", Seg: [][4]float64{{x, y, x, oben}}, Fuer: ls, Fest: alleFest(ls)})
			stamm.Fest = stamm.Fest || slices.ContainsFunc(ls, func(l baumLinie) bool { return l.Fest })
		}
		if hi-lo > 0.5 {
			stamm.Seg = append(stamm.Seg, [4]float64{lo, y, hi, y})
		}
		z.out = append(z.out, stamm)
	}
}

// klammern draws the bar over siblings without parents and the bracket
// between half-siblings, above their boxes.
func (z *baumZeichner) klammern(linien []baumLinie, fams []*baumFamilie, quer []baumLinie) {
	type klammer struct {
		ids   []string
		fuer  []baumLinie
		neben float64 // beside the middle: a drop from the family's bar comes down there
	}
	var ks []klammer
	for _, f := range fams {
		if len(f.Eltern) > 0 {
			continue
		}
		k := klammer{ids: f.Kinder}
		for _, l := range linien {
			if l.Art == "geschwister" && slices.Contains(f.Kinder, l.A) && slices.Contains(f.Kinder, l.B) {
				k.fuer = append(k.fuer, l)
			}
		}
		ks = append(ks, k)
	}
	for _, l := range quer {
		ks = append(ks, klammer{ids: []string{l.A, l.B}, fuer: []baumLinie{l}, neben: 14})
	}
	von := make([][2]float64, len(ks))
	for i, k := range ks {
		xs := []float64{}
		for _, id := range k.ids {
			xs = append(xs, z.mitteX(id)+k.neben)
		}
		sp := spanne(xs...)
		von[i] = [2]float64{sp[0] + z.wo[k.ids[0]].Y*1e3, sp[1] + z.wo[k.ids[0]].Y*1e3}
	}
	for i, spur := range baumSpuren(von) {
		k := ks[i]
		oben := z.wo[k.ids[0]].Y
		y := oben - 8 - 6*float64(spur%2)
		st := baumStrich{Art: "geschwister", Fuer: k.fuer, Fest: alleFest(k.fuer)}
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, id := range k.ids {
			x := z.frei(z.mitteX(id)+k.neben, y, oben)
			st.Seg = append(st.Seg, [4]float64{x, y, x, oben})
			lo, hi = min(lo, x), max(hi, x)
		}
		st.Seg = append(st.Seg, [4]float64{lo, y, hi, y})
		z.out = append(z.out, st)
	}
}

// strichTitel is a line of a stroke as the page says it.
func strichTitel(n *Netz, l baumLinie) string {
	titel := baumPaar(n, l) + ": " + baumWort(l)
	if !l.Fest {
		titel += " (abgeleitet, noch nicht bestätigt)"
	}
	return titel
}

// svg draws the tree: the strokes under the boxes, each that stands for a
// line a link with a wide invisible stroke to hit; one that stands for
// several lines names them all (data-mehr: the script offers them, without
// it the first opens and the list under the tree has the rest); the
// boxes, each a link to the person with what she is of the person, an
// empty place of the edit tree its own box (baumFrei.svg).
func (sb Stammbaum) svg(n *Netz, id string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<div class=baum id=`+id+`><svg viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" role=group aria-label="Stammbaum">`, sb.Breite, sb.Hoehe, sb.Breite, sb.Hoehe)
	for _, s := range sb.Striche {
		var d strings.Builder
		for _, g := range s.Seg {
			fmt.Fprintf(&d, "M%.1f %.1fL%.1f %.1f", g[0], g[1], g[2], g[3])
		}
		klasse := "linie " + s.Art
		if s.Frei {
			klasse += " frei"
		} else if !s.Fest {
			klasse += " vermutet"
		}
		ziel, titel, mehr := kanteAdresse(s.A, s.B), "", ""
		switch {
		case len(s.Fuer) == 0 && s.A == "":
			fmt.Fprintf(&b, `<path class="%s" d="%s"/>`, klasse, d.String())
			continue
		case len(s.Fuer) == 0:
			a, _ := n.Person(s.A)
			c, _ := n.Person(s.B)
			titel = n.Anzeige(a) + " und " + n.Anzeige(c) + ": gemeinsame Kinder"
		default:
			var ts, ms []string
			for _, l := range s.Fuer {
				ts, ms = append(ts, strichTitel(n, l)), append(ms, kanteAdresse(l.A, l.B)+"\t"+strichTitel(n, l))
			}
			ziel, titel = kanteAdresse(s.Fuer[0].A, s.Fuer[0].B), strings.Join(ts, "; ")
			if len(ms) > 1 {
				mehr = ` data-mehr="` + esc(strings.Join(ms, "\n")) + `"`
			}
		}
		fmt.Fprintf(&b, `<a href="%s" aria-label="%s"%s><path class=treffer d="%s"/><path class="%s" d="%s"/></a>`, esc(ziel), esc(titel), mehr, d.String(), klasse, d.String())
	}
	for _, s := range sb.Seiten {
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="k seite">%s</text>`, s.X, s.Y, esc(s.Text))
	}
	for _, p := range sb.Personen {
		if f, frei := sb.Frei[p.ID]; frei {
			f.svg(&b, n, p)
			continue
		}
		vor, nach := baumName(n.Anzeige(p.Knoten))
		wort, _, _ := strings.Cut(sb.Wer[p.ID], " (")
		if strings.Contains(wort, " von ") {
			wort = "" // a way only a sentence says: „Wer ist wer“ has it
		}
		du := ""
		if p.ID == sb.ich {
			du = ` class=du data-du`
		}
		fmt.Fprintf(&b, `<a href="%s"%s data-id="%s" data-name="%s" aria-label="%s"><rect x="%.1f" y="%.1f" width="%.0f" height="%.0f" rx="10"/>`,
			esc(umfeldMitte(p.ID)), du, esc(p.ID), esc(n.Anzeige(p.Knoten)), esc(strings.TrimSuffix(p.Name+", "+sb.Wer[p.ID], ", ")), p.X, p.Y, baumBreite, baumHoehe)
		zeilen := [][2]string{{"", vor}}
		if nach != "" {
			zeilen = append(zeilen, [2]string{" class=k", nach})
		}
		if wort != "" {
			zeilen = append(zeilen, [2]string{` class="k wer"`, bisZu(wort, 18)})
		}
		for i, t := range zeilen {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f"%s>%s</text>`, p.X+baumBreite/2, p.Y+[][]float64{{34}, {27, 43}, {20, 34, 49}}[len(zeilen)-1][i], t[0], esc(t[1]))
		}
		b.WriteString(`</a>`)
	}
	b.WriteString(`</svg></div>`)
	return b.String()
}

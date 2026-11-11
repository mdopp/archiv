package main

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"net/url"
	"slices"
	"strings"
)

// The Stammbaum (archiv#27 step 7; owner 2026-11-10: „Und wenn wir den
// Stammbaum haben sollte es dort einfach Korrekturen an den Kanten
// geben.“): the family circle (familienkreis: the person, her relatives,
// their family names) drawn from its Eltern/Kind, Partner/Ex-Partner and
// Geschwister lines — answered, followed from an answer, written in a
// phone book (solid), or derived and not yet confirmed (dashed).
// Generations are rows, partners side by side. Every person opens its
// detail, every line the edge page, where a correction is one tap; a
// dashed line's edge page has the derivation pre-chosen, so one tap
// confirms it.
//
// Drawn on the server as one SVG, no library, no script: the tree
// scrolls sideways inside its box, never the page. Real data is messy,
// and the layout takes it as it comes: several partners (in a row, the
// one with most in the middle where it can), step-families (a child's
// line from each parent it has), people without birthdays (no order by
// age is needed: a row is ordered by where the parents stand), and lines
// that contradict each other — a cycle, or someone who would be two
// generations at once. Those are not drawn: the generations follow the
// confirmed lines first (a 0-1 breadth-first walk, a derived line costs
// one), and a line that does not fit is listed under the tree as a
// question („Passt nicht in den Stammbaum“), its edge page saying why.

const (
	baumBreite  = 96.0  // a person's box
	baumHoehe   = 48.0  // ≥ 44 px: a finger's target
	baumSpalte  = 112.0 // a box and the gap to the next
	baumZeile   = 124.0 // a generation and the room for its lines
	baumRand    = 24.0
	baumKlammer = 12.0 // a sibling bracket above the boxes
)

// baumArten are the kinds the tree draws; „X is Art of Y“ as the
// answers say them, kind turned into eltern.
var baumArten = []string{"eltern", "kind", "partner", "expartner", "geschwister"}

// baumLinie is one line: for eltern, A is a parent of B; the others are
// symmetric (A < B). Fest: confirmed (answered, followed, a phone book's).
type baumLinie struct {
	A, B, Art string
	Fest      bool
}

// baumPerson is a box: the node, its generation and where it stands.
type baumPerson struct {
	Knoten
	Gen  int
	X, Y float64
}

// Stammbaum is the laid-out tree: the boxes, the lines drawn, the lines
// that did not fit (Brueche), and the size.
type Stammbaum struct {
	Personen      []baumPerson
	Linien        []baumLinie
	Brueche       []baumLinie
	Breite, Hoehe float64
	ich           string
}

// baumLinien are the family lines of the circle, each pair once: the
// person's answer over a phone book's, those over a derived one. n.mu is held.
func (n *Netz) baumLinien() (out []baumLinie) {
	kreis := n.familienkreis()
	gesehen := map[string]bool{}
	dazu := func(x, y, art string, fest bool) {
		if n.selbst[x] {
			x = n.ich
		}
		if n.selbst[y] {
			y = n.ich
		}
		key := paarKey(x, y)
		if gesehen[key] || x == y || !slices.Contains(baumArten, art) || !n.fragbar(x) || !n.fragbar(y) {
			return
		}
		if len(kreis) > 0 && !kreis[x] && !kreis[y] {
			return
		}
		gesehen[key] = true
		switch art {
		case "kind":
			x, y, art = y, x, "eltern"
		case "partner", "expartner", "geschwister":
			x, y = min(x, y), max(x, y)
		}
		out = append(out, baumLinie{A: x, B: y, Art: art, Fest: fest})
	}
	for _, key := range slices.Sorted(maps.Keys(n.antworten)) {
		a := n.antworten[key]
		if a.Art == artSpaeter {
			continue
		}
		x, y, _ := strings.Cut(key, "|")
		if a.X == y {
			x, y = y, x
		}
		dazu(x, y, a.Art, true)
		gesehen[key] = true // keine, fremd, a friend: the person said so, no phone book's line
	}
	for _, f := range n.st.Fest {
		if f.Art != bandBeziehung {
			continue
		}
		if art := buchArt(f.Wie); art != "" {
			dazu(n.von[f.B], n.von[f.A], art, true) // A's card names B as Wie: B is that of A
		}
	}
	for _, a := range n.abgeleitete() {
		if !a.Konflikt {
			dazu(a.X, a.Y, a.Art, false)
		}
	}
	return out
}

// Stammbaum lays the tree out.
func (n *Netz) Stammbaum() Stammbaum {
	if n == nil {
		return Stammbaum{}
	}
	n.mu.Lock()
	linien := n.baumLinien()
	knoten := map[string]Knoten{}
	for _, l := range linien {
		knoten[l.A], knoten[l.B] = n.knoten[l.A], n.knoten[l.B]
	}
	ich := n.ich
	geb := map[string]string{}
	for id := range knoten {
		if g := n.merkmale[id].geb; !g.IsZero() {
			geb[id] = g.Format("2006-01-02")
		}
	}
	n.mu.Unlock()
	return baumLegen(linien, knoten, geb, ich)
}

// baumLegen places the boxes and splits the lines into drawn and broken.
func baumLegen(linien []baumLinie, knoten map[string]Knoten, geb map[string]string, ich string) Stammbaum {
	sb := Stammbaum{ich: ich}
	if len(linien) == 0 {
		return sb
	}
	gen, komp := baumGenerationen(linien, knoten, ich)
	for _, l := range linien {
		soll := 0
		if l.Art == "eltern" {
			soll = 1
		}
		if gen[l.B]-gen[l.A] != soll {
			sb.Brueche = append(sb.Brueche, l)
		} else {
			sb.Linien = append(sb.Linien, l)
		}
	}
	eltern, partner, geschw := map[string][]string{}, map[string][]string{}, map[string][]string{}
	for _, l := range sb.Linien {
		switch l.Art {
		case "eltern":
			eltern[l.B] = append(eltern[l.B], l.A)
		case "geschwister":
			geschw[l.A], geschw[l.B] = append(geschw[l.A], l.B), append(geschw[l.B], l.A)
		default:
			partner[l.A], partner[l.B] = append(partner[l.A], l.B), append(partner[l.B], l.A)
		}
	}
	name := func(id string) string { return knoten[id].Name }
	x := map[string]float64{}
	start := 0.0
	for _, k := range slices.Sorted(maps.Keys(baumMengen(komp))) {
		ids := baumMengen(komp)[k]
		unten := math.MaxInt
		for _, id := range ids {
			unten = min(unten, gen[id])
		}
		reihen := map[int][]string{}
		hoch := 0
		for _, id := range ids {
			gen[id] -= unten
			reihen[gen[id]] = append(reihen[gen[id]], id)
			hoch = max(hoch, gen[id])
		}
		rechts := start
		geordnet := map[int][]string{}
		for g := 0; g <= hoch; g++ {
			reihe := baumReihe(reihen[g], x, eltern, partner, geschw, geb, name)
			geordnet[g] = reihe
			vorher := math.Inf(-1)
			for _, id := range reihe {
				soll := start
				if ks := eltern[id]; len(ks) > 0 {
					soll = 0
					for _, e := range ks {
						soll += x[e]
					}
					soll /= float64(len(ks))
				}
				x[id] = max(soll, vorher+baumSpalte, start)
				vorher = x[id]
				rechts = max(rechts, x[id])
			}
		}
		baumZentrieren(geordnet, hoch, x, eltern)
		start = rechts + baumSpalte*1.5
	}
	for id, k := range knoten {
		sb.Personen = append(sb.Personen, baumPerson{Knoten: k, Gen: gen[id], X: baumRand + x[id], Y: baumRand + baumKlammer + float64(gen[id])*baumZeile})
		sb.Breite = max(sb.Breite, baumRand+x[id]+baumBreite+baumRand)
		sb.Hoehe = max(sb.Hoehe, baumRand+baumKlammer+float64(gen[id])*baumZeile+baumHoehe+baumRand+10)
	}
	slices.SortFunc(sb.Personen, func(a, b baumPerson) int { return cmp.Or(cmp.Compare(a.Gen, b.Gen), cmp.Compare(a.X, b.X)) })
	return sb
}

// baumZentrieren moves parents right, over their children, from the
// lowest row up, as far as the next box in the row lets them: a row is
// laid out left to right before its children, so a parent may stand far
// left of them.
func baumZentrieren(reihen map[int][]string, hoch int, x map[string]float64, eltern map[string][]string) {
	kinder := map[string][]string{}
	for k, es := range eltern {
		for _, e := range es {
			kinder[e] = append(kinder[e], k)
		}
	}
	for g := hoch - 1; g >= 0; g-- {
		reihe := reihen[g]
		for i := len(reihe) - 1; i >= 0; i-- {
			id := reihe[i]
			ks := kinder[id]
			if len(ks) == 0 {
				continue
			}
			soll := 0.0
			for _, k := range ks {
				soll += x[k]
			}
			soll /= float64(len(ks))
			grenze := math.Inf(1)
			if i+1 < len(reihe) {
				grenze = x[reihe[i+1]] - baumSpalte
			}
			if ziel := min(soll, grenze); ziel > x[id] {
				x[id] = ziel
			}
		}
	}
}

// baumMengen are the ids by component.
func baumMengen(komp map[string]int) map[int][]string {
	out := map[int][]string{}
	for _, id := range slices.Sorted(maps.Keys(komp)) {
		out[komp[id]] = append(out[komp[id]], id)
	}
	return out
}

// baumGenerationen gives each node a generation (a parent one above its
// child, partners and siblings level) and its component, the person's
// first: a 0-1 breadth-first walk from the person, then from the others
// by name, where a confirmed line costs nothing and a derived one one,
// so the confirmed lines decide and a derived one is what breaks.
func baumGenerationen(linien []baumLinie, knoten map[string]Knoten, ich string) (gen, komp map[string]int) {
	nach := map[string][]int{}
	for i, l := range linien {
		nach[l.A], nach[l.B] = append(nach[l.A], i), append(nach[l.B], i)
	}
	erst := func(id string) int {
		if id == ich {
			return 0
		}
		return 1
	}
	starts := slices.SortedFunc(maps.Keys(knoten), func(a, b string) int {
		return cmp.Or(cmp.Compare(erst(a), erst(b)), strings.Compare(knoten[a].Name, knoten[b].Name), strings.Compare(a, b))
	})
	gen, komp = map[string]int{}, map[string]int{}
	weit := map[string]int{}
	for k, s := range starts {
		if _, da := komp[s]; da {
			continue
		}
		gen[s], komp[s], weit[s] = 0, k, 0
		schlange := []string{s}
		for len(schlange) > 0 {
			u := schlange[0]
			schlange = schlange[1:]
			for _, i := range nach[u] {
				l := linien[i]
				v, d := l.B, 0
				if l.Art == "eltern" {
					d = 1
				}
				if u == l.B {
					v, d = l.A, -d
				}
				kosten := weit[u]
				if !l.Fest {
					kosten++
				}
				if w, da := weit[v]; da && w <= kosten {
					continue
				}
				gen[v], komp[v], weit[v] = gen[u]+d, k, kosten
				if l.Fest {
					schlange = append([]string{v}, schlange...)
				} else {
					schlange = append(schlange, v)
				}
			}
		}
	}
	return gen, komp
}

// baumReihe orders one row: by where the parents stand (placed already,
// one row up), a sibling taking its sibling's place, then by birthday and
// name; partners side by side, one with several in the middle.
func baumReihe(ids []string, x map[string]float64, eltern, partner, geschw map[string][]string, geb map[string]string, name func(string) string) []string {
	ort := map[string]float64{}
	for _, id := range ids {
		ort[id] = math.Inf(1)
		if ks := eltern[id]; len(ks) > 0 {
			s := 0.0
			for _, e := range ks {
				s += x[e]
			}
			ort[id] = s / float64(len(ks))
		}
	}
	for _, id := range ids {
		for _, g := range geschw[id] {
			if o, da := ort[g]; da && o < ort[id] {
				ort[id] = o
			}
		}
	}
	for _, id := range ids { // one without parents stands by its partner
		for _, p := range partner[id] {
			if o, da := ort[p]; da && math.IsInf(ort[id], 1) && !math.IsInf(o, 1) {
				ort[id] = o
			}
		}
	}
	sortiert := slices.Clone(ids)
	slices.SortFunc(sortiert, func(a, b string) int {
		return cmp.Or(cmp.Compare(ort[a], ort[b]), strings.Compare(geb[a], geb[b]), strings.Compare(name(a), name(b)), strings.Compare(a, b))
	})
	inReihe := map[string]bool{}
	for _, id := range ids {
		inReihe[id] = true
	}
	var out []string
	da := map[string]bool{}
	for _, id := range sortiert {
		if da[id] {
			continue
		}
		// the partners reachable from id in this row, as a chain
		gruppe := []string{id}
		for i := 0; i < len(gruppe); i++ {
			for _, p := range partner[gruppe[i]] {
				if inReihe[p] && !slices.Contains(gruppe, p) {
					gruppe = append(gruppe, p)
				}
			}
		}
		if len(gruppe) > 2 { // the one with most partners in the middle
			mitte := slices.MaxFunc(gruppe, func(a, b string) int {
				return cmp.Or(cmp.Compare(len(partner[a]), len(partner[b])), strings.Compare(b, a))
			})
			rest := slices.DeleteFunc(slices.Clone(gruppe), func(s string) bool { return s == mitte })
			slices.SortFunc(rest, func(a, b string) int { return cmp.Compare(slices.Index(sortiert, a), slices.Index(sortiert, b)) })
			h := len(rest) / 2
			gruppe = append(append(append([]string{}, rest[:h]...), mitte), rest[h:]...)
		} else if len(gruppe) == 2 && ort[gruppe[1]] < ort[gruppe[0]] {
			gruppe[0], gruppe[1] = gruppe[1], gruppe[0]
		}
		for _, g := range gruppe {
			da[g] = true
		}
		out = append(out, gruppe...)
	}
	return out
}

// baumWort is a line's kind as the tree's label says it.
func baumWort(l baumLinie) string {
	if l.Art == "eltern" {
		return "Mutter/Vater und Kind"
	}
	return kurzWort(l.Art)
}

// stammbaumTab is the workplace's Stammbaum: the tree in its box, the
// lines as a list (the screen reader's way and the small screen's), the
// lines that did not fit, and „Beziehung ergänzen“.
func stammbaumTab(b *strings.Builder, n *Netz) {
	sb := n.Stammbaum()
	if len(sb.Personen) == 0 {
		fmt.Fprintf(b, `<p class=ruhig><span class="punkt grau"></span>%s</p>`, esc(saysStammbaumLeer))
	} else {
		b.WriteString(sb.svg(n))
		b.WriteString(`<p class=k>Tipp auf eine Person: ihre Verbindungen. Tipp auf eine Linie: was die beiden verbindet, und dort nachbessern. Gestrichelt ist abgeleitet und noch nicht bestätigt.</p>`)
		sb.liste(b, n)
	}
	if len(sb.Brueche) > 0 {
		b.WriteString(`<section class=karte><h2>Passt nicht in den Stammbaum</h2><ul class=nahe>`)
		for _, l := range sb.Brueche {
			fmt.Fprintf(b, `<li><a class=zeile href="%s">%s <span class=k>%s</span></a></li>`, esc(kanteAdresse(l.A, l.B)+"&pruefen=1"), esc(baumPaar(n, l)), esc(baumWort(l)+" · bitte prüfen"))
		}
		b.WriteString(`</ul></section>`)
	}
	fmt.Fprintf(b, `<details class=karte id=dazu><summary>+ Beziehung ergänzen</summary><form method=get action="%s" class=feld><input type=hidden name=zurueck value="/tresor?tab=stammbaum"><label>Wer?%s</label><label>Zu wem?%s</label><button>Weiter</button></form>%s</details>`,
		kantePfad, namenFeld("von", "Du"), namenFeld("wer", ""), namenListe(n))
}

// baumPaar is a line's two names: „Anna Berg und Ben Berg“.
func baumPaar(n *Netz, l baumLinie) string {
	a, _ := n.Person(l.A)
	c, _ := n.Person(l.B)
	return n.Anzeige(a) + " und " + n.Anzeige(c)
}

// liste is every drawn line as a link, folded.
func (sb Stammbaum) liste(b *strings.Builder, n *Netz) {
	fmt.Fprintf(b, `<details class=karte id=baum-liste><summary>Alle Linien als Liste (%d)</summary><ul>`, len(sb.Linien))
	for _, l := range sb.Linien {
		wie := baumWort(l)
		if l.Art == "eltern" {
			a, _ := n.Person(l.A)
			wie = n.Anzeige(a) + " ist Mutter/Vater"
		}
		if !l.Fest {
			wie += " · abgeleitet, noch nicht bestätigt"
		}
		fmt.Fprintf(b, `<li><a href="%s">%s<span class=k>%s</span></a></li>`, esc(kanteAdresse(l.A, l.B)), esc(baumPaar(n, l)), esc(wie))
	}
	b.WriteString(`</ul></details>`)
}

// svg draws the tree: the lines under the boxes, each a link with a wide
// invisible stroke to hit; the boxes, each a link to the person.
func (sb Stammbaum) svg(n *Netz) string {
	wo := map[string]baumPerson{}
	for _, p := range sb.Personen {
		wo[p.ID] = p
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<div class=baum id=stammbaum><svg viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" role=group aria-label="Stammbaum">`, sb.Breite, sb.Hoehe, sb.Breite, sb.Hoehe)
	elternVon := map[string][]string{}
	for _, l := range sb.Linien {
		if l.Art == "eltern" {
			elternVon[l.B] = append(elternVon[l.B], l.A)
		}
	}
	for _, l := range sb.Linien {
		a, c := wo[l.A], wo[l.B]
		var d string
		switch l.Art {
		case "eltern":
			i := slices.Index(elternVon[l.B], l.A)
			versatz := 0.0
			if len(elternVon[l.B]) > 1 {
				versatz = float64(i)*8 - 4*float64(len(elternVon[l.B])-1)
			}
			px, py := a.X+baumBreite/2, a.Y+baumHoehe
			cx, cy := c.X+baumBreite/2+versatz, c.Y
			mitte := py + (cy-py)/2 + versatz
			d = fmt.Sprintf("M%.1f %.1fV%.1fH%.1fV%.1f", px, py, mitte, cx, cy)
		case "geschwister":
			if gemeinsam(elternVon[l.A], elternVon[l.B]) {
				continue // the parents' lines show it; the edge stays a tap away in the list
			}
			d = fmt.Sprintf("M%.1f %.1fV%.1fH%.1fV%.1f", a.X+baumBreite/2, a.Y, a.Y-baumKlammer, c.X+baumBreite/2, c.Y)
		default:
			links, rechts := a, c
			if c.X < a.X {
				links, rechts = c, a
			}
			if rechts.X-links.X <= baumSpalte+0.5 {
				d = fmt.Sprintf("M%.1f %.1fH%.1f", links.X+baumBreite, links.Y+baumHoehe/2, rechts.X)
			} else { // someone between: under the boxes
				d = fmt.Sprintf("M%.1f %.1fV%.1fH%.1fV%.1f", links.X+baumBreite/2, links.Y+baumHoehe, links.Y+baumHoehe+8, rechts.X+baumBreite/2, rechts.Y+baumHoehe)
			}
		}
		klasse := "linie " + l.Art
		titel := baumPaar(n, l) + ": " + baumWort(l)
		if !l.Fest {
			klasse += " vermutet"
			titel += " (abgeleitet, noch nicht bestätigt)"
		}
		fmt.Fprintf(&b, `<a href="%s" aria-label="%s"><path class=treffer d="%s"/><path class="%s" d="%s"/></a>`, esc(kanteAdresse(l.A, l.B)), esc(titel), d, klasse, d)
	}
	for _, p := range sb.Personen {
		vor, nach := baumName(n.Anzeige(p.Knoten))
		du := ""
		if p.ID == sb.ich {
			du = ` class=du data-du`
		}
		fmt.Fprintf(&b, `<a href="%s"%s aria-label="%s"><rect x="%.1f" y="%.1f" width="%.0f" height="%.0f" rx="10"/><text x="%.1f" y="%.1f">%s</text>`,
			esc("/tresor/umfeld?"+url.Values{"mitte": {p.ID}}.Encode()), du, esc(p.Name), p.X, p.Y, baumBreite, baumHoehe, p.X+baumBreite/2, p.Y+map[bool]float64{true: 29, false: 21}[nach == ""], esc(vor))
		if nach != "" {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class=k>%s</text>`, p.X+baumBreite/2, p.Y+37, esc(nach))
		}
		b.WriteString(`</a>`)
	}
	b.WriteString(`</svg></div>`)
	return b.String()
}

// gemeinsam: the two lists share one.
func gemeinsam(a, b []string) bool {
	return slices.ContainsFunc(a, func(s string) bool { return slices.Contains(b, s) })
}

// baumName is a box's two lines: the first name, the rest, each short.
func baumName(name string) (string, string) {
	ws := strings.Fields(name)
	if len(ws) < 2 {
		return bisZu(name, 13), ""
	}
	return bisZu(ws[0], 13), bisZu(strings.Join(ws[1:], " "), 14)
}

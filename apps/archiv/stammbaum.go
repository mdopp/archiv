package main

import (
	"cmp"
	"fmt"
	"maps"
	"math"
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
// Drawn on the server as one SVG, no library: the tree scrolls sideways
// inside its box, never the page. Real data is messy, and the layout
// takes it as it comes (stammbaum_legen.go, stammbaum_striche.go):
// several partners (in a row, the one with most in the middle),
// step-families (a rail under three parents), people without birthdays
// (a row is ordered by where the families stand), and lines
// that contradict each other — a cycle, or someone who would be two
// generations at once. Those are not drawn: the generations follow the
// confirmed lines first (a 0-1 breadth-first walk, a derived line costs
// one), and a line that does not fit is listed under the tree as a
// question („Passt nicht in den Stammbaum“), its edge page saying why.

const (
	baumBreite  = 96.0  // a person's box
	baumHoehe   = 58.0  // ≥ 44 px: a finger's target; the name and what she is of the person
	baumSpalte  = 112.0 // a box and the gap to its partner
	baumLuft    = 12.0  // more gap between two who are no couple
	baumZeile   = 150.0 // a generation and the room for its lines
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
	// Weitere: the lines a step away that fit (stammbaum_weitere.go),
	// drawn as a place; Fragen those whose place no drawn line gives;
	// Seiten the side rows' labels.
	Weitere []baumLinie
	Fragen  []BaumFrage
	Seiten  []baumSeite
	// Striche: what is drawn (stammbaum_striche.go); Wer: what each one is
	// of the person by the lines known (verwandt.go)
	Striche []baumStrich
	Wer     map[string]string
}

// baumSeite is a side row's label and where it stands.
type baumSeite struct {
	X, Y float64
	Text string
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
		if gesehen[key] || x == y || !slices.Contains(baumArten, art) && !(fest && slices.Contains(baumWeitere, art)) || !n.fragbar(x) || !n.fragbar(y) {
			return
		}
		if len(kreis) > 0 && !kreis[x] && !kreis[y] {
			return
		}
		gesehen[key] = true
		switch stufe := baumStufe[art]; {
		case stufe < 0:
			x, y, art = y, x, gegen[art]
		case stufe == 0:
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
	weg := maps.Clone(n.baumWeg)
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
	wer := n.verwandte(ich, true)
	n.mu.Unlock()
	sb := baumLegen(linien, knoten, geb, ich)
	sb.Wer = wer
	sb.Fragen = slices.DeleteFunc(baumFragen(sb.Weitere, sb.Linien, ich, weg), func(f BaumFrage) bool { return knoten[f.X].Tot || knoten[f.Y].Tot })
	return sb
}

// baumLegen places the boxes and splits the lines into drawn and broken.
func baumLegen(linien []baumLinie, knoten map[string]Knoten, geb map[string]string, ich string) Stammbaum {
	sb := Stammbaum{ich: ich}
	if len(linien) == 0 {
		return sb
	}
	gen, komp := baumGenerationen(linien, knoten, ich)
	for _, l := range linien {
		switch {
		case gen[l.B]-gen[l.A] != baumStufe[l.Art]:
			sb.Brueche = append(sb.Brueche, l)
		case slices.Contains(baumWeitere, l.Art):
			sb.Weitere = append(sb.Weitere, l)
		default:
			sb.Linien = append(sb.Linien, l)
		}
	}
	seite := baumSeitlich(sb)
	fams, quer := baumFamilien(sb.Linien)
	x := map[string]float64{}
	start := 0.0
	for _, k := range slices.Sorted(maps.Keys(baumMengen(komp))) {
		ids := baumMengen(komp)[k]
		unten := math.MaxInt
		for _, id := range ids {
			unten = min(unten, gen[id])
		}
		reihen, seitlich, hoch := baumZeilen(ids, gen, unten, seite)
		o := neueBaumOrdnung(reihen, hoch, sb.Linien, fams, func(a, b string) int {
			return cmp.Or(strings.Compare(geb[a], geb[b]), strings.Compare(knoten[a].Name, knoten[b].Name), strings.Compare(a, b))
		})
		rechts := o.setzen(x, start)
		rechts = max(rechts, sb.seitenLegen(seitlich, seite, x, o.reihen, start))
		start = rechts + baumSpalte*1.5
	}
	for id, k := range knoten {
		sb.Personen = append(sb.Personen, baumPerson{Knoten: k, Gen: gen[id], X: baumRand + x[id], Y: baumRand + baumKlammer + float64(gen[id])*baumZeile})
		sb.Breite = max(sb.Breite, baumRand+x[id]+baumBreite+baumRand)
		sb.Hoehe = max(sb.Hoehe, baumRand+baumKlammer+float64(gen[id])*baumZeile+baumHoehe+baumRand)
	}
	for _, s := range sb.Seiten {
		sb.Breite = max(sb.Breite, baumRand+s.X+float64(len([]rune(s.Text)))*6.5+baumRand)
	}
	slices.SortFunc(sb.Personen, func(a, b baumPerson) int { return cmp.Or(cmp.Compare(a.Gen, b.Gen), cmp.Compare(a.X, b.X)) })
	sb.zeichnen(fams, quer)
	return sb
}

// baumZeilen moves a component's generations to start at 0 (unten its
// lowest) and splits its people into the tree's rows and the side rows.
func baumZeilen(ids []string, gen map[string]int, unten int, seite map[string]string) (reihen, seitlich map[int][]string, hoch int) {
	reihen, seitlich = map[int][]string{}, map[int][]string{}
	for _, id := range ids {
		gen[id] -= unten
		if seite[id] != "" {
			seitlich[gen[id]] = append(seitlich[gen[id]], id)
		} else {
			reihen[gen[id]] = append(reihen[gen[id]], id)
		}
		hoch = max(hoch, gen[id])
	}
	return reihen, seitlich, hoch
}

// baumSeitlich are the people no drawn line holds, only a line a step
// away, with that line's kind: they stand on a side row.
func baumSeitlich(sb Stammbaum) map[string]string {
	drin := map[string]bool{}
	for _, l := range sb.Linien {
		drin[l.A], drin[l.B] = true, true
	}
	seite := map[string]string{}
	for _, l := range sb.Weitere {
		for _, id := range []string{l.A, l.B} {
			if !drin[id] && seite[id] == "" && (id != sb.ich) {
				seite[id] = l.Art
			}
		}
	}
	if len(drin) == 0 { // nothing drawn: the person is the tree, the others beside her
		delete(seite, sb.ich)
	}
	return seite
}

// seitenLegen puts each side row right of its row in the tree (a half
// column apart; at start when the row is empty), its label above its
// first box, and answers the right end.
func (sb *Stammbaum) seitenLegen(reihen map[int][]string, seite map[string]string, x map[string]float64, geordnet map[int][]string, start float64) float64 {
	ende := start
	for _, g := range slices.Sorted(maps.Keys(reihen)) {
		ids := slices.Sorted(slices.Values(reihen[g]))
		links := start
		if r := geordnet[g]; len(r) > 0 {
			links = x[r[len(r)-1]] + baumSpalte*1.5
		}
		var arten []string
		for i, id := range ids {
			x[id] = links + float64(i)*baumSpalte
			ende = max(ende, x[id])
			arten = append(arten, seite[id])
		}
		sb.Seiten = append(sb.Seiten, baumSeite{X: baumRand + links, Y: baumRand + baumKlammer + float64(g)*baumZeile - 6, Text: seitenWort(arten)})
	}
	return ende
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
				v, d := l.B, baumStufe[l.Art]
				if u == l.B {
					v, d = l.A, -d
				}
				kosten := weit[u]
				fest := l.Fest && !slices.Contains(baumWeitere, l.Art)
				if !fest {
					kosten++
				}
				if w, da := weit[v]; da && w <= kosten {
					continue
				}
				gen[v], komp[v], weit[v] = gen[u]+d, k, kosten
				if fest {
					schlange = append([]string{v}, schlange...)
				} else {
					schlange = append(schlange, v)
				}
			}
		}
	}
	return gen, komp
}

// baumWort is a line's kind as the tree's label says it.
func baumWort(l baumLinie) string {
	if l.Art == "eltern" {
		return "Mutter/Vater und Kind"
	}
	return kurzWort(l.Art)
}

// stammbaumTab is the workplace's Stammbaum, in the box the editor's
// answer fills anew (stammbaum_bau.go).
func stammbaumTab(b *strings.Builder, n *Netz) {
	b.WriteString(`<div id=baum-tab>` + stammbaumInhalt(n) + `</div>`)
}

// stammbaumInhalt is the tab: the tree in its box, „Verbindungen bauen“,
// the lines and who is who as lists (the screen reader's way and the
// small screen's), the relatives without a place, the lines that did
// not fit, and „Beziehung ergänzen“.
func stammbaumInhalt(n *Netz) string {
	b := &strings.Builder{}
	sb := n.Stammbaum()
	if len(sb.Personen) == 0 {
		fmt.Fprintf(b, `<p class=ruhig><span class="punkt grau"></span>%s</p>`, esc(saysStammbaumLeer))
		bauAbschnitt(b, n, sb)
	} else {
		b.WriteString(sb.svg(n))
		b.WriteString(`<p class=k>Tipp auf eine Person: ihre Verbindungen. Tipp auf eine Linie: was die beiden verbindet, und dort nachbessern. Gestrichelt ist abgeleitet und noch nicht bestätigt. <button type=button class=leise id=baum-ganz>Ganzen Baum zeigen</button></p>`)
		bauAbschnitt(b, n, sb)
		sb.liste(b, n)
	}
	baumFragenTab(b, n, sb.Fragen)
	if len(sb.Brueche) > 0 {
		b.WriteString(`<section class=karte><h2>Passt nicht in den Stammbaum</h2><ul class=nahe>`)
		for _, l := range sb.Brueche {
			fmt.Fprintf(b, `<li><a class=zeile href="%s">%s <span class=k>%s</span></a></li>`, esc(kanteAdresse(l.A, l.B)+"&pruefen=1"), esc(baumPaar(n, l)), esc(baumWort(l)+" · bitte prüfen"))
		}
		b.WriteString(`</ul></section>`)
	}
	fmt.Fprintf(b, `<details class=karte id=dazu><summary>+ Beziehung ergänzen</summary><form method=get action="%s" class=feld><input type=hidden name=zurueck value="/tresor?tab=stammbaum"><label>Wer?%s</label><label>Zu wem?%s</label><button>Weiter</button></form>%s</details>`,
		kantePfad, namenFeld("von", "Du"), namenFeld("wer", ""), namenListe(n))
	return b.String()
}

// baumPaar is a line's two names: „Anna Berg und Ben Berg“.
func baumPaar(n *Netz, l baumLinie) string {
	a, _ := n.Person(l.A)
	c, _ := n.Person(l.B)
	return n.Anzeige(a) + " und " + n.Anzeige(c)
}

// liste is every drawn line as a link, folded.
func (sb Stammbaum) liste(b *strings.Builder, n *Netz) {
	fmt.Fprintf(b, `<details class=karte id=baum-liste><summary>Alle Linien als Liste (%d)</summary><ul>`, len(sb.Linien)+len(sb.Weitere))
	for _, l := range append(slices.Clone(sb.Linien), sb.Weitere...) {
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
	var wer []baumPerson
	for _, p := range sb.Personen {
		if sb.Wer[p.ID] != "" {
			wer = append(wer, p)
		}
	}
	if len(wer) == 0 {
		return
	}
	fmt.Fprintf(b, `<details class=karte id=baum-wer><summary>Wer ist wer für dich (%d)</summary><ul>`, len(wer))
	for _, p := range wer {
		fmt.Fprintf(b, `<li><a href="%s">%s<span class=k>%s</span></a></li>`, esc(umfeldMitte(p.ID)), esc(p.mitKreuz()), esc(sb.Wer[p.ID]))
	}
	b.WriteString(`</ul></details>`)
}

// baumName is a box's two lines: the first name, the rest, each short.
func baumName(name string) (string, string) {
	ws := strings.Fields(name)
	if len(ws) < 2 {
		return bisZu(name, 13), ""
	}
	return bisZu(ws[0], 13), bisZu(strings.Join(ws[1:], " "), 14)
}

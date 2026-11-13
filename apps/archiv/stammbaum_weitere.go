package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The relatives a step away in the Stammbaum (owner 2026-11-11: „Wenn
// Hedwig meine Tante ist, wird das nicht im Stammbaum angezeigt.“): every
// family kind the archive knows is in the tree. Großeltern, Onkel/Tante,
// Cousin/e, Neffe/Nichte, Enkel/in, Schwiegereltern, Schwiegerkind and
// Schwager/Schwägerin each stand for a path over someone in between (an
// aunt is a sibling of a parent). Where the path is drawn, the relative
// stands at its place by it. Where it is not, the relative stands in the
// right generation on a side row („Tanten und Onkel“), and one one-tap
// question places it: „Hedwig ist Schwester oder Bruder von …“ with the
// names of the ones in between (an aunt: the parents) and „weiß nicht“.
// The answer is an ordinary answer about Hedwig and that parent, so the
// tree derives the rest. „Sonstige verwandt“ has no path: its question is
// the edge page, to say how. Nothing is dropped: a relative whose path
// lacks the one in between says what is missing, and after „weiß nicht“
// it stays on the side row, not asked again (umfeld/baum-weissnicht.json).

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

// umwegWort is an answer as the question says it: „Schwester oder Bruder“.
var umwegWort = map[string]string{"eltern": "Mutter oder Vater", "kind": "Kind", "geschwister": "Schwester oder Bruder", "partner": "Partner/in", "onkel": "Tante oder Onkel"}

// baumWahl is one button: X is Art of M.
type baumWahl struct{ M, Art string }

// BaumFrage is a relative a step away the drawn lines do not place: X is
// Art of Y; Wahl the ones in between, Fehlt what is missing when there
// is none, Weg: the person said „weiß nicht“.
type BaumFrage struct {
	X, Y, Art string
	Wahl      []baumWahl
	Fehlt     string
	Weg       bool
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
func baumFragen(weitere, linien []baumLinie, ich string, weg map[string]time.Time) (out []BaumFrage) {
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
		f := BaumFrage{X: x, Y: y, Art: art, Weg: !weg[paarKey(x, y)].IsZero()}
		if f.platz(nahe) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// platz: the drawn lines place X; else the buttons that would.
func (f *BaumFrage) platz(nahe baumNahe) bool {
	wege := baumUmwege[f.Art]
	for _, u := range wege {
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
	if len(f.Wahl) == 0 && len(wege) > 0 {
		f.Fehlt = umwegWort[wege[0].Ueber]
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

// baumFragenTab is the section under the tree (owner 2026-11-13: „das
// ist immer noch viel … man sollte sagen können ‚nichts davon' und eine
// andere Person nennen und anlegen können“): one relative at a time, how
// many follow, and beside the names „Jemand anderes …“, a name field
// that finds a person or makes a new one. After „weiß nicht“ a relative
// stays on the side row of the tree and is not asked here again.
func baumFragenTab(b *strings.Builder, n *Netz, fs []BaumFrage) {
	fs = slices.DeleteFunc(slices.Clone(fs), func(f BaumFrage) bool { return f.Weg })
	if len(fs) == 0 {
		return
	}
	f := fs[0]
	b.WriteString(`<section class=karte id=einordnen><h2>Noch ohne Platz im Stammbaum</h2>`)
	x, _ := n.Person(f.X)
	y, _ := n.Person(f.Y)
	wer, von := n.Anzeige(x), "von "+n.Anzeige(y)
	if f.Y == n.Ich() {
		von = "von dir"
	}
	fmt.Fprintf(b, `<div class=einordnen><p><b>%s</b> <span class=k>%s</span></p>`, esc(wer), esc(kurzWort(f.Art)+" "+von))
	switch {
	case len(f.Wahl) > 0:
		b.WriteString(baumFrageForm(n, f, wer))
	case f.Fehlt != "":
		fmt.Fprintf(b, `<p>%s</p>`, esc("Für den Platz fehlt noch: "+f.Fehlt+" "+von+"."))
		if u := baumUmwege[f.Art][0].Ueber; slices.Contains(bauArten, u) {
			b.WriteString(baumNameForm(f.Y, "", map[string]string{u: "Eintragen"}, false))
			b.WriteString(`<p class="k ohne-skript"><a href="#dazu">Eintragen</a></p>`)
		} else {
			b.WriteString(`<p class=k><a href="#dazu">Eintragen</a></p>`)
		}
	default:
		fmt.Fprintf(b, `<p><a class=zeile href="%s">%s</a></p>`, esc(kanteAdresse(f.X, f.Y)), esc("Wie genau ist "+wer+" verwandt?"))
	}
	b.WriteString(`</div>`)
	if len(fs) > 1 {
		fmt.Fprintf(b, `<p class=k>%s</p>`, esc(fmt.Sprintf("Danach noch %d.", len(fs)-1)))
	}
	b.WriteString(`</section>`)
}

// baumFrageForm is „Hedwig ist Schwester oder Bruder von …“, the names
// as chips, „Jemand anderes …“ and „weiß nicht“; a chip sets the
// relation as the edge page does.
func baumFrageForm(n *Netz, f BaumFrage, wer string) string {
	var b strings.Builder
	eine := !slices.ContainsFunc(f.Wahl, func(w baumWahl) bool { return w.Art != f.Wahl[0].Art })
	frage := wer + " ist …"
	if eine {
		frage = wer + " ist " + umwegWort[f.Wahl[0].Art] + " von …"
	}
	fmt.Fprintf(&b, `<p>%s</p><div class=chips>`, esc(frage))
	knoepfe := map[string]string{}
	for _, w := range f.Wahl {
		m, _ := n.Person(w.M)
		text := n.Anzeige(m)
		if !eine {
			text = umwegWort[w.Art] + " von " + text
		}
		fmt.Fprintf(&b, `<form method=post action="%s" class=sofort><input type=hidden name=x value="%s"><input type=hidden name=y value="%s"><input type=hidden name=festlegen value=1><input type=hidden name=zurueck value="/tresor?tab=stammbaum">%s</form>`,
			antwortPfad, esc(f.X), esc(w.M), chip(w.Art, text, ""))
		// The one named is the counterpart of X: X is w.Art of her.
		knoepfe[cmp.Or(gegen[w.Art], w.Art)] = map[bool]string{true: "Eintragen", false: wer + " ist " + umwegWort[w.Art]}[eine]
	}
	b.WriteString(`<button type=button class="chip anderer">Jemand anderes …</button>`)
	fmt.Fprintf(&b, `<form method=post action="%s" class=sofort><input type=hidden name=paar value="%s"><input type=hidden name=tu value=weissnicht><input type=hidden name=zurueck value="/tresor?tab=stammbaum"><button class="chip leise" data-laeuft="weiß nicht …">weiß nicht</button></form></div>`,
		antwortPfad, esc(paarKey(f.X, f.Y)))
	b.WriteString(baumNameForm(f.X, paarKey(f.X, f.Y), knoepfe, true))
	return b.String()
}

// baumNameForm is a name field for the editor's post: the one named is
// art of y, one button an art; erledigt the question it answers. The
// script finds the name among the editor's names or makes a new person.
func baumNameForm(y, erledigt string, knoepfe map[string]string, zu bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<form class=bau-frage data-y="%s" data-erledigt="%s"%s><label>Wer?<input name=name list=namen data-namen autocomplete=off required placeholder="Name, auch neu"></label><div class=chips>`,
		esc(y), esc(erledigt), map[bool]string{true: " hidden"}[zu])
	for _, art := range slices.Sorted(maps.Keys(knoepfe)) {
		fmt.Fprintf(&b, `<button name=art value="%s" class=voll>%s</button>`, esc(art), esc(knoepfe[art]))
	}
	b.WriteString(`</div></form>`)
	return b.String()
}

func (i Innen) umfeldBaumWeg() string { return filepath.Join(i.Umfeld(), "baum-weissnicht.json") }

func baumWegLaden(innen Innen) map[string]time.Time {
	out := map[string]time.Time{}
	if b, err := os.ReadFile(innen.umfeldBaumWeg()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// BaumWeissNicht keeps „weiß nicht“ for the relative of the pair key: it
// stays on the side row, not asked again.
func (u *Umfeld) BaumWeissNicht(innen Innen, key string) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	n.mu.Lock()
	n.baumWeg[key] = n.now()
	alle := maps.Clone(n.baumWeg)
	n.mu.Unlock()
	return jsonImTresor(innen.umfeldBaumWeg(), alle)
}

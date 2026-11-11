package main

import (
	"math"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// baumNetz is Uwe's family (h/uwe is the person): his card names Silke
// his wife, Lena his daughter, Bruno his brother; he answered Hedwig is
// his mother and Petra his ex-partner; more answers as given. Nobody has
// a birthday. Others: Max and Moritz, no relatives.
func baumNetz(mehr map[string]antwort) *Netz {
	n := buchNetz("h/uwe", [][3]string{{"h/uwe", "h/silke", "Ehefrau"}, {"h/uwe", "h/lena", "Tochter"}, {"h/uwe", "h/bruno", "Bruder"}}, nil)
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, k := range []string{"hedwig", "petra", "max", "moritz", "xaver", "yvonne", "zora"} {
		n.st.Kontakte = append(n.st.Kontakte, netzKontakt{Key: "h/" + k, Name: strings.ToUpper(k[:1]) + k[1:]})
	}
	am := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	n.antworten[paarKey("h/hedwig", "h/uwe")] = antwort{X: "h/hedwig", Art: "eltern", Am: am}
	n.antworten[paarKey("h/petra", "h/uwe")] = antwort{X: "h/petra", Art: "expartner", Am: am}
	for k, a := range mehr {
		n.antworten[k] = a
	}
	n.bauen()
	return n
}

func baumPos(sb Stammbaum) map[string]baumPerson {
	out := map[string]baumPerson{}
	for _, p := range sb.Personen {
		out[p.ID] = p
	}
	return out
}

func baumHat(sb Stammbaum, a, b, art string, fest bool) bool {
	return slices.Contains(sb.Linien, baumLinie{A: a, B: b, Art: art, Fest: fest})
}

// Generations as rows (Hedwig above Uwe, Lena below), partners side by
// side with the one of several partners in the middle, a child under its
// parents; the phone book's and the answered lines solid, the derived
// one (Silke, Uwe's wife, Lena's mother?) dashed; nobody outside the
// family circle; no birthday needed.
func TestStammbaumLayout(t *testing.T) {
	sb := baumNetz(nil).Stammbaum()
	p := baumPos(sb)
	if len(p) != 6 || p["h/max"].ID != "" {
		t.Fatalf("who is in it: %+v", sb.Personen)
	}
	if p["h/hedwig"].Gen != 0 || p["h/uwe"].Gen != 1 || p["h/silke"].Gen != 1 || p["h/bruno"].Gen != 1 || p["h/petra"].Gen != 1 || p["h/lena"].Gen != 2 {
		t.Fatalf("generations: %+v", sb.Personen)
	}
	if p["h/uwe"].Y-p["h/hedwig"].Y != baumZeile || p["h/lena"].Y-p["h/uwe"].Y != baumZeile {
		t.Fatal("rows")
	}
	// Uwe with two partners: between them, each a column away
	if math.Abs(math.Abs(p["h/silke"].X-p["h/uwe"].X)-baumSpalte) > 0.1 || math.Abs(math.Abs(p["h/petra"].X-p["h/uwe"].X)-baumSpalte) > 0.1 ||
		(p["h/silke"].X-p["h/uwe"].X)*(p["h/petra"].X-p["h/uwe"].X) > 0 {
		t.Fatalf("partners: uwe %.0f silke %.0f petra %.0f", p["h/uwe"].X, p["h/silke"].X, p["h/petra"].X)
	}
	// Lena stands under her parents, between them
	if lo, hi := min(p["h/uwe"].X, p["h/silke"].X), max(p["h/uwe"].X, p["h/silke"].X); p["h/lena"].X < lo || p["h/lena"].X > hi {
		t.Fatalf("Lena %.0f not under %.0f..%.0f", p["h/lena"].X, lo, hi)
	}
	for _, l := range []struct {
		a, b, art string
		fest      bool
	}{{"h/hedwig", "h/uwe", "eltern", true}, {"h/silke", "h/uwe", "partner", true}, {"h/uwe", "h/lena", "eltern", true}, {"h/bruno", "h/uwe", "geschwister", true},
		{"h/petra", "h/uwe", "expartner", true}, {"h/silke", "h/lena", "eltern", false}} {
		if !baumHat(sb, l.a, l.b, l.art, l.fest) {
			t.Fatalf("no line %+v in %+v", l, sb.Linien)
		}
	}
	if len(sb.Brueche) != 0 || sb.Breite <= 0 || sb.Hoehe < 2*baumZeile+baumHoehe {
		t.Fatalf("%+v %v×%v", sb.Brueche, sb.Breite, sb.Hoehe)
	}
}

// The SVG: every person a link to its detail (≥ 44 px), every line a link
// to its edge page with a wide hit stroke, dashed for derived; Uwe marked
// for the script to scroll to; the lines as a list too; sideways scroll
// only inside the box.
func TestStammbaumSVG(t *testing.T) {
	n := baumNetz(nil)
	var b strings.Builder
	stammbaumTab(&b, n)
	page := b.String()
	for _, want := range []string{`<div class=baum id=stammbaum><svg viewBox=`, `<a href="/tresor/umfeld?mitte=h%2Fuwe" class=du data-du aria-label="Uwe"><rect`,
		`width="96" height="48" rx="10"`, `<a href="/tresor/kante?a=h%2Fsilke&amp;b=h%2Flena" aria-label="Silke und Lena: Mutter/Vater und Kind (abgeleitet, noch nicht bestätigt)"><path class=treffer`,
		`class="linie eltern vermutet"`, `aria-label="Du und Lena: Mutter/Vater und Kind"><path class=treffer d="M`, `class="linie eltern" d="M`,
		`class="linie partner"`, `class="linie expartner"`, `class="linie geschwister"`, "Alle Linien als Liste (6)", "Silke ist Mutter/Vater · abgeleitet, noch nicht bestätigt"} {
		if !strings.Contains(page, want) {
			t.Fatalf("%q missing:\n%s", want, page)
		}
	}
	if svg, _, _ := strings.Cut(page, "</svg>"); strings.Contains(page, "Passt nicht") || strings.Contains(svg, "Max") {
		t.Fatal("a break, or someone outside the family")
	}
	for _, want := range []string{".baum{max-width:100%;overflow-x:auto", ".baum .treffer{fill:none;stroke:transparent;stroke-width:22", ".baum .linie.vermutet{stroke-dasharray"} {
		if !strings.Contains(css, want) {
			t.Fatalf("css lacks %q", want)
		}
	}
	if !strings.Contains(skript, "querySelector('[data-du]')") {
		t.Fatal("the script does not bring Du into view")
	}
}

// Bad data: a cycle (Xaver is Yvonne's parent, Yvonne Zora's, Zora
// Xaver's) cannot be drawn; one line breaks, is listed as a question and
// its edge page says why. A step-family: Lena with a third parent, Petra,
// gets three lines. Someone who would be two generations at once
// (Moritz, Hedwig's partner, Lena's parent) breaks a line rather than
// bend the rows.
func TestStammbaumUnordnung(t *testing.T) {
	am := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	n := baumNetz(map[string]antwort{
		paarKey("h/xaver", "h/uwe"):     {X: "h/xaver", Art: "geschwister", Am: am},
		paarKey("h/xaver", "h/yvonne"):  {X: "h/xaver", Art: "eltern", Am: am},
		paarKey("h/yvonne", "h/zora"):   {X: "h/yvonne", Art: "eltern", Am: am},
		paarKey("h/zora", "h/xaver"):    {X: "h/zora", Art: "eltern", Am: am},
		paarKey("h/petra", "h/lena"):    {X: "h/petra", Art: "eltern", Am: am},
		paarKey("h/moritz", "h/hedwig"): {X: "h/moritz", Art: "partner", Am: am},
		paarKey("h/moritz", "h/lena"):   {X: "h/moritz", Art: "eltern", Am: am},
	})
	sb := n.Stammbaum()
	kreis := 0
	for _, l := range sb.Brueche {
		if strings.Contains("h/xaver h/yvonne h/zora", l.A) && strings.Contains("h/xaver h/yvonne h/zora", l.B) {
			kreis++
		}
	}
	if kreis != 1 || !slices.ContainsFunc(sb.Brueche, func(l baumLinie) bool { return strings.Contains(l.A+l.B, "h/moritz") }) {
		t.Fatalf("breaks: %+v", sb.Brueche)
	}
	p := baumPos(sb)
	for _, l := range sb.Linien {
		soll := map[bool]int{true: 1, false: 0}[l.Art == "eltern"]
		if p[l.B].Gen-p[l.A].Gen != soll {
			t.Fatalf("a drawn line across the rows: %+v", l)
		}
	}
	eltern := 0
	for _, l := range sb.Linien {
		if l.Art == "eltern" && l.B == "h/lena" {
			eltern++
		}
	}
	if eltern < 3 {
		t.Fatalf("Lena's parents: %+v", sb.Linien)
	}
	var b strings.Builder
	stammbaumTab(&b, n)
	if !strings.Contains(b.String(), "<h2>Passt nicht in den Stammbaum</h2>") || !strings.Contains(b.String(), "&amp;pruefen=1") {
		t.Fatalf("the breaks are not asked:\n%s", b.String())
	}
}

// A dashed line's edge page has the derivation pre-chosen: one tap on the
// marked chip confirms it, and the line is solid.
func TestStammbaumBestaetigen(t *testing.T) {
	wt, _, _, _, _ := kanteBox(t)
	wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"kind"}, "zurueck": {"/tresor"}}))
	baum := wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String()
	if !strings.Contains(baum, `<a href="/tresor/kante?a=p-ben&amp;b=p-kim" aria-label="Ben Berg und Kim Berg: Mutter/Vater und Kind (abgeleitet, noch nicht bestätigt)">`) {
		t.Fatalf("no dashed line from Ben to Kim:\n%s", baum)
	}
	page := wt.rufe("GET", kanteAdresse("p-ben", "p-kim"), anna, nil).Body.String()
	if !strings.Contains(page, `<button name=art value=eltern class="chip vorschlag"`) || !strings.Contains(page, "Abgeleitet: „Mutter/Vater“, weil du und Ben Berg Partner seid und Kim Berg dein Kind ist.") ||
		!strings.Contains(page, "Ben Berg ist für Kim Berg …") {
		t.Fatalf("the edge page of a dashed line:\n%s", page)
	}
	wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-ben"}, "y": {"p-kim"}, "art": {"eltern"}, "zurueck": {kanteAdresse("p-ben", "p-kim")}}))
	baum = wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String()
	if !strings.Contains(baum, `aria-label="Ben Berg und Kim Berg: Mutter/Vater und Kind">`) {
		t.Fatalf("still dashed:\n%s", baum)
	}
	if page := wt.rufe("GET", kanteAdresse("p-ben", "p-kim")+"&pruefen=1", anna, nil).Body.String(); !strings.Contains(page, esc(saysKantePruefen)) {
		t.Fatal("pruefen says nothing")
	}
}

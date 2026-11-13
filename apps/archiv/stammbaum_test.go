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

// The SVG: every person a link to its detail (≥ 44 px) with what she is
// of the person under her name; a stroke a link to the edge page of the
// line it stands for, with a wide hit stroke, dashed while one of its
// lines is derived; Lena's one drop stands for the line to each parent
// and names both (data-mehr); Uwe marked for the script to scroll to;
// the lines and who is who as lists too; sideways scroll only inside
// the box.
func TestStammbaumSVG(t *testing.T) {
	n := baumNetz(nil)
	var b strings.Builder
	stammbaumTab(&b, n)
	page := b.String()
	for _, want := range []string{`<div id=baum-tab><div class=baum id=stammbaum><svg viewBox=`, `<a href="/tresor/umfeld?mitte=h%2Fuwe" class=du data-du data-id="h/uwe" data-name="Du" aria-label="Uwe"><rect`,
		`width="96" height="58" rx="10"`,
		`<a href="/tresor/kante?a=h%2Fuwe&amp;b=h%2Flena" aria-label="Du und Lena: Mutter/Vater und Kind; Silke und Lena: Mutter/Vater und Kind (abgeleitet, noch nicht bestätigt)" data-mehr="/tresor/kante?a=h%2Fuwe&amp;b=h%2Flena`,
		"/tresor/kante?a=h%2Fsilke&amp;b=h%2Flena\tSilke und Lena: Mutter/Vater und Kind (abgeleitet, noch nicht bestätigt)\"><path class=treffer",
		`class="linie kind vermutet"`, `aria-label="Hedwig und Du: Mutter/Vater und Kind"><path class=treffer d="M`, `class="linie kind" d="M`, `<path class="linie stamm" d="M`,
		`class="linie partner"`, `class="linie expartner"`, `aria-label="Bruno und Du: Geschwister"`, "Alle Linien als Liste (6)", "Silke ist Mutter/Vater · abgeleitet, noch nicht bestätigt",
		`<text x="296.0" y="229.0" class="k wer">Partner/in</text>`, `<summary>Wer ist wer für dich (4)</summary>`, `id=baum-ganz>Ganzen Baum zeigen</button>`} {
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
	if !strings.Contains(baum, "/tresor/kante?a=p-ben&amp;b=p-kim\tBen Berg und Kim Berg: Mutter/Vater und Kind (abgeleitet, noch nicht bestätigt)\"><path class=treffer") || !strings.Contains(baum, `class="linie kind vermutet"`) {
		t.Fatalf("no dashed line from Ben to Kim:\n%s", baum)
	}
	page := wt.rufe("GET", kanteAdresse("p-ben", "p-kim"), anna, nil).Body.String()
	if !strings.Contains(page, `<button name=art value=eltern class="chip vorschlag"`) || !strings.Contains(page, "Abgeleitet: „Mutter/Vater“, weil du und Ben Berg Partner seid und Kim Berg dein Kind ist.") ||
		!strings.Contains(page, "Ben Berg ist für Kim Berg …") {
		t.Fatalf("the edge page of a dashed line:\n%s", page)
	}
	wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-ben"}, "y": {"p-kim"}, "art": {"eltern"}, "zurueck": {kanteAdresse("p-ben", "p-kim")}}))
	baum = wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String()
	if !strings.Contains(baum, "Ben Berg und Kim Berg: Mutter/Vater und Kind\"") || strings.Contains(baum, `class="linie kind vermutet"`) {
		t.Fatalf("still dashed:\n%s", baum)
	}
	if page := wt.rufe("GET", kanteAdresse("p-ben", "p-kim")+"&pruefen=1", anna, nil).Body.String(); !strings.Contains(page, esc(saysKantePruefen)) {
		t.Fatal("pruefen says nothing")
	}
}

// An aunt whose side is known (owner 2026-11-11, „Wenn Hedwig meine Tante
// ist, wird das nicht im Stammbaum angezeigt.“): Yvonne, Uwe's aunt and
// his mother Hedwig's sister, stands in Hedwig's row, by the sibling
// line; nothing to ask, no side row.
func TestStammbaumTanteBekannt(t *testing.T) {
	am := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	sb := baumNetz(map[string]antwort{
		paarKey("h/yvonne", "h/uwe"):    {X: "h/yvonne", Art: "onkel", Am: am},
		paarKey("h/yvonne", "h/hedwig"): {X: "h/yvonne", Art: "geschwister", Am: am},
	}).Stammbaum()
	p := baumPos(sb)
	if p["h/yvonne"].ID == "" || p["h/yvonne"].Gen != p["h/hedwig"].Gen || !baumHat(sb, "h/hedwig", "h/yvonne", "geschwister", true) {
		t.Fatalf("Yvonne: %+v %+v", p["h/yvonne"], sb.Linien)
	}
	if len(sb.Fragen) != 0 || len(sb.Seiten) != 0 || len(sb.Brueche) != 0 || len(sb.Weitere) != 1 {
		t.Fatalf("asked %+v, side %+v, breaks %+v", sb.Fragen, sb.Seiten, sb.Brueche)
	}
}

// An aunt whose side is not known stands in the parents' row on a side
// row „Tanten und Onkel“, asked once: „Zora ist Schwester oder Bruder von
// …“ with the parents; a grandmother the same, a row higher; the answer
// places her and nothing is asked any more.
func TestStammbaumTanteUnbekannt(t *testing.T) {
	am := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	mehr := map[string]antwort{
		paarKey("h/zora", "h/uwe"):  {X: "h/zora", Art: "onkel", Am: am},
		paarKey("h/xaver", "h/uwe"): {X: "h/uwe", Art: "enkel", Am: am}, // said from Uwe's side: he is Xaver's grandchild
	}
	sb := baumNetz(mehr).Stammbaum()
	p := baumPos(sb)
	if p["h/zora"].Gen != p["h/hedwig"].Gen || p["h/xaver"].Gen != p["h/hedwig"].Gen-1 || p["h/zora"].X <= p["h/hedwig"].X {
		t.Fatalf("places: %+v", sb.Personen)
	}
	want := []BaumFrage{{X: "h/xaver", Y: "h/uwe", Art: "grosseltern", Wahl: []baumWahl{{"h/hedwig", "eltern"}}},
		{X: "h/zora", Y: "h/uwe", Art: "onkel", Wahl: []baumWahl{{"h/hedwig", "geschwister"}}}}
	if !slices.EqualFunc(sb.Fragen, want, func(a, b BaumFrage) bool {
		return a.X == b.X && a.Y == b.Y && a.Art == b.Art && slices.Equal(a.Wahl, b.Wahl)
	}) {
		t.Fatalf("questions: %+v", sb.Fragen)
	}
	if !slices.ContainsFunc(sb.Seiten, func(s baumSeite) bool { return s.Text == "Tanten und Onkel" }) || !slices.ContainsFunc(sb.Seiten, func(s baumSeite) bool { return s.Text == "Großeltern" }) {
		t.Fatalf("side rows: %+v", sb.Seiten)
	}
	var b strings.Builder
	stammbaumTab(&b, baumNetz(mehr))
	for _, s := range []string{"<h2>Noch ohne Platz im Stammbaum</h2>", "Zora ist Schwester oder Bruder von …", `<input type=hidden name=festlegen value=1>`,
		`<button name=art value=geschwister class="chip" data-laeuft="Hedwig …">Hedwig</button>`, "weiß nicht", `class="k seite">Tanten und Onkel</text>`, "Xaver ist Mutter oder Vater von …"} {
		if !strings.Contains(b.String(), s) {
			t.Fatalf("%q missing:\n%s", s, b.String())
		}
	}
	mehr[paarKey("h/zora", "h/hedwig")] = antwort{X: "h/zora", Art: "geschwister", Am: am}
	mehr[paarKey("h/xaver", "h/hedwig")] = antwort{X: "h/xaver", Art: "eltern", Am: am}
	sb = baumNetz(mehr).Stammbaum()
	if p := baumPos(sb); len(sb.Fragen) != 0 || len(sb.Seiten) != 0 || p["h/zora"].Gen != p["h/hedwig"].Gen || !baumHat(sb, "h/xaver", "h/hedwig", "eltern", true) {
		t.Fatalf("placed: %+v %+v", sb.Fragen, sb.Personen)
	}
}

// Nothing dropped: a relative of each kind a step away is in the tree,
// at its generation, with the question that would place it, or what is
// missing for one, or (Sonstige verwandt) the edge page to say how.
func TestStammbaumNichtsFaellt(t *testing.T) {
	am := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	for _, art := range baumWeitere {
		sb := baumNetz(map[string]antwort{paarKey("h/max", "h/uwe"): {X: "h/max", Art: art, Am: am}}).Stammbaum()
		p := baumPos(sb)
		if p["h/max"].ID == "" || p["h/uwe"].Gen-p["h/max"].Gen != baumStufe[art] {
			t.Fatalf("%s: Max %+v, Uwe %+v", art, p["h/max"], p["h/uwe"])
		}
		i := slices.IndexFunc(sb.Fragen, func(f BaumFrage) bool { return f.X == "h/max" })
		if i < 0 || len(sb.Fragen[i].Wahl) == 0 && sb.Fragen[i].Fehlt == "" && art != "verwandt" {
			t.Fatalf("%s: not asked: %+v", art, sb.Fragen)
		}
		var b strings.Builder
		stammbaumTab(&b, baumNetz(map[string]antwort{paarKey("h/max", "h/uwe"): {X: "h/max", Art: art, Am: am}}))
		if !strings.Contains(b.String(), "<b>Max</b> <span class=k>"+kurzWort(art)+" von dir</span>") {
			t.Fatalf("%s: no card:\n%s", art, b.String())
		}
	}
}

// The whole way on the page: Hedwig, anna's aunt, with her parents Rosi
// and Karl: one tap on Rosi places her (and Homes is asked to write the
// sibling line); then her son Lukas, anna's cousin, is asked as her
// child; „weiß nicht“ for Erna keeps her on the side row, not asked
// again, also after the vault is read anew.
func TestStammbaumEinordnen(t *testing.T) {
	wt, ut, _, innen, _ := kanteBox(t)
	schauFamilie(t, ut, innen)
	baum := wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String()
	for _, s := range []string{"Hedwig Sommer ist Schwester oder Bruder von …", ">Rosi Berg</button>", ">Karl Berg</button>", "Erna Lang ist Mutter oder Vater von …",
		"Lukas Sommer ist Kind von …", ">Hedwig Sommer</button>", "Wie genau ist Otto Lang verwandt?", `aria-label="Otto Lang"><rect`} {
		if !strings.Contains(baum, s) {
			t.Fatalf("%q missing:\n%s", s, baum)
		}
	}
	ziel, baum := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-hedwig"}, "y": {"p-rosi"}, "art": {"geschwister"}, "festlegen": {"1"}, "zurueck": {"/tresor?tab=stammbaum"}}))
	if !strings.Contains(ziel, "tab=stammbaum") || strings.Contains(baum, "Hedwig Sommer ist Schwester") || !strings.Contains(baum, `aria-label="Hedwig Sommer und Rosi Berg: Geschwister"`) {
		t.Fatalf("Hedwig not placed (%s):\n%s", ziel, baum)
	}
	wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-lukas"}, "y": {"p-hedwig"}, "art": {"kind"}, "festlegen": {"1"}, "zurueck": {"/tresor?tab=stammbaum"}}))
	_, baum = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {paarKey("p-erna", "p-anna")}, "tu": {"weissnicht"}, "zurueck": {"/tresor?tab=stammbaum"}}))
	if strings.Contains(baum, "Lukas Sommer ist Kind") || !strings.Contains(baum, `aria-label="Hedwig Sommer und Lukas Sommer: Mutter/Vater und Kind"`) ||
		strings.Contains(baum, "Erna Lang ist Mutter oder Vater") || !strings.Contains(baum, "Wo genau, weißt du nicht.") || !strings.Contains(baum, `aria-label="Erna Lang"><rect`) {
		t.Fatalf("after the taps:\n%s", baum)
	}
	ut.u.laden(innen)
	if baum = wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String(); !strings.Contains(baum, "Wo genau, weißt du nicht.") {
		t.Fatal("„weiß nicht“ is not kept")
	}
}

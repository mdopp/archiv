package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// sippeNetz is Mara Lindner's family (s/mara is the person), every line a
// plain one: her parents Jutta and Rolf, Rolf's parents Greta and Emil,
// Emil's brother Kurt, Rolf's sister Heidi with her husband Uli, their
// son Jonas and his partner Lea; Jutta's mother Ilse; Mara's brother Tim
// with Nele and their son Paul; Mara's partner Sven, their children Mia
// and Ben (Sven is Ben's father by her answer, Mia's only by derivation),
// Sven's parents Karin and Dieter and his sister Pia. Hanna Vogel is an
// aunt whose side nobody said. The Winters and three others are in the
// phone book and no relatives.
func sippeNetz(mehr map[string]antwort) *Netz {
	st := netzTabelle{Namen: map[string]string{}, Ich: "s/mara"}
	for _, name := range []string{"Mara Lindner", "Jutta Lindner", "Rolf Lindner", "Greta Lindner", "Emil Lindner", "Kurt Lindner", "Heidi Brandt", "Uli Brandt",
		"Jonas Brandt", "Lea Roth", "Ilse Vogel", "Tim Lindner", "Nele Kraus", "Paul Lindner", "Sven Albers", "Mia Albers", "Ben Albers", "Karin Albers",
		"Dieter Albers", "Pia Albers", "Hanna Vogel", "Olga Winter", "Piet Winter", "Ruth Winter", "Anton Zeller", "Bea Engel", "Cem Yilmaz"} {
		st.Kontakte = append(st.Kontakte, netzKontakt{Key: "s/" + strings.ToLower(strings.Fields(name)[0]), Name: name})
	}
	am := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	as := map[string]antwort{}
	for _, l := range [][3]string{{"jutta", "eltern", "mara"}, {"rolf", "eltern", "mara"}, {"jutta", "partner", "rolf"}, {"greta", "eltern", "rolf"}, {"emil", "eltern", "rolf"},
		{"greta", "partner", "emil"}, {"kurt", "geschwister", "emil"}, {"heidi", "geschwister", "rolf"}, {"uli", "partner", "heidi"}, {"jonas", "kind", "heidi"},
		{"jonas", "kind", "uli"}, {"lea", "partner", "jonas"}, {"ilse", "eltern", "jutta"}, {"tim", "kind", "jutta"}, {"tim", "kind", "rolf"}, {"nele", "partner", "tim"},
		{"paul", "kind", "tim"}, {"paul", "kind", "nele"}, {"sven", "partner", "mara"}, {"mia", "kind", "mara"}, {"ben", "kind", "mara"}, {"ben", "kind", "sven"},
		{"karin", "eltern", "sven"}, {"dieter", "eltern", "sven"}, {"karin", "partner", "dieter"}, {"pia", "geschwister", "sven"}, {"hanna", "onkel", "mara"}} {
		as[paarKey("s/"+l[0], "s/"+l[2])] = antwort{X: "s/" + l[0], Art: l[1], Am: am}
	}
	for k, a := range mehr {
		as[k] = a
	}
	return neuesNetz(st, nil, as, time.UTC, func() time.Time { return am })
}

// TestVorschau writes the Stammbaum tab of sippeNetz, with the page's
// stylesheet and script inline, to the file ARCHIV_VORSCHAU names: to
// look at before it goes live. „Bearbeiten“ and the sheet of a place work
// as on the page; the post is answered by the page itself (nothing is
// saved, no link leads anywhere). Without the variable it does nothing.
func TestVorschau(t *testing.T) {
	ziel := os.Getenv("ARCHIV_VORSCHAU")
	if ziel == "" {
		t.Skip("ARCHIV_VORSCHAU is not set")
	}
	var b strings.Builder
	stammbaumTab(&b, sippeNetz(nil))
	const probe = `<script>
window.fetch=function(){return Promise.resolve({json:function(){return Promise.resolve({satz:'Vorschau: Hier wird nichts gespeichert. Im Archiv steht der Name jetzt an diesem Platz, und der nächste Platz ist frei.'})}})};
document.addEventListener('click',function(e){var a=e.target.closest&&e.target.closest('a[href^="/tresor"]');if(a&&!e.defaultPrevented){e.preventDefault();var h=document.getElementById('hinweis');h.textContent='Vorschau: „'+(a.getAttribute('aria-label')||a.textContent)+'“ öffnet im Archiv die Seite dazu.'}});
</script>`
	page := `<!doctype html><html lang=de><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><meta name=color-scheme content="dark light"><title>Stammbaum (Vorschau)</title><style>` +
		css + bauCSS + `</style>` + skript + bauSkript + probe + `</head><body><main><h1>Stammbaum</h1><p class=hinweis id=hinweis>Vorschau mit erfundenen Namen. Nichts wird gespeichert.</p>` + b.String() + `</main></body></html>`
	if err := os.WriteFile(ziel, []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
}

// strichFehler checks the laid-out tree as data and says what is wrong:
// a box outside the picture or on another, two strokes on each other, a
// stroke through a box, a child with more or less than one drop.
func strichFehler(sb Stammbaum) (out []string) {
	drin := func(a, b, x float64) bool { return x > min(a, b)+0.5 && x < max(a, b)-0.5 }
	for i, p := range sb.Personen {
		if p.X < 0 || p.Y < 0 || p.X+baumBreite > sb.Breite || p.Y+baumHoehe > sb.Hoehe {
			out = append(out, fmt.Sprintf("%s stands outside %vx%v: %v,%v", p.Name, sb.Breite, sb.Hoehe, p.X, p.Y))
		}
		for _, q := range sb.Personen[i+1:] {
			if p.Gen == q.Gen && math.Abs(p.X-q.X) < baumSpalte-0.5 {
				out = append(out, p.Name+" and "+q.Name+" stand on each other")
			}
		}
	}
	type stueck struct {
		s int
		g [4]float64
	}
	var alle []stueck
	for i, s := range sb.Striche {
		for _, g := range s.Seg {
			if g[0] != g[2] && g[1] != g[3] {
				out = append(out, fmt.Sprintf("a slanted piece: %v", g))
			}
			alle = append(alle, stueck{i, g})
			for _, p := range sb.Personen {
				quer := g[1] == g[3] && drin(p.Y, p.Y+baumHoehe, g[1]) && min(max(g[0], g[2]), p.X+baumBreite)-max(min(g[0], g[2]), p.X) > 0.5
				hoch := g[0] == g[2] && drin(p.X, p.X+baumBreite, g[0]) && min(max(g[1], g[3]), p.Y+baumHoehe)-max(min(g[1], g[3]), p.Y) > 0.5
				if quer || hoch {
					out = append(out, fmt.Sprintf("a %s stroke runs through %s: %v", sb.Striche[i].Art, p.Name, g))
				}
			}
		}
	}
	for i, a := range alle {
		for _, b := range alle[i+1:] {
			achse := 0 // both upright: the same column; both level: the same height
			if a.g[1] == a.g[3] && b.g[1] == b.g[3] {
				achse = 1
			} else if a.g[0] != a.g[2] || b.g[0] != b.g[2] {
				continue
			}
			lang := 1 - achse
			if math.Abs(a.g[achse]-b.g[achse]) < 3 && min(max(a.g[lang], a.g[lang+2]), max(b.g[lang], b.g[lang+2]))-max(min(a.g[lang], a.g[lang+2]), min(b.g[lang], b.g[lang+2])) > 0.5 {
				out = append(out, fmt.Sprintf("two strokes on each other: %s %v and %s %v", sb.Striche[a.s].Art, a.g, sb.Striche[b.s].Art, b.g))
			}
		}
	}
	tropfen := map[string]int{}
	for _, s := range sb.Striche {
		if s.Art == "kind" {
			for _, p := range sb.Personen {
				if g := s.Seg[0]; g[3] == p.Y && g[0] > p.X && g[0] < p.X+baumBreite {
					tropfen[p.ID]++
				}
			}
		}
	}
	for _, l := range sb.Linien {
		if l.Art == "eltern" && tropfen[l.B] != 1 {
			out = append(out, fmt.Sprintf("%s hangs from %d drops", l.B, tropfen[l.B]))
		}
	}
	return out
}

// kreuzungen counts where a level piece of one stroke crosses an upright
// piece of another.
func kreuzungen(sb Stammbaum) (n int) {
	for i, a := range sb.Striche {
		for j, b := range sb.Striche {
			for _, w := range a.Seg {
				for _, h := range b.Seg {
					if i != j && w[1] == w[3] && h[0] == h[2] && h[0] > min(w[0], w[2])+0.5 && h[0] < max(w[0], w[2])-0.5 && w[1] > min(h[1], h[3])+0.5 && w[1] < max(h[1], h[3])-0.5 {
						n++
					}
				}
			}
		}
	}
	return n
}

// The picture as data (owner 2026-11-12: doubled lines, a descent that
// does not come from the couple, a person cut off): in Mara's family of
// twenty nobody stands outside the picture or on another, no two strokes
// lie on each other, none runs through a box, none crosses another; each
// child hangs from one drop that stands for the line to each parent; a
// couple is one line, and their children's line starts in its middle
// and ends in a bar over them. The same holds for families that are not
// tidy: two partners, a third parent, half-siblings, siblings without
// parents, a cycle.
func TestStammbaumStriche(t *testing.T) {
	sb := sippeNetz(nil).Stammbaum()
	if f := strichFehler(sb); len(f) > 0 || kreuzungen(sb) != 0 || len(sb.Personen) != 21 || len(sb.Brueche) != 0 {
		t.Fatalf("%d people, %d crossings, %v", len(sb.Personen), kreuzungen(sb), f)
	}
	// the check does see what it is for: a stroke drawn twice, a box moved out, a line through a box
	krumm := sb
	krumm.Striche = append(slices.Clone(sb.Striche), sb.Striche[len(sb.Striche)-1], baumStrich{Art: "kind", Seg: [][4]float64{{sb.Personen[0].X + 10, 0, sb.Personen[0].X + 10, sb.Hoehe}}})
	krumm.Breite -= baumBreite
	if f := strings.Join(strichFehler(krumm), "\n"); !strings.Contains(f, "two strokes on each other") || !strings.Contains(f, "stands outside") || !strings.Contains(f, "runs through") {
		t.Fatalf("the check is blind: %s", f)
	}
	p := baumPos(sb)
	paar, staemme := 0, 0
	jutta, rolf := p["s/jutta"], p["s/rolf"]
	mitte := (jutta.X + rolf.X + baumBreite) / 2
	for _, s := range sb.Striche {
		g := s.Seg[0]
		switch {
		case s.Art == "partner" && len(s.Fuer) == 1 && paarKey(s.Fuer[0].A, s.Fuer[0].B) == paarKey("s/jutta", "s/rolf"):
			paar++
			if len(s.Seg) != 1 || g[1] != jutta.Y+baumHoehe/2 || g[3] != g[1] || math.Abs(g[2]-g[0]-(baumSpalte-baumBreite)) > 0.1 {
				t.Fatalf("the couple's line: %v", s.Seg)
			}
		case s.Art == "stamm" && math.Abs(g[0]-mitte) < 0.1 && g[1] == jutta.Y+baumHoehe/2:
			staemme++
			bar := s.Seg[len(s.Seg)-1]
			if len(s.Seg) != 2 || g[3] != bar[1] || bar[1] != bar[3] || math.Abs(bar[0]-(p["s/tim"].X+baumBreite/2)) > 0.1 || math.Abs(bar[2]-(p["s/mara"].X+baumBreite/2)) > 0.1 || bar[1] >= p["s/mara"].Y {
				t.Fatalf("the line down and the bar: %v, Tim %v, Mara %v", s.Seg, p["s/tim"].X, p["s/mara"].X)
			}
		case s.Art == "kind" && g[3] == p["s/mara"].Y && g[0] == p["s/mara"].X+baumBreite/2:
			if len(s.Fuer) != 2 || !s.Fest || s.Fuer[0].B != "s/mara" || s.Fuer[1].B != "s/mara" {
				t.Fatalf("Mara's drop: %+v", s)
			}
		case s.Art == "kind" && g[3] == p["s/mia"].Y && g[0] == p["s/mia"].X+baumBreite/2:
			if len(s.Fuer) != 2 || s.Fest { // Sven is her father only by derivation: dashed
				t.Fatalf("Mia's drop: %+v", s)
			}
		}
	}
	if paar != 1 || staemme != 1 || math.Abs(jutta.X-rolf.X) != baumSpalte || p["s/mara"].Gen != p["s/sven"].Gen || math.Abs(p["s/mara"].X-p["s/sven"].X) != baumSpalte {
		t.Fatalf("%d couple lines, %d lines down, Jutta %v, Rolf %v", paar, staemme, jutta.X, rolf.X)
	}
	// siblings side by side, each with the one who married in outside
	if math.Abs(p["s/tim"].X-p["s/mara"].X) > baumSpalte+baumLuft+0.1 || math.Abs(p["s/heidi"].X-p["s/uli"].X) != baumSpalte {
		t.Fatalf("Tim %v, Mara %v", p["s/tim"].X, p["s/mara"].X)
	}
	gezeichnet := map[string]bool{}
	for _, s := range sb.Striche {
		for _, l := range s.Fuer {
			gezeichnet[paarKey(l.A, l.B)] = true
		}
	}
	for _, l := range sb.Linien { // every line is a stroke, but the siblings a shared bar already shows
		if !gezeichnet[paarKey(l.A, l.B)] && l.Art != "geschwister" {
			t.Fatalf("no stroke stands for %+v", l)
		}
	}
	am := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for name, n := range map[string]*Netz{
		"two partners, a third parent, a cycle": baumNetz(map[string]antwort{
			paarKey("h/xaver", "h/uwe"): {X: "h/xaver", Art: "geschwister", Am: am}, paarKey("h/xaver", "h/yvonne"): {X: "h/xaver", Art: "eltern", Am: am},
			paarKey("h/yvonne", "h/zora"): {X: "h/yvonne", Art: "eltern", Am: am}, paarKey("h/zora", "h/xaver"): {X: "h/zora", Art: "eltern", Am: am},
			paarKey("h/petra", "h/lena"): {X: "h/petra", Art: "eltern", Am: am}, paarKey("h/moritz", "h/hedwig"): {X: "h/moritz", Art: "partner", Am: am}}),
		"half-siblings and siblings without parents": sippeNetz(map[string]antwort{
			paarKey("s/olga", "s/ruth"): {X: "s/olga", Art: "geschwister", Am: am}, paarKey("s/piet", "s/ruth"): {X: "s/piet", Art: "geschwister", Am: am},
			paarKey("s/anton", "s/mia"): {X: "s/anton", Art: "eltern", Am: am}, paarKey("s/bea", "s/anton"): {X: "s/bea", Art: "kind", Am: am},
			paarKey("s/bea", "s/mia"): {X: "s/bea", Art: "geschwister", Am: am}, paarKey("s/cem", "s/rolf"): {X: "s/cem", Art: "expartner", Am: am},
			paarKey("s/olga", "s/cem"): {X: "s/olga", Art: "kind", Am: am}, paarKey("s/olga", "s/rolf"): {X: "s/olga", Art: "kind", Am: am}}),
	} {
		sb := n.Stammbaum()
		if f := strichFehler(sb); len(f) > 0 || len(sb.Striche) == 0 {
			t.Errorf("%s: %v", name, f)
		}
	}
	for _, want := range []string{".baum{max-width:100%;overflow-x:auto", "#baum-tab.ganz .baum svg{width:100%;height:auto}", "html:not(.skript) .baum-kopf"} {
		if !strings.Contains(css+bauCSS, want) {
			t.Fatalf("the stylesheet lacks %q", want)
		}
	}
}

// What German has a word for, by the way from the one to the other
// (verwandtWort): each way of the table, built as a chain of plain lines
// and nothing else, is named so. A way without a word is described from
// the nearest named one; a parent's partner is never a step-parent by a
// guess; a way nothing names, and no way at all, say nothing.
func TestVerwandtTabelle(t *testing.T) {
	am := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	kette := func(weg string) *Netz {
		st := netzTabelle{Namen: map[string]string{}, Ich: "k/p0"}
		as := map[string]antwort{}
		for i := 0; i <= len(weg); i++ {
			st.Kontakte = append(st.Kontakte, netzKontakt{Key: fmt.Sprintf("k/p%d", i), Name: fmt.Sprintf("Person%d", i)})
		}
		for i, s := range weg {
			a, b := fmt.Sprintf("k/p%d", i), fmt.Sprintf("k/p%d", i+1)
			as[paarKey(a, b)] = antwort{X: b, Art: map[rune]string{'E': "eltern", 'K': "kind", 'P': "partner", 'G': "geschwister"}[s], Am: am}
		}
		return neuesNetz(st, nil, as, time.UTC, func() time.Time { return am })
	}
	for weg, wort := range verwandtWort {
		if got := kette(weg).Verwandt(fmt.Sprintf("k/p%d", len(weg)), "k/p0"); got != wort {
			t.Errorf("%s: %q, not %q", weg, got, wort)
		}
	}
	for weg, satz := range map[string]string{
		"EGKP":    "Partner/in von Person3 (Cousin/e)", // the owner's „Partnerin deines Cousins“
		"EP":      "Partner/in von Person1 (Mutter/Vater)",
		"PEE":     "Mutter/Vater von Person2 (Schwiegereltern)",
		"GKP":     "Partner/in von Person2 (Neffe/Nichte)",
		"EEGKKK":  "Kind von Person5 (Cousin/e 2. Grades)",
		"EEEEEE":  "Großeltern von Person4 (Ururgroßeltern)",
		"EEEEEEE": "", // seven steps: too far
	} {
		if got := kette(weg).Verwandt(fmt.Sprintf("k/p%d", len(weg)), "k/p0"); got != satz {
			t.Errorf("%s: %q, not %q", weg, got, satz)
		}
	}
	n := sippeNetz(nil)
	for id, wort := range map[string]string{"kurt": "Großonkel/Großtante", "uli": "Onkel/Tante (angeheiratet)", "nele": "Schwager/Schwägerin", "pia": "Schwager/Schwägerin",
		"paul": "Neffe/Nichte", "karin": "Schwiegereltern", "jonas": "Cousin/e", "lea": "Partner/in von Jonas Brandt (Cousin/e)", "ilse": "Großeltern", "tim": "Geschwister",
		"olga": "", "hanna": ""} { // Hanna is an aunt by her answer only: no line places her yet
		if got := n.Verwandt("s/"+id, "s/mara"); got != wort {
			t.Errorf("%s: %q, not %q", id, got, wort)
		}
	}
	if got := n.Verwandt("s/mara", "s/kurt"); got != "Großneffe/Großnichte" {
		t.Errorf("Mara for Kurt: %q", got)
	}
}

// The net takes the derived relatives in without a question (owner
// 2026-11-12: „können auch implizit ins Netz aufnehmen“): Kurt, the
// great-uncle nobody named, and Lea, the cousin's partner, are ties of
// Mara's with what they are as the reason; nobody is asked what they are
// to her. Hanna, an aunt no line places, is still asked about (with
// Jutta: is she her sister?), and what the person said outranks the
// lines: Nele is „Kollege/Kollegin“ by her answer, not a derived tie.
func TestVerwandtImNetz(t *testing.T) {
	am := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	n := sippeNetz(map[string]antwort{paarKey("s/nele", "s/mara"): {X: "s/nele", Art: "kollege", Am: am}})
	kurz := map[string]string{}
	for _, v := range n.Nahe("s/mara", 100) {
		kurz[v.Mit.ID] = n.Kurz("s/mara", v.Kante)
	}
	if kurz["s/kurt"] != "Großonkel/Großtante (aus dem Stammbaum)" || kurz["s/lea"] != "Partner/in von Jonas Brandt (Cousin/e) (aus dem Stammbaum)" ||
		kurz["s/nele"] != "Kollege/Kollegin" || kurz["s/olga"] != "" {
		t.Fatalf("Mara's ties: %v", kurz)
	}
	gefragt := map[string]bool{}
	for _, f := range n.Fragen() {
		gefragt[f.key()] = true
	}
	for _, id := range []string{"kurt", "lea", "paul", "uli"} {
		if gefragt[paarKey("s/"+id, "s/mara")] {
			t.Errorf("asked what %s is to Mara", id)
		}
	}
	if gefragt[paarKey("s/kurt", "s/jonas")] || !gefragt[paarKey("s/hanna", "s/jutta")] {
		t.Fatalf("asked: %v", slices.Sorted(maps.Keys(gefragt)))
	}
}

// bauPost is the editor's post as the page's script sends it.
func bauPost(t *testing.T, wt *webTest, form url.Values, code int) bauAntwort {
	t.Helper()
	rec := wt.rufe("POST", verbindenPfad, anna, form)
	var a bauAntwort
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || rec.Code != code || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%v: %d, not %d: %v\n%s", form, rec.Code, code, err, rec.Body)
	}
	return a
}

// The editor's post (owner 2026-11-12, -13): a name at a place is one
// post in the background. It keeps the line in the vault as the edge
// page does, Homes writes it, and the answer is the sentence (with what
// the lines now make her of the person), the tab anew and what takes it
// back; Rückgängig is the same post and leaves nothing. A name nobody
// has becomes a person by hand and Rückgängig takes her back too. Only
// the four plain kinds, one person a post; the vault must be open, the
// person signed in, the post from this origin. The old editor is gone
// from the page.
func TestStammbaumVerbinden(t *testing.T) {
	wt, ut, homes, innen, _ := kanteBox(t)
	vorher := len(antwortenLaden(innen))
	page := wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String()
	for _, want := range []string{`<button type=button class=voll id=baum-modus>Bearbeiten</button>`, `<div class=baum id=baum-frei>`, `<div id=bau-namen hidden data-namen="[`,
		`[&#34;p-kim&#34;,&#34;Kim Berg&#34;]`, "fetch('" + verbindenPfad + "',{method:'POST'", "credentials:'same-origin'", "'Rückgängig'", "'Fertig'", `<details class=karte id=dazu><summary>+ Beziehung ergänzen</summary>`} {
		if !strings.Contains(page, want) {
			t.Fatalf("%q missing:\n%s", want, page)
		}
	}
	for _, weg := range []string{"Verbindungen bauen", "bau-suche", "bau-liste", "pointerdown", "Noch ohne Platz"} {
		if strings.Contains(page, weg) {
			t.Fatalf("%q is still on the page", weg)
		}
	}
	a := bauPost(t, wt, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"kind"}}, 200)
	if a.Satz != "Kim Berg ist Kind von dir." || a.Zurueck != "paar="+url.QueryEscape(paarKey("p-kim", "p-anna")) || a.Fehler != "" ||
		!strings.HasPrefix(a.HTML, `<p class=baum-kopf>`) || !strings.Contains(a.HTML, `data-id="p-kim" data-name="Kim Berg" aria-label="Kim Berg, Kind"`) || strings.Contains(a.HTML, "<main") {
		t.Fatalf("the answer: %+v", a)
	}
	as := antwortenLaden(innen)
	if got := as[paarKey("p-kim", "p-anna")]; got.X != "p-kim" || got.Art != "kind" || got.Eingetragen == nil || len(homes.genommen()) == 0 {
		t.Fatalf("not kept or not written by Homes: %+v, %d packets", got, len(homes.genommen()))
	}
	if b := bauPost(t, wt, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"kind"}}, 200); b.Satz != saysBauSchon || b.Zurueck != "" {
		t.Fatalf("the same again: %+v", b)
	}
	// Kim's own child, by name only: a person by hand, a grandchild by the lines
	b := bauPost(t, wt, url.Values{"neu": {"  Lotte   Berg "}, "y": {"p-kim"}, "art": {"kind"}}, 200)
	q, _ := url.ParseQuery(b.Zurueck)
	lotte := q.Get("neu")
	if p, ok := ut.u.Netz().Angelegt(lotte); !ok || p.Name != "Lotte Berg" || len(q["paar"]) != 1 || b.Satz != "Lotte Berg ist Kind von Kim Berg · Lotte Berg ist für dich: Enkel/in · Kim Berg ist für dich: Kind." ||
		!strings.Contains(b.HTML, `data-name="Lotte Berg" aria-label="Lotte Berg, Enkel/in"`) || len(angelegtLaden(innen)) != 1 {
		t.Fatalf("Lotte: %+v %+v", b, q)
	}
	q.Set("tu", "zurueck")
	if c := bauPost(t, wt, q, 200); c.Satz != saysBauZurueck || strings.Contains(c.HTML, "Lotte") || len(angelegtLaden(innen)) != 0 || len(antwortenLaden(innen)) != len(as) {
		t.Fatalf("Lotte is not taken back: %+v", c)
	}
	a2, _ := url.ParseQuery(a.Zurueck)
	a2.Set("tu", "zurueck")
	bauPost(t, wt, a2, 200)
	if got := antwortenLaden(innen); len(got) != vorher {
		t.Fatalf("after every Rückgängig: %d answers, not %d: %v", len(got), vorher, got)
	}
	for _, f := range []url.Values{{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"onkel"}}, {"y": {"p-anna"}, "art": {"kind"}}, {"x": {"p-kim"}, "art": {"kind"}},
		{"x": {"p-kim"}, "neu": {"Lotte Berg"}, "y": {"p-anna"}, "art": {"kind"}}, {"x": {"p-kim"}, "y": {"p-anna", ""}, "art": {"kind"}}} {
		if e := bauPost(t, wt, f, 400); e.Fehler != saysBauOhne || e.Satz != "" {
			t.Fatalf("%v: %+v", f, e)
		}
	}
	if e := bauPost(t, wt, url.Values{"x": {"p-niemand"}, "y": {"p-anna"}, "art": {"kind"}}, 409); e.Fehler != saysFrageWeg {
		t.Fatalf("nobody: %+v", e)
	}
	form := url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"kind"}}
	fremd := map[string]string{proxyHeader: "s3cret", "Remote-User": "anna", "Origin": "https://anderswo.test"}
	if rec := wt.rufe("POST", verbindenPfad, fremd, form); rec.Code != http.StatusForbidden {
		t.Fatalf("a post from another site: %d", rec.Code)
	}
	if rec := wt.rufe("POST", verbindenPfad, nil, form); rec.Code != http.StatusUnauthorized {
		t.Fatalf("not signed in: %d", rec.Code)
	}
	homes.kaputt = true
	bauPost(t, wt, form, 200) // kept in the vault; Homes will take it later
	homes.kaputt = false
	wt.tt.Abschliessen(t.Context(), "test")
	if e := bauPost(t, wt, form, http.StatusLocked); e.Fehler != saysZu || e.HTML != "" {
		t.Fatalf("closed: %+v", e)
	}
}

// What is kept does not change shape (archiv#55 kept an aunt, a
// grandmother, a cousin as the person's answers, and what followed from
// an answer marked „aus“): the answers file of before is read as it is,
// every answer still an answer and still in the tree, and neither reading
// it nor drawing the tree writes it. A line set in the editor adds its
// answer and what follows from it and leaves every older entry as it was.
func TestStammbaumAlteAntworten(t *testing.T) {
	wt, ut, _, innen, _ := kanteBox(t)
	schauFamilie(t, ut, innen)
	tab, _ := tabelleLaden(innen)
	tab.Namen["p-ole"] = "Ole Berg"
	jsonImTresor(innen.umfeldTabelle(), tab)
	ut.u.laden(innen)
	for _, f := range [][3]string{{"p-hedwig", "geschwister", "p-rosi"}, {"p-ole", "eltern", "p-karl"}} { // Ole: her grandfather follows, marked „aus“
		bauPost(t, wt, url.Values{"x": {f[0]}, "y": {f[2]}, "art": {f[1]}}, 200)
	}
	alt, err := os.ReadFile(innen.umfeldAntworten())
	if err != nil {
		t.Fatal(err)
	}
	var roh map[string]json.RawMessage
	if json.Unmarshal(alt, &roh) != nil || !strings.Contains(string(alt), `"art":"onkel"`) || !strings.Contains(string(alt), `"art":"cousin"`) || !strings.Contains(string(alt), `"art":"grosseltern"`) || !strings.Contains(string(alt), `"aus":`) {
		t.Fatalf("not the answers of before:\n%s", alt)
	}
	ut.u.laden(innen)
	baum := wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String()
	if jetzt, _ := os.ReadFile(innen.umfeldAntworten()); string(jetzt) != string(alt) {
		t.Fatalf("reading rewrote the answers:\n%s\n%s", alt, jetzt)
	}
	n := ut.u.Netz()
	if len(n.Gegebene()) != len(roh) {
		t.Fatalf("%d answers of %d", len(n.Gegebene()), len(roh))
	}
	for _, want := range []string{`aria-label="Hedwig Sommer, Onkel/Tante"`, `aria-label="Erna Lang"`, `aria-label="Lukas Sommer"`, `aria-label="Rosi Berg, Mutter/Vater"`} {
		if !strings.Contains(baum, want) {
			t.Fatalf("%q missing:\n%s", want, baum)
		}
	}
	bauPost(t, wt, url.Values{"x": {"p-lukas"}, "y": {"p-hedwig"}, "art": {"kind"}}, 200)
	var neu map[string]json.RawMessage
	jetzt, _ := os.ReadFile(innen.umfeldAntworten())
	if json.Unmarshal(jetzt, &neu) != nil || len(neu) < len(roh) {
		t.Fatalf("answers lost: %d of %d", len(neu), len(roh))
	}
	for key, a := range roh {
		if key != paarKey("p-lukas", "p-hedwig") && string(neu[key]) != string(a) {
			t.Fatalf("%s changed: %s, was %s", key, neu[key], a)
		}
	}
	if got := n.Verwandt("p-lukas", "p-anna"); got != "Cousin/e" {
		t.Fatalf("Lukas by the lines: %q", got)
	}
	// Ole's father, whom nobody named to anna: her page says what he is
	bauPost(t, wt, url.Values{"neu": {"Uropa Fritz"}, "y": {"p-ole"}, "art": {"eltern"}}, 200)
	fritz := ut.u.Netz().Finden("Uropa Fritz")
	if len(fritz) != 1 {
		t.Fatalf("Fritz: %v", fritz)
	}
	if page := wt.rufe("GET", umfeldMitte(fritz[0].ID), anna, nil).Body.String(); !strings.Contains(page, "Für dich: Urgroßeltern (aus dem Stammbaum)") {
		t.Fatalf("Fritz's page:\n%s", page)
	}
}

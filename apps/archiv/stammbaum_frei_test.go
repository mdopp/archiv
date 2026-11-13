package main

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// freiVon are the edit tree's empty places as the sheet says them, each
// with the relatives it offers first, sorted.
func freiVon(n *Netz) (out []string) {
	_, eb := n.Stammbaeume()
	for _, f := range eb.Frei {
		s := f.text(n)
		if len(f.Vor) > 0 {
			s += " ← " + strings.Join(f.Vor, ",")
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// alleinNetz is Mara alone (s/mara is the person) with three names in her
// phone book, and the answers given.
func alleinNetz(as map[string]antwort) *Netz {
	st := netzTabelle{Namen: map[string]string{}, Ich: "s/mara"}
	for _, name := range []string{"Mara Lindner", "Jutta Lindner", "Rolf Lindner", "Mia Albers", "Sven Albers"} {
		st.Kontakte = append(st.Kontakte, netzKontakt{Key: "s/" + strings.ToLower(strings.Fields(name)[0]), Name: name})
	}
	return neuesNetz(st, nil, as, time.UTC, time.Now)
}

// The empty places of edit mode (owner 2026-11-13). In Mara's family:
// upward, she, her partner and their ancestors have two parents' places,
// the ones nobody stands at free (Jutta one, beside Ilse; the
// grandparents and the parents-in-law two each); nobody who married in
// has any (Uli, Nele, Lea). Every couple and every parent alone has one
// free child, no more. Mara has parents and a partner: no free sibling
// of her own (her parents' free child is it), no free partner. Hanna,
// the aunt no line places, is offered first where a line would place
// her: as a child of either pair of grandparents. The tree to look at
// has none of it, and the edit tree is as clean a picture: nobody on
// another, no stroke through a box, none on another.
func TestStammbaumFrei(t *testing.T) {
	n := sippeNetz(nil)
	want := []string{"Kind von Dieter und Karin", "Kind von Emil und Greta ← s/hanna", "Kind von Heidi und Uli", "Kind von Ilse ← s/hanna", "Kind von Jonas und Lea",
		"Kind von Jutta und Rolf", "Kind von Nele und Tim", "Kind von dir und Sven",
		"Mutter oder Vater von Dieter", "Mutter oder Vater von Dieter", "Mutter oder Vater von Emil", "Mutter oder Vater von Emil", "Mutter oder Vater von Greta", "Mutter oder Vater von Greta",
		"Mutter oder Vater von Ilse", "Mutter oder Vater von Ilse", "Mutter oder Vater von Jutta", "Mutter oder Vater von Karin", "Mutter oder Vater von Karin"}
	if got := freiVon(n); !slices.Equal(got, want) {
		t.Fatalf("the empty places:\n%s\nnot\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	sb, eb := n.Stammbaeume()
	if len(sb.Frei) != 0 || slices.ContainsFunc(sb.Personen, func(p baumPerson) bool { return istFrei(p.ID) }) || len(eb.Personen) != len(sb.Personen)+len(want) {
		t.Fatalf("to look at: %d places; to edit: %d boxes of %d people", len(sb.Frei), len(eb.Personen), len(sb.Personen))
	}
	if f := strichFehler(eb); len(f) > 0 {
		t.Fatalf("the edit tree: %v", f)
	}
	wo := baumPos(eb)
	for id, f := range eb.Frei { // a free parent stands a row above, a free child a row below
		if d := wo[f.Y[0]].Gen - wo[id].Gen; d != baumStufe[f.Art] {
			t.Fatalf("%s stands %d rows above %s", f.text(n), d, f.Y[0])
		}
	}
	for _, s := range eb.Striche { // a stroke to a place stands for no line and opens nothing
		if slices.ContainsFunc(s.Fuer, func(l baumLinie) bool { return istFrei(l.A) || istFrei(l.B) }) || istFrei(s.A) || istFrei(s.B) || s.Frei && (s.Fest || len(s.Fuer) > 0) {
			t.Fatalf("a stroke of a place: %+v", s)
		}
	}
	for i, p := range sb.Personen { // the people stand in the edit tree as in the tree: the same row, the same order from left to right
		if wo[p.ID].Gen != p.Gen+wo[sb.ich].Gen-baumPos(sb)[sb.ich].Gen {
			t.Fatalf("%s changed its row", p.Name)
		}
		if q := sb.Personen[max(i-1, 0)]; q.Gen == p.Gen && q.X < p.X && wo[q.ID].X >= wo[p.ID].X {
			t.Fatalf("%s and %s changed sides", q.Name, p.Name)
		}
	}
	if eb.Breite > sb.Breite+3*baumSpalte {
		t.Fatalf("the edit tree is %v wide, the tree %v", eb.Breite, sb.Breite)
	}
	for _, s := range eb.Striche { // the line up to parents nobody stands at is dashed, also over Emil and Kurt, siblings without parents
		if g := s.Seg[0]; s.Art == "stamm" && g[1] < wo["s/greta"].Y && s.Fest {
			t.Fatalf("a solid line up to empty places: %+v", s)
		}
	}
	var b strings.Builder
	stammbaumTab(&b, n)
	_, zeigen, _ := strings.Cut(b.String(), "<div class=nur-zeigen>")
	vorher, _, _ := strings.Cut(b.String(), "<div class=nur-zeigen>")
	if strings.Contains(zeigen, "class=frei") || strings.Contains(zeigen, freiPrefix) || strings.Count(vorher, "<a href=\"#dazu\" class=frei role=button") != len(want) {
		t.Fatalf("places in the tree to look at, or not %d in the one to edit", len(want))
	}
	for _, s := range []string{`data-art="kind" data-y="s/emil|s/greta" data-vor="s/hanna" data-text="Kind von Emil und Greta" aria-label="Kind von Emil und Greta: frei, eintragen"`,
		`data-art="eltern" data-y="s/jutta" data-vor="" data-text="Mutter oder Vater von Jutta"`, `class="linie kind frei"`, `<p class="k nur-bearbeiten">` + saysBauErklaert + `</p>`} {
		if !strings.Contains(vorher, s) {
			t.Fatalf("%q missing:\n%s", s, vorher)
		}
	}
	for _, s := range []string{"#baum-frei,.nur-bearbeiten,#baum-tab.bearbeiten .nur-zeigen{display:none}", "html:not(.skript) .baum-kopf", ".baum a.frei rect{"} {
		if !strings.Contains(bauCSS, s) {
			t.Fatalf("the stylesheet lacks %q", s)
		}
	}
}

// From nothing, step by step (owner: „Wenn man einen füllt wird ein neuer
// frei“): Mara alone has her parents' two places, a free sibling beside
// her, a free partner and a free child, and the tree to look at is
// empty. A child named: the next child is free. A parent named: her
// other parent's place stays free, the new one has her own parents'
// places, and Mara's free sibling is now that parent's free child. A
// partner named: no free partner, and the free child is the couple's.
func TestStammbaumFreiWaechst(t *testing.T) {
	am := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	as := map[string]antwort{}
	n := alleinNetz(as)
	sb, eb := n.Stammbaeume()
	if len(sb.Personen) != 0 || len(eb.Personen) != 6 || len(strichFehler(eb)) > 0 {
		t.Fatalf("alone: %d to look at, %d to edit, %v", len(sb.Personen), len(eb.Personen), strichFehler(eb))
	}
	want := []string{"Geschwister von dir", "Kind von dir", "Mutter oder Vater von dir", "Mutter oder Vater von dir", "Partner/in von dir"}
	if got := freiVon(n); !slices.Equal(got, want) {
		t.Fatalf("alone: %v", got)
	}
	var b strings.Builder
	stammbaumTab(&b, n)
	if !strings.Contains(b.String(), "id=baum-modus>Bearbeiten</button>") || !strings.Contains(b.String(), esc(saysStammbaumLeer)) || strings.Contains(b.String(), "id=stammbaum>") {
		t.Fatalf("alone, the tab:\n%s", b.String())
	}
	as[paarKey("s/mia", "s/mara")] = antwort{X: "s/mia", Art: "kind", Am: am}
	if got := freiVon(alleinNetz(as)); !slices.Equal(got, want) {
		t.Fatalf("with a child: %v", got)
	}
	as[paarKey("s/jutta", "s/mara")] = antwort{X: "s/jutta", Art: "eltern", Am: am}
	want = []string{"Kind von Jutta", "Kind von dir", "Mutter oder Vater von Jutta", "Mutter oder Vater von Jutta", "Mutter oder Vater von dir", "Partner/in von dir"}
	if got := freiVon(alleinNetz(as)); !slices.Equal(got, want) {
		t.Fatalf("with a mother: %v", got)
	}
	as[paarKey("s/rolf", "s/mara")] = antwort{X: "s/rolf", Art: "eltern", Am: am}
	as[paarKey("s/sven", "s/mara")] = antwort{X: "s/sven", Art: "partner", Am: am}
	want = []string{"Kind von Jutta und Rolf", "Kind von dir und Sven", "Mutter oder Vater von Jutta", "Mutter oder Vater von Jutta", "Mutter oder Vater von Rolf", "Mutter oder Vater von Rolf",
		"Mutter oder Vater von Sven", "Mutter oder Vater von Sven"}
	n = alleinNetz(as)
	if got := freiVon(n); !slices.Equal(got, want) {
		t.Fatalf("with both parents and a partner: %v", got)
	}
	if _, eb := n.Stammbaeume(); len(strichFehler(eb)) > 0 {
		t.Fatalf("the edit tree: %v", strichFehler(eb))
	}
}

// A place filled on the page (owner 2026-11-13): anna's tree offers Erna,
// known as her grandmother, first at the place of a parent of each of
// her parents; named at Rosi's, she stands there, and Hedwig, known as
// an aunt, is offered as Erna's free child. A couple's free child sets
// the line to both parents, each the person's own answer and none only
// derived, in one post with one sentence, for a known name and for a new
// one; Rückgängig takes back both lines and the new person.
func TestStammbaumFreiFuellen(t *testing.T) {
	wt, ut, _, innen, _ := kanteBox(t)
	schauFamilie(t, ut, innen)
	page := wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String()
	for _, s := range []string{`data-art="eltern" data-y="p-rosi" data-vor="p-erna" data-text="Mutter oder Vater von Rosi"`, `data-art="eltern" data-y="p-karl" data-vor="p-erna"`,
		`data-art="kind" data-y="p-karl|p-rosi" data-vor="" data-text="Kind von Karl und Rosi"`, `class="k seite">Tanten und Onkel</text>`} {
		if !strings.Contains(page, s) {
			t.Fatalf("%q missing:\n%s", s, page)
		}
	}
	a := bauPost(t, wt, url.Values{"x": {"p-erna"}, "y": {"p-rosi"}, "art": {"eltern"}}, 200)
	if !strings.Contains(a.HTML, `data-art="kind" data-y="p-erna" data-vor="p-hedwig" data-text="Kind von Erna"`) || strings.Contains(a.HTML, `data-vor="p-erna"`) ||
		strings.Count(a.HTML, `data-text="Mutter oder Vater von Rosi"`) != 1 || strings.Count(a.HTML, `data-text="Mutter oder Vater von Erna"`) != 2 {
		t.Fatalf("after Erna:\n%s", a.HTML)
	}
	vorher := antwortenLaden(innen)
	eltern := []string{"p-karl", "p-rosi"}
	if e := bauPost(t, wt, url.Values{"x": {"p-ben"}, "y": eltern, "art": {"partner"}}, 400); e.Fehler != saysBauOhne { // only a parent or a child is that of several
		t.Fatalf("a partner of two: %+v", e)
	}
	for _, wer := range []url.Values{{"x": {"p-hedwig"}}, {"neu": {"Tom Berg"}}} {
		form := url.Values{"y": eltern, "art": {"kind"}}
		for k, v := range wer {
			form[k] = v
		}
		c := bauPost(t, wt, form, 200)
		q, _ := url.ParseQuery(c.Zurueck)
		x := wer.Get("x")
		if x == "" {
			x = q.Get("neu")
		}
		name := ut.u.Netz().knoten[x].Name
		as := antwortenLaden(innen)
		for _, e := range eltern {
			if got := as[paarKey(x, e)]; got.X != x || got.Art != "kind" || got.Aus != "" {
				t.Fatalf("%s and %s: %+v", name, e, got)
			}
		}
		if len(q["paar"]) != 2 || !strings.HasPrefix(c.Satz, name+" ist Kind von Karl Berg und Rosi Berg · "+name+" ist für dich: Geschwister") || c.Fehler != "" ||
			!strings.Contains(c.HTML, `data-name="`+name+`" aria-label="`+name+`, Geschwister"`) || !strings.Contains(c.HTML, `data-art="kind" data-y="p-karl|p-rosi"`) {
			t.Fatalf("%s as the couple's child: %+v", name, c)
		}
		q.Set("tu", "zurueck")
		if d := bauPost(t, wt, q, 200); d.Satz != saysBauZurueck || strings.Contains(d.HTML, `aria-label="`+name+`, Geschwister"`) {
			t.Fatalf("%s is not taken back: %+v", name, d)
		}
		if got := antwortenLaden(innen); len(got) != len(vorher) || len(angelegtLaden(innen)) != 0 {
			t.Fatalf("after Rückgängig: %d answers, not %d; %d people by hand", len(got), len(vorher), len(angelegtLaden(innen)))
		}
	}
}

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The tests of Homes' internal/netz/fragen_test.go, on the same fixture:
// the archive asks the same questions with the same suggestions.

func fragenVon(fs []Frage) (out []string) {
	for _, f := range fs {
		out = append(out, f.X.Name+">"+f.Y.Name+"="+f.Vorschlag)
	}
	return out
}

// What is asked of netzBox (Anna is the person): Kim, with Anna's family
// name, about Anna first („zu dir“); then what follows from the phone
// books (Fritz is Anna's sibling, Anna Ben's partner: Fritz is Ben's
// Schwager); then the family circle (Fritz, Anna's sibling; Kim and Ben
// with her name). None where a phone book names a relation (Anna–Ben,
// Anna–Fritz), none about a hidden person, and none for Carla: the same
// firm as Anna, so they know each other — a reason, no question.
func TestFragenOhneBeziehung(t *testing.T) {
	ut := netzBox(t)
	n := ut.u.Netz()
	fs := n.Fragen()
	if got := fragenVon(fs); !slices.Equal(got, []string{"Kim Berg>Anna Berg=", "Fritz Kurz>Ben Berg=schwager", "Fritz Kurz>Kim Berg=", "Ben Berg>Kim Berg="}) {
		t.Fatalf("the questions: %v", got)
	}
	if !fs[1].Abgeleitet || fs[1].Weil != "du und Fritz Kurz Geschwister seid und du und Ben Berg Partner seid" || fs[0].Abgeleitet || !fs[0].Ich || fs[1].Ich || fs[2].Ich {
		t.Fatalf("%+v", fs)
	}
	if carla := n.Von("haushalt/c"); !slices.ContainsFunc(n.Nahe("p-anna", 10), func(v Verbindung) bool { return v.Mit.ID == carla }) {
		t.Fatal("Carla is no question, but still close")
	}
	ut.u.Verbergen(ut.innen, "p-kim", verbergenAus)
	if got := fragenVon(n.Fragen()); !slices.Equal(got, []string{"Fritz Kurz>Ben Berg=schwager"}) {
		t.Fatalf("kim hidden: %v", got)
	}
}

// An answer is a relation of its own, shown right from both ends, never
// asked again, kept over a round and a new opening; what follows from it
// by definition comes with it (Fritz, Anna's sibling, is Kim's uncle),
// what does not stays a question (Ben, Anna's partner, Kim's parent?);
// Keine is never asked again, Spaeter last; a pair not asked is refused;
// forgetting takes the answer and what followed. Only in the vault.
func TestBeantworten(t *testing.T) {
	ut := netzBox(t)
	u := ut.u
	if f, err := u.Beantworten(ut.innen, "p-anna", "p-kim", "eltern"); err != nil || f.X.ID != "p-anna" || f.Ich {
		t.Fatalf("%+v %v", f, err)
	}
	n := u.Netz()
	kim := n.Nahe("p-anna", 10)[1]
	if kim.Mit.ID != "p-kim" || n.Gruende("p-anna", kim.Kante) != "2 Tage auf deinen Fotos seit 2019 · Kind (von dir angegeben)" ||
		n.Gruende("p-kim", kim.Kante) != "2 Tage auf deinen Fotos seit 2019 · Mutter/Vater (von dir angegeben)" {
		t.Fatalf("anna–kim: %+v %q", kim, n.Gruende("p-anna", kim.Kante))
	}
	if d := n.Gefolgerte(paarKey("p-anna", "p-kim")); len(d) != 1 || gegebenSatz(d[0]) != "Fritz Kurz ist Onkel/Tante von Kim Berg." {
		t.Fatalf("followed: %+v", d)
	}
	if got := fragenVon(n.Fragen()); !slices.Equal(got, []string{"Ben Berg>Kim Berg=eltern", "Fritz Kurz>Ben Berg=schwager"}) {
		t.Fatalf("after the first answer: %v", got)
	}
	if _, err := u.Beantworten(ut.innen, "p-kim", "p-ben", artKeine); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Beantworten(ut.innen, "p-fritz", "p-ben", artSpaeter); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Beantworten(ut.innen, "p-gerd", "p-anna", "freund"); err != errFrageWeg {
		t.Fatalf("gerd, one day only: %v", err)
	}
	if _, err := u.Beantworten(ut.innen, n.Von("haushalt/c"), "p-anna", "kollege"); err != errFrageWeg {
		t.Fatalf("carla, they know each other: %v", err)
	}
	if got := fragenVon(n.Fragen()); !slices.Equal(got, []string{"Fritz Kurz>Ben Berg=schwager"}) {
		t.Fatalf("after the answers: %v", got)
	}
	if _, err := u.Beantworten(ut.innen, "p-anna", "p-ben", "partner"); err != errFrageWeg {
		t.Fatalf("a relation the phone book names: %v", err)
	}
	if _, err := u.Beantworten(ut.innen, "p-fritz", "p-kim", "erzfeind"); err != errFrageWeg {
		t.Fatalf("no such kind: %v", err)
	}
	*ut.jetzt = ut.jetzt.Add(25 * time.Hour)
	u.Runde(t.Context(), ut.innen)
	again := &Umfeld{URL: u.URL, Loc: time.UTC, Now: u.Now}
	again.laden(ut.innen)
	for _, m := range []*Netz{u.Netz(), again.Netz()} {
		nahe := m.Nahe("p-kim", 10)
		i := slices.IndexFunc(nahe, func(v Verbindung) bool { return v.Mit.ID == "p-anna" })
		if got := fragenVon(m.Fragen()); len(got) != 1 || i < 0 || !strings.Contains(m.Gruende("p-kim", nahe[i].Kante), "Mutter/Vater (von dir angegeben)") ||
			len(m.Gefolgerte(paarKey("p-anna", "p-kim"))) != 1 {
			t.Fatalf("kept: %v", got)
		}
	}
	if fi, err := os.Stat(ut.innen.umfeldAntworten()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the answers not in the vault: %v %v", fi, err)
	}
	filepath.WalkDir(ut.daten, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("an answer outside the vault: %s", p)
		}
		return nil
	})
	if err := again.Vergessen(ut.innen, paarKey("p-anna", "p-kim")); err != nil {
		t.Fatal(err)
	}
	if got := fragenVon(again.Netz().Fragen()); !slices.Equal(got, []string{"Kim Berg>Anna Berg=", "Fritz Kurz>Kim Berg=", "Fritz Kurz>Ben Berg=schwager"}) || len(again.Netz().Gefolgerte(paarKey("p-anna", "p-kim"))) != 0 {
		t.Fatalf("forgotten, with what followed: %v", got)
	}
}

// The suggestions: a name's „Oma“ toward the person; the family name and
// address with the age gap (siblings, partners, parents, grandparents, or
// only kin); an address and a similar age; a street; many photo days.
func TestVorschlag(t *testing.T) {
	jetzt := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	n := &Netz{Now: func() time.Time { return jetzt }, ich: "ich", knoten: map[string]Knoten{"oma": {Name: "Oma Hilde"}}}
	geb := func(j int) time.Time { return time.Date(j, 6, 1, 0, 0, 0, 0, time.UTC) }
	haus := Kante{Fest: []festBand{{Art: bandAdresse}}}
	for _, c := range []struct {
		x, y       merkmale
		k          Kante
		name, want string
	}{
		{merkmale{}, merkmale{}, Kante{}, "oma", "grosseltern „Oma“ im Namen"},
		{merkmale{family: "berg", geb: geb(2012)}, merkmale{family: "berg", geb: geb(2015)}, haus, "", "geschwister gleicher Nachname und gleiche Adresse, 3 Jahre Altersunterschied"},
		{merkmale{family: "berg", geb: geb(1980)}, merkmale{family: "berg", geb: geb(1983)}, haus, "", "partner gleicher Nachname und gleiche Adresse, 3 Jahre Altersunterschied"},
		{merkmale{family: "berg", geb: geb(1980)}, merkmale{family: "berg", geb: geb(2012)}, haus, "", "eltern gleicher Nachname und gleiche Adresse, 32 Jahre Altersunterschied"},
		{merkmale{family: "berg", geb: geb(2012)}, merkmale{family: "berg", geb: geb(1950)}, haus, "", "enkel gleicher Nachname und gleiche Adresse, 62 Jahre Altersunterschied"},
		{merkmale{family: "berg"}, merkmale{family: "berg"}, haus, "", "verwandt gleicher Nachname und gleiche Adresse"},
		{merkmale{family: "berg", geb: geb(1980)}, merkmale{family: "wolf", geb: geb(1978)}, haus, "", "partner gleiche Adresse, 2 Jahre Altersunterschied"},
		{merkmale{family: "berg", geb: geb(1980)}, merkmale{family: "wolf", geb: geb(1950)}, haus, "", " "},
		{merkmale{strassen: []string{"gartenweg|66111"}}, merkmale{strassen: []string{"gartenweg|66111"}}, Kante{}, "", "nachbar gleiche Straße, anderes Haus"},
		{merkmale{family: "berg"}, merkmale{family: "wolf"}, Kante{Tage: 6}, "", "freund 6 Tage zusammen auf deinen Fotos, kein Zeichen von Familie"},
		{merkmale{family: "berg"}, merkmale{family: "berg"}, Kante{Tage: 6}, "", " "},
		{merkmale{family: nachname("", "Oma Hilde")}, merkmale{family: "wolf"}, Kante{Tage: 6}, "", " "}, // Hilde is no family name
	} {
		x := "x"
		if c.name != "" {
			x = c.name
		}
		n.merkmale = map[string]merkmale{x: c.x, "ich": c.y}
		if art, weil := n.vorschlag(x, "ich", &c.k); art+" "+weil != c.want {
			t.Errorf("%+v %+v: %q %q, want %q", c.x, c.y, art, weil, c.want)
		}
	}
}

// The cards give what the suggestions need: N's family name, BDAY, the
// street without its number with its postcode.
func TestMerkmaleAusKarte(t *testing.T) {
	k := kartenLesen("haushalt/", karteMit("x", "Clara B.", "N:Beispiel;Clara;;;\r\nBDAY:20140203\r\nADR;TYPE=HOME:;;7 Gartenstraße;Stuttgart;;70173;Deutschland\r\n"))[0]
	if k.family != "Beispiel" || k.geb != "2014-02-03" || !slices.Equal(k.strassen, []string{"gartenstrasse|70173"}) {
		t.Fatalf("%+v", k)
	}
	if gebLesen("--0203") != "" || gebLesen("1984-04-12T00:00:00Z") != "1984-04-12" {
		t.Fatal("BDAY")
	}
}

// Fremd takes the tie out of the net: neither end shows the other, and the
// pair is not asked again; forgetting it brings the tie back.
func TestFremd(t *testing.T) {
	ut := netzBox(t)
	n := ut.u.Netz()
	nahe := func() bool {
		return slices.ContainsFunc(n.Nahe("p-kim", 10), func(k Verbindung) bool { return k.Mit.ID == "p-anna" })
	}
	if !nahe() {
		t.Fatal("kim–anna: no tie to start with")
	}
	if _, err := ut.u.Beantworten(ut.innen, "p-kim", "p-anna", artFremd); err != nil {
		t.Fatal(err)
	}
	if nahe() || slices.ContainsFunc(fragenVon(n.Fragen()), func(s string) bool { return strings.Contains(s, "Anna") && strings.Contains(s, "Kim") }) {
		t.Fatalf("fremd: still a tie or a question: %v", fragenVon(n.Fragen()))
	}
	if g := n.Gegebene(); len(g) != 1 || gegebenSatz(g[0]) != "Du kennst Kim Berg nicht." || g[0].Karte.Wohin != "" {
		t.Fatalf("listed: %+v", g)
	}
	if err := ut.u.Vergessen(ut.innen, paarKey("p-kim", "p-anna")); err != nil || !nahe() {
		t.Fatalf("forgotten: %v, tie back %v", err, nahe())
	}
}

// Where an answer goes (owner 2026-11-10): two household contacts → both
// household cards, by UID; anything with the person's own phone contact
// → only the phone; a household card never carries a name from the
// phone or the photos — the line does not even name a household UID then.
func TestUebernahmeWohin(t *testing.T) {
	ut := netzBox(t)
	n := ut.u.Netz()
	carla := n.Von("haushalt/c")
	for _, c := range []struct {
		x, y, art, wohin string
		zeile            uebernahmeZeile
	}{
		{carla, "p-anna", "kollege", wohinHaushalt, uebernahmeZeile{X: uebernahmeEnde{Name: "Carla Wolf", Haushalt: "c"}, Y: uebernahmeEnde{Name: "Anna Berg", Haushalt: "a"}, Art: "kollege"}},
		{"p-kim", "p-anna", "kind", wohinHandy, uebernahmeZeile{X: uebernahmeEnde{Name: "Kim Berg"}, Y: uebernahmeEnde{Name: "Anna Berg", Handy: "k1"}, Art: "kind"}},
		{"p-fritz", "p-ben", "freund", wohinHandy, uebernahmeZeile{X: uebernahmeEnde{Name: "Fritz Kurz", Handy: "urn:uuid:k2"}, Y: uebernahmeEnde{Name: "Ben Berg"}, Art: "freund"}},
		{"p-kim", "p-gerd", "freund", "", uebernahmeZeile{}},
	} {
		u := n.Uebernahme(c.x, c.y, c.art)
		if u.Wohin != c.wohin || u.Zeile != c.zeile {
			t.Errorf("%s–%s: %+v", c.x, c.y, u)
		}
	}
}

// band puts a tie of photos between a and b into n, as a round would
// (only for asking: the next rebuild drops it).
func band(n *Netz, a, b string, tage int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	k := &Kante{A: min(a, b), B: max(a, b), Staerke: float64(tage), Rang: float64(tage), Tage: tage, Seit: 2020}
	n.kanten[a], n.kanten[b] = append(n.kanten[a], k), append(n.kanten[b], k)
}

// wertNetz is Ida's net (h/ida is the person, Ida Roth): Udo, her husband
// by her card, and his daughter Mia; Rolf Roth and Wilma Wehr share their
// family names; Olaf on six days of her photos; and among others: Phil
// and Lukas on 29 days (the owner's screenshot), Kai and Lea at one firm,
// Nora and Emil in one street, Vera and Tim Stein at one address, Nina
// and Tom Sand on three days, Hans and Gabi Gast on one.
func wertNetz() *Netz {
	st := netzTabelle{Namen: map[string]string{}, Ich: "h/ida"}
	for _, k := range [][2]string{{"ida", "Ida Roth"}, {"udo", "Udo Wehr"}, {"mia", "Mia Wehr"}, {"rolf", "Rolf Roth"}, {"wilma", "Wilma Wehr"}, {"olaf", "Olaf Ott"},
		{"phil", "Phil Engler"}, {"lukas", "Lukas Diekmann"}, {"kai", "Kai Kern"}, {"lea", "Lea Haas"}, {"nora", "Nora Feld"}, {"emil", "Emil Busch"},
		{"vera", "Vera Stein"}, {"tim", "Tim Stein"}, {"nina", "Nina Sand"}, {"tom", "Tom Sand"}, {"hans", "Hans Gast"}, {"gabi", "Gabi Gast"}} {
		c := netzKontakt{Key: "h/" + k[0], Name: k[1]}
		if k[0] == "nora" || k[0] == "emil" {
			c.Strassen = []string{"gartenweg|66111"}
		}
		st.Kontakte = append(st.Kontakte, c)
	}
	st.Fest = []festBand{{A: "h/ida", B: "h/udo", Art: bandBeziehung, Wie: beziehungsWort("Ehemann")}, {A: "h/udo", B: "h/mia", Art: bandBeziehung, Wie: beziehungsWort("Tochter")},
		{A: "h/kai", B: "h/lea", Art: bandFirma}, {A: "h/vera", B: "h/tim", Art: bandAdresse}}
	n := neuesNetz(st, nil, nil, time.UTC, func() time.Time { return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) })
	band(n, "h/ida", "h/olaf", 6)
	band(n, "h/phil", "h/lukas", 29)
	band(n, "h/nora", "h/emil", 3)
	band(n, "h/nina", "h/tom", 3)
	band(n, "h/hans", "h/gabi", 1)
	return n
}

// Only what has value (owner 2026-11-11): the person's family first (Mia,
// whom Udo's card names his daughter, derived; Rolf and Wilma by their
// names, without a tie), then the family circle among themselves, then
// two others of one name with a tie. Photos, chats, a firm, a street
// alone are „kennen sich“: Olaf, Phil–Lukas, Kai–Lea, Nora–Emil are no
// question, and still a tie; Hans–Gabi has one photo day, too weak to
// be a tie at all.
func TestFragenNurFamilie(t *testing.T) {
	n := wertNetz()
	want := []string{
		"Mia Wehr>Ida Roth=kind", "Rolf Roth>Ida Roth=", "Wilma Wehr>Ida Roth=",
		"Mia Wehr>Rolf Roth=", "Mia Wehr>Wilma Wehr=", "Rolf Roth>Udo Wehr=", "Rolf Roth>Wilma Wehr=", "Udo Wehr>Wilma Wehr=",
		"Nina Sand>Tom Sand=", "Tim Stein>Vera Stein=verwandt",
	}
	if got := fragenVon(n.Fragen()); !slices.Equal(got, want) {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	if !slices.ContainsFunc(n.Nahe("h/phil", 5), func(v Verbindung) bool { return v.Mit.ID == "h/lukas" }) {
		t.Fatal("Phil and Lukas know each other: still a tie")
	}
	if (&Kante{Tage: 1}).belegt() || !(&Kante{Tage: 2}).belegt() || !(&Kante{Fest: []festBand{{Art: bandFirma}}}).belegt() || !(&Kante{Chats: 1, Faeden: 1}).belegt() {
		t.Fatal("belegt: the threshold of a tie")
	}
}

// A visit's portion (owner 2026-11-11): ten, less what the person
// answered since the visit began; Später waits behind it; the next visit
// brings ten again, the skipped last; what followed from an answer does
// not count.
func TestPortion(t *testing.T) {
	rel := [][3]string{{"h/anna", "h/ben", "Partner"}}
	for _, k := range []string{"carla", "dora", "emil", "frida", "gustav", "hanna", "ida", "jan"} {
		rel = append(rel, [3]string{"h/ben", "h/" + k, "Kind"})
	}
	jetzt := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	n := buchNetz("", rel, nil)
	n.Now = func() time.Time { return jetzt }
	alle := len(n.Fragen())
	besuch := jetzt
	fs, weitere := n.Portion(besuch)
	if alle != 36 || len(fs) != fragenJeBesuch || len(weitere) != alle-fragenJeBesuch {
		t.Fatalf("%d open, %d now, %d later", alle, len(fs), len(weitere))
	}
	for _, f := range fs[:3] {
		if _, _, err := n.Beantworten(f.X.ID, f.Y.ID, f.Vorschlag); err != nil {
			t.Fatal(err)
		}
	}
	spaeter := fs[3]
	n.Beantworten(spaeter.X.ID, spaeter.Y.ID, artSpaeter)
	fs, weitere = n.Portion(besuch)
	if len(fs) != 6 || slices.ContainsFunc(fs, func(f Frage) bool { return f.X.ID == spaeter.X.ID && f.Y.ID == spaeter.Y.ID }) ||
		weitere[len(weitere)-1].X.ID != spaeter.X.ID || weitere[len(weitere)-1].Y.ID != spaeter.Y.ID {
		t.Fatalf("after three answers and one Später: %d now, %v", len(fs), fragenVon(fs))
	}
	jetzt = jetzt.Add(time.Hour)
	if fs, _ = n.Portion(jetzt); len(fs) != fragenJeBesuch || fs[len(fs)-1].X.ID == spaeter.X.ID && fs[len(fs)-1].Y.ID == spaeter.Y.ID {
		t.Fatalf("the next visit: %v", fragenVon(fs))
	}
	for range fragenJeBesuch {
		fs, _ = n.Portion(jetzt)
		n.Beantworten(fs[0].X.ID, fs[0].Y.ID, artKeine)
	}
	if fs, weitere = n.Portion(jetzt); len(fs) != 0 || len(weitere) == 0 {
		t.Fatalf("the visit's portion done: %d now, %d more", len(fs), len(weitere))
	}

	// Hedwig, the mother of Uwe, takes with her what follows; one answer, not three
	n = kranzNetz(nil)
	n.Now = func() time.Time { return jetzt }
	n.Beantworten("h/hedwig", "h/uwe", "eltern")
	if d := n.Gefolgerte(paarKey("h/hedwig", "h/uwe")); len(d) != 2 {
		t.Fatalf("followed: %+v", d)
	}
	if fs, _ := n.Portion(jetzt); len(fs) != min(fragenJeBesuch-1, len(n.Fragen())) {
		t.Fatalf("what followed counted: %d of %d", len(fs), len(n.Fragen()))
	}
}

// kranzNetz: Uwe's card names Silke his wife and Lena his daughter, and
// Hedwig Kranz lives with Uwe; nobody is the person.
func kranzNetz(antworten map[string]antwort) *Netz {
	n := buchNetz("", [][3]string{{"h/uwe", "h/silke", "Ehefrau"}, {"h/uwe", "h/lena", "Tochter"}}, antworten)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.st.Kontakte = append(n.st.Kontakte, netzKontakt{Key: "h/hedwig", Name: "Hedwig"})
	n.st.Fest = append(n.st.Fest, festBand{A: "h/hedwig", B: "h/uwe", Art: bandAdresse})
	for i := range n.st.Kontakte {
		n.st.Kontakte[i].Family = "Kranz"
	}
	n.bauen()
	return n
}

// What follows by definition is taken over with the answer, as a chain
// (owner 2026-11-11): Hedwig is Uwe's mother, so Silke's mother-in-law
// (Uwe's wife) and Lena's grandmother (Uwe's daughter). What does not
// hold by definition stays a question: Silke, Uwe's wife, Lena's mother?
// One Vergessen takes the chain back. An answer is never written over:
// had the person said Hedwig is Silke's friend, that conflict is asked —
// once — and nothing else of it is lost.
func TestFolgernKette(t *testing.T) {
	n := kranzNetz(nil)
	if got := fragenVon(n.Fragen()); !slices.Equal(got, []string{"Silke>Lena=eltern", "Hedwig>Uwe=verwandt"}) {
		t.Fatalf("before: %q", got)
	}
	f, _, err := n.Beantworten("h/hedwig", "h/uwe", "eltern")
	if err != nil || f.Abgeleitet {
		t.Fatalf("%+v %v", f, err)
	}
	var kette []string
	for _, d := range n.Gefolgerte(paarKey("h/hedwig", "h/uwe")) {
		kette = append(kette, gegebenSatz(d)+" weil "+d.Weil)
	}
	if !slices.Equal(kette, []string{
		"Hedwig ist Großeltern von Lena. weil Uwe Hedwigs Kind ist und Lena Uwes Kind ist",
		"Hedwig ist Schwiegereltern von Silke. weil Uwe Hedwigs Kind ist und Uwe und Silke Partner sind",
	}) {
		t.Fatalf("the chain: %q", kette)
	}
	if got := fragenVon(n.Fragen()); !slices.Equal(got, []string{"Silke>Lena=eltern"}) {
		t.Fatalf("after: %q", got)
	}
	alle, err := n.Vergessen(paarKey("h/hedwig", "h/uwe"))
	if err != nil || len(alle) != 0 || len(n.Gegebene()) != 0 {
		t.Fatalf("one Vergessen, the whole chain: %v %+v", err, alle)
	}

	freundin := map[string]antwort{paarKey("h/hedwig", "h/silke"): {X: "h/silke", Art: "freund"}}
	n = kranzNetz(freundin)
	n.Beantworten("h/hedwig", "h/uwe", "eltern")
	if d := n.Gefolgerte(paarKey("h/hedwig", "h/uwe")); len(d) != 1 || d[0].Art != "grosseltern" {
		t.Fatalf("written over an answer: %+v", d)
	}
	fs := n.Fragen()
	if got := fragenVon(fs); !slices.Equal(got, []string{"Silke>Lena=eltern", "Hedwig>Silke=schwiegereltern"}) || !fs[1].Konflikt ||
		fs[1].Weil != "Uwe Hedwigs Kind ist und Uwe und Silke Partner sind; du hattest „Freund/in“ angegeben" {
		t.Fatalf("the conflict, a question: %q %+v", got, fs[1])
	}
	if g, _ := n.Gegebenes(paarKey("h/hedwig", "h/silke")); g.Art != "freund" {
		t.Fatalf("the answer kept: %+v", g)
	}
	n.Beantworten("h/silke", "h/hedwig", "freund") // she stays with what she said
	if got := fragenVon(n.Fragen()); !slices.Equal(got, []string{"Silke>Lena=eltern"}) {
		t.Fatalf("a conflict answered is not asked again: %q", got)
	}
}

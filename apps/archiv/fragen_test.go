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

// A tie without a relation is asked about, the person second („zu dir“);
// none where a phone book names a relation (Anna–Ben, Anna–Fritz), none
// about a hidden person; the strongest first, after the derived ones
// (Fritz is Anna's sibling, Anna Ben's partner: Fritz is Ben's Schwager).
func TestFragenOhneBeziehung(t *testing.T) {
	ut := netzBox(t)
	n := ut.u.Netz()
	fs := n.Fragen()
	if got := fragenVon(fs); !slices.Equal(got, []string{"Fritz Kurz>Ben Berg=schwager", "Fritz Kurz>Kim Berg=", "Ben Berg>Kim Berg=", "Kim Berg>Anna Berg=", "Carla Wolf>Anna Berg=kollege"}) {
		t.Fatalf("the questions: %v", got)
	}
	if !fs[0].Abgeleitet || fs[0].Weil != "du und Fritz Kurz Geschwister seid und du und Ben Berg Partner seid" || fs[1].Abgeleitet ||
		!fs[3].Ich || fs[2].Ich || fs[4].Weil != "gleiche Firma" {
		t.Fatalf("%+v", fs)
	}
	ut.u.Verbergen(ut.innen, "p-kim", verbergenAus)
	if got := fragenVon(n.Fragen()); !slices.Equal(got, []string{"Fritz Kurz>Ben Berg=schwager", "Carla Wolf>Anna Berg=kollege"}) {
		t.Fatalf("kim hidden: %v", got)
	}
}

// An answer is a relation of its own, shown right from both ends, never
// asked again, kept over a round and a new opening; Keine is never asked
// again, Spaeter last; a pair not asked is refused. Only in the vault.
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
	u.Beantworten(ut.innen, "p-kim", "p-ben", artKeine)
	if _, err := u.Beantworten(ut.innen, n.Von("haushalt/c"), "p-anna", artSpaeter); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Beantworten(ut.innen, "p-gerd", "p-anna", "freund"); err != errFrageWeg {
		t.Fatalf("gerd, one day only: %v", err)
	}
	if got := fragenVon(n.Fragen()); !slices.Equal(got, []string{"Fritz Kurz>Kim Berg=onkel", "Fritz Kurz>Ben Berg=schwager", "Carla Wolf>Anna Berg=kollege"}) {
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
		if got := fragenVon(m.Fragen()); len(got) != 3 || !strings.Contains(m.Gruende("p-kim", m.Nahe("p-kim", 10)[0].Kante), "Mutter/Vater (von dir angegeben)") {
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
	if err := again.Vergessen(ut.innen, paarKey("p-anna", "p-kim")); err != nil || len(again.Netz().Fragen()) != 4 {
		t.Fatalf("forgotten: %v %v", err, fragenVon(again.Netz().Fragen()))
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
		return slices.ContainsFunc(n.Nahe("p-kim", 10), func(k Verbindung) bool { return k.Mit.ID == "p-ben" })
	}
	if !nahe() {
		t.Fatal("kim–ben: no tie to start with")
	}
	if _, err := ut.u.Beantworten(ut.innen, "p-kim", "p-ben", artFremd); err != nil {
		t.Fatal(err)
	}
	if nahe() || slices.ContainsFunc(fragenVon(n.Fragen()), func(s string) bool { return strings.Contains(s, "Ben") && strings.Contains(s, "Kim") }) {
		t.Fatalf("fremd: still a tie or a question: %v", fragenVon(n.Fragen()))
	}
	if g := n.Gegebene(); len(g) != 1 || gegebenSatz(g[0]) != "Kim Berg und Ben Berg kennen sich nicht." || g[0].Karte.Wohin != "" {
		t.Fatalf("listed: %+v", g)
	}
	if err := ut.u.Vergessen(ut.innen, paarKey("p-kim", "p-ben")); err != nil || !nahe() {
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

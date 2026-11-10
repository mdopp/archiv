package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// buchNetz is a net of contacts named by their key's letter („h/anna“ is
// Anna) with the relations their cards write: {card, other, word} — the
// card names the other as word, as a phone writes RELATED. No photos.
func buchNetz(ich string, rel [][3]string, antworten map[string]antwort) *Netz {
	st := netzTabelle{Namen: map[string]string{}, Ich: ich}
	gesehen := map[string]bool{}
	kontakt := func(key string) {
		if !gesehen[key] {
			gesehen[key] = true
			_, name, _ := strings.Cut(key, "/")
			st.Kontakte = append(st.Kontakte, netzKontakt{Key: key, Name: strings.ToUpper(name[:1]) + name[1:]})
		}
	}
	for _, r := range rel {
		kontakt(r[0])
		kontakt(r[1])
		st.Fest = append(st.Fest, festBand{A: r[0], B: r[1], Art: bandBeziehung, Wie: beziehungsWort(r[2])})
	}
	return neuesNetz(st, nil, antworten, time.UTC, func() time.Time { return time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC) })
}

// abgeleitetVon are the derived questions as „X>Y=kind: reason“.
func abgeleitetVon(fs []Frage) (out []string) {
	for _, f := range fs {
		if f.Abgeleitet {
			out = append(out, f.X.Name+">"+f.Y.Name+"="+f.Vorschlag+": "+f.Weil)
		}
	}
	return out
}

// Each rule, from the phone books' words in both languages: X is the
// kind of Y, the reason names what it follows from.
func TestAbgeleiteteRegeln(t *testing.T) {
	for _, tc := range []struct {
		name string
		ich  string
		rel  [][3]string
		want []string
	}{
		{"partner of a parent: Eltern",
			"", [][3]string{{"h/anna", "h/ben", "Partner"}, {"h/ben", "h/carla", "Tochter"}},
			[]string{"Anna>Carla=eltern: Anna und Ben Partner sind und Carla Bens Kind ist"}},
		{"step-family: a question, though Carla has two parents",
			"", [][3]string{{"h/anna", "h/ben", "_$!<Spouse>!$_"}, {"h/carla", "h/ben", "Vater"}, {"h/carla", "h/dora", "Mutter"}},
			[]string{"Anna>Carla=eltern: Anna und Ben Partner sind und Carla Bens Kind ist"}},
		{"a shared parent: Geschwister",
			"", [][3]string{{"h/petra", "h/anna", "Kind"}, {"h/petra", "h/ben", "son"}},
			[]string{"Anna>Ben=geschwister: Anna und Ben beide Petras Kinder sind"}},
		{"parent of a parent: Großeltern",
			"", [][3]string{{"h/gerd", "h/ben", "Sohn"}, {"h/ben", "h/carla", "Kind"}},
			[]string{"Gerd>Carla=grosseltern: Ben Gerds Kind ist und Carla Bens Kind ist"}},
		{"sibling of a parent: Onkel/Tante",
			"", [][3]string{{"h/sven", "h/ben", "Bruder"}, {"h/ben", "h/carla", "Tochter"}},
			[]string{"Sven>Carla=onkel: Sven und Ben Geschwister sind und Carla Bens Kind ist"}},
		{"child of an uncle or aunt: Cousin/e",
			"", [][3]string{{"h/xaver", "h/uta", "Tante"}, {"h/uta", "h/carla", "Kind"}},
			[]string{"Carla>Xaver=cousin: Uta Xavers Onkel oder Tante ist und Carla Utas Kind ist"}},
		{"parent of a partner: Schwiegereltern",
			"", [][3]string{{"h/petra", "h/anna", "Tochter"}, {"h/anna", "h/ben", "Ehemann"}},
			[]string{"Petra>Ben=schwiegereltern: Anna Petras Kind ist und Anna und Ben Partner sind"}},
		{"sibling of a partner: Schwager",
			"", [][3]string{{"h/sina", "h/anna", "Schwester"}, {"h/anna", "h/ben", "Partner/in"}},
			[]string{"Sina>Ben=schwager: Sina und Anna Geschwister sind und Anna und Ben Partner sind"}},
		{"partner of a sibling: Schwager",
			"", [][3]string{{"h/quirin", "h/sina", "Ehefrau"}, {"h/sina", "h/ben", "Bruder"}},
			[]string{"Ben>Quirin=schwager: Ben und Sina Geschwister sind und Sina und Quirin Partner sind"}},
		{"the person: „du“, and asked toward her",
			"h/anna", [][3]string{{"h/anna", "h/ben", "Partner"}, {"h/ben", "h/carla", "Tochter"}},
			[]string{"Carla>Anna=kind: du und Ben Partner seid und Carla Bens Kind ist"}},
		{"the person's child",
			"h/anna", [][3]string{{"h/anna", "h/carla", "Tochter"}, {"h/anna", "h/dora", "Tochter"}},
			[]string{"Carla>Dora=geschwister: Carla und Dora beide deine Kinder sind"}},
		{"Klaus: the genitive of a name in s",
			"", [][3]string{{"h/klaus", "h/carla", "Kind"}, {"h/carla", "h/emil", "Kind"}},
			[]string{"Klaus>Emil=grosseltern: Carla Klaus’ Kind ist und Emil Carlas Kind ist"}},
		{"a friend or a colleague derives nothing",
			"", [][3]string{{"h/anna", "h/ben", "friend"}, {"h/ben", "h/carla", "Kollege"}},
			nil},
		{"a pair the phone book names is not asked",
			"", [][3]string{{"h/anna", "h/ben", "Partner"}, {"h/ben", "h/carla", "Tochter"}, {"h/carla", "h/anna", "Mutter"}},
			nil},
	} {
		n := buchNetz(tc.ich, tc.rel, nil)
		if got := abgeleitetVon(n.Fragen()); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// Several at once: each pair once, by the first rule; the derived ones
// come before the plain suggestions.
func TestAbgeleiteteReihenfolge(t *testing.T) {
	n := buchNetz("", [][3]string{
		{"h/anna", "h/ben", "Partner"}, {"h/ben", "h/carla", "Kind"}, {"h/ben", "h/dora", "Kind"}, {"h/gerd", "h/ben", "Sohn"},
	}, nil)
	want := []string{
		"Anna>Carla=eltern: Anna und Ben Partner sind und Carla Bens Kind ist",
		"Anna>Dora=eltern: Anna und Ben Partner sind und Dora Bens Kind ist",
		"Carla>Dora=geschwister: Carla und Dora beide Bens Kinder sind",
		"Gerd>Carla=grosseltern: Ben Gerds Kind ist und Carla Bens Kind ist",
		"Gerd>Dora=grosseltern: Ben Gerds Kind ist und Dora Bens Kind ist",
		"Gerd>Anna=schwiegereltern: Ben Gerds Kind ist und Ben und Anna Partner sind",
	}
	if got := abgeleitetVon(n.Fragen()); !slices.Equal(got, want) {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	// with a plain question there too (a tie of photos), the derived ones first
	n.st.Namen = map[string]string{}
	n.mu.Lock()
	k := &Kante{A: "h/anna", B: "h/gerd", Staerke: 9, Rang: 9, Tage: 9}
	n.kanten["h/anna"], n.kanten["h/gerd"] = append(n.kanten["h/anna"], k), append(n.kanten["h/gerd"], k)
	fs := n.offen()
	n.mu.Unlock()
	if len(fs) != len(want) || !slices.ContainsFunc(fs, func(f Frage) bool {
		return f.Abgeleitet && f.X.Name == "Gerd" && f.Y.Name == "Anna" && f.Kante.Tage == 9
	}) {
		t.Fatalf("the pair with a tie keeps its derived kind and its tie: %+v", fs)
	}
}

// An answered pair is never asked again (Keine and Kennen sich nicht
// too), nothing is set on its own; Später puts it last.
func TestAbgeleitetNieWieder(t *testing.T) {
	rel := [][3]string{{"h/anna", "h/ben", "Partner"}, {"h/ben", "h/carla", "Tochter"}, {"h/petra", "h/sina", "Kind"}, {"h/petra", "h/tom", "Kind"}}
	for _, art := range []string{artKeine, artFremd, "freund", "eltern"} {
		n := buchNetz("", rel, map[string]antwort{paarKey("h/anna", "h/carla"): {X: "h/anna", Art: art}})
		if got := abgeleitetVon(n.Fragen()); !slices.Equal(got, []string{"Sina>Tom=geschwister: Sina und Tom beide Petras Kinder sind"}) {
			t.Errorf("answered %s, asked again: %q", art, got)
		}
	}
	n := buchNetz("", rel, nil)
	if _, _, err := n.Beantworten("h/anna", "h/carla", artKeine); err != nil {
		t.Fatal(err)
	}
	if got := abgeleitetVon(n.Fragen()); len(got) != 1 || n.Nahe("h/anna", 10) != nil && slices.ContainsFunc(n.Nahe("h/anna", 10), func(v Verbindung) bool { return v.Mit.Name == "Carla" }) {
		t.Fatalf("Keine: asked again or set: %q %+v", got, n.Nahe("h/anna", 10))
	}
	n = buchNetz("", rel, map[string]antwort{paarKey("h/anna", "h/carla"): {X: "h/anna", Art: artSpaeter}})
	fs := n.Fragen()
	if got := abgeleitetVon(fs); len(got) != 2 || !strings.HasPrefix(got[1], "Anna>Carla=eltern") || fs[len(fs)-1].X.Name != "Anna" {
		t.Fatalf("Später: last: %q", got)
	}
	// nothing derived is a relation before it is answered
	n = buchNetz("", rel, nil)
	if b := n.Gegebene(); len(b) != 0 || n.imBuchBeziehung("h/anna", "h/carla") {
		t.Fatalf("derived set on its own: %+v", b)
	}
}

// A derivation from a derivation only after its source is answered:
// Anna is likely Carla's parent, so likely Emil's grandparent — asked
// only once the person said Anna is Carla's mother. An answer is a
// source as a phone book's relation is.
func TestAbgeleitetErstNachAntwort(t *testing.T) {
	n := buchNetz("", [][3]string{{"h/anna", "h/ben", "Partner"}, {"h/ben", "h/carla", "Kind"}, {"h/carla", "h/emil", "Sohn"}}, nil)
	want := []string{
		"Anna>Carla=eltern: Anna und Ben Partner sind und Carla Bens Kind ist",
		"Ben>Emil=grosseltern: Carla Bens Kind ist und Emil Carlas Kind ist",
	}
	if got := abgeleitetVon(n.Fragen()); !slices.Equal(got, want) {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	f, _, err := n.Beantworten("h/carla", "h/anna", "kind") // asked the other way round: Carla is Anna's child
	if err != nil || !f.Abgeleitet {
		t.Fatalf("%+v %v", f, err)
	}
	want = []string{
		"Anna>Emil=grosseltern: Carla Annas Kind ist und Emil Carlas Kind ist",
		"Ben>Emil=grosseltern: Carla Bens Kind ist und Emil Carlas Kind ist",
	}
	if got := abgeleitetVon(n.Fragen()); !slices.Equal(got, want) {
		t.Fatalf("after the answer:\n got %q\nwant %q", got, want)
	}
}

// A hidden person is in no derived question, neither asked about nor
// as the reason.
func TestAbgeleitetVerborgen(t *testing.T) {
	n := buchNetz("", [][3]string{{"h/anna", "h/ben", "Partner"}, {"h/ben", "h/carla", "Kind"}}, nil)
	n.Verbergen("h/ben", verbergenAus)
	if got := abgeleitetVon(n.Fragen()); got != nil {
		t.Fatalf("through a hidden person: %q", got)
	}
}

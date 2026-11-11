package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// beziehungswoerter.yml reads, every kind and category is one the archive
// knows, and a word is one word; a broken file is an error, not a panic.
func TestWoerterDatei(t *testing.T) {
	if woerter == nil || len(woerter.arten) < 200 || len(woerter.gruppen) < 50 {
		t.Fatalf("the file is thin: %d words, %d group words", len(woerter.arten), len(woerter.gruppen))
	}
	for wort, as := range woerter.arten {
		for _, a := range as {
			if !slices.Contains(familienArten, a) {
				t.Errorf("%s: %s", wort, a)
			}
		}
	}
	for _, kaputt := range []string{
		"beziehung:\n  - art: erzfeind\n    de: [Feind]\n",
		"beziehung:\n  - art: eltern\n    de: [Meine Mama]\n",
		"beziehung:\n  - art: eltern\n    de: Mama\n",
		"gruppe:\n  - kategorie: sekte\n    de: [Sekte]\nbeziehung:\n  - art: eltern\n    de: [Mama]\n",
		"beziehung: [",
	} {
		if _, err := woerterLesen([]byte(kaputt)); err == nil {
			t.Errorf("read: %q", kaputt)
		}
	}
}

// Words compare with umlauts spelled out or dropped, in any case, across
// the family's languages; a word inside another is none.
func TestWoerterImText(t *testing.T) {
	for _, c := range []struct{ text, wort, art string }{
		{"Hallo Mama, kommst du?", "Mama", "eltern"},
		{"HALLO MAMI", "MAMI", "eltern"},
		{"Liebe Schwägerin", "Schwägerin", "schwager"},
		{"liebe schwaegerin", "schwaegerin", "schwager"},
		{"Liebe Schwagerin", "Schwagerin", "schwager"},
		{"Grüße an Großmutter", "Großmutter", "grosseltern"},
		{"gruesse an grossmutter", "grossmutter", "grosseltern"},
		{"Hi mom!", "mom", "eltern"},
		{"¿Vienes, mamá?", "mamá", "eltern"},
		{"Bonjour mon frère", "frère", "geschwister"},
		{"bonjour frere", "frere", "geschwister"},
		{"kardeşim nasılsın", "kardeşim", "geschwister"},
		{"kardesim nasilsin", "kardesim", "geschwister"},
		{"Cześć babciu", "babciu", "grosseltern"},
		{"Привет мама", "мама", "eltern"},
		{"habibi wein", "habibi", "partner"},
		{"Gute Nacht, Schatz ❤️", "Schatz", "partner"},
	} {
		ts := woerter.imText(c.text)
		if len(ts) != 1 || ts[0].Wort != c.wort || ts[0].Arten[0] != c.art {
			t.Errorf("%q: %+v", c.text, ts)
		}
	}
	for _, nichts := range []string{"Die Tomate ist rot", "Europa", "Omar kommt", "prima!", "", "123"} {
		if ts := woerter.imText(nichts); len(ts) != 0 {
			t.Errorf("%q: %+v", nichts, ts)
		}
	}
	if a := woerter.wortArten("Schatz"); !slices.Equal(a, []string{"partner", "kind"}) {
		t.Fatalf("Schatz: %v", a)
	}
	ts := woerter.imText("Ja, ich hole Oma und Opa morgen um zehn Uhr am Bahnhof ab, und Oma bringt Kuchen mit für alle die kommen")
	if len(ts) != 2 || ts[0].Zitat != "ich hole Oma und Opa" || ts[1].Wort != "Opa" {
		t.Fatalf("each word once, with its cue: %+v", ts)
	}
}

// msgvault's filter is wider than the archive's match, never narrower:
// every text imText finds, the pattern finds in its lower case (as DuckDB
// gets it); a word inside another it does not.
func TestWoerterMuster(t *testing.T) {
	re := regexp.MustCompile(woerter.muster())
	for _, text := range []string{"Hallo Mama, kommst du?", "Liebe SCHWÄGERIN", "liebe schwaegerin", "Liebe Schwagerin", "kardeşim", "kardesim", "Привет мама",
		"¿Vienes, mamá?", "bonjour frere", "Oma!", "(Opa)", "Gute Nacht, Schatz ❤️", "Mama"} {
		if len(woerter.imText(text)) == 0 || !re.MatchString(strings.ToLower(text)) {
			t.Errorf("%q: imText %v, pattern %v", text, woerter.imText(text), re.MatchString(strings.ToLower(text)))
		}
	}
	for _, text := range []string{"die tomate", "europa", "omar"} {
		if re.MatchString(text) {
			t.Errorf("%q matched", text)
		}
	}
}

// Weg 1, names: a contact whose name begins with a relation word gets
// the suggestion; a word that is also a name does not.
func TestWoerterImNamen(t *testing.T) {
	for _, c := range []struct{ name, wort, art string }{
		{"Oma Hilde", "Oma", "grosseltern"},
		{"Mama", "Mama", "eltern"},
		{"Mama ❤️", "Mama", "eltern"},
		{"Schwiegermutter Hedwig", "Schwiegermutter", "schwiegereltern"},
		{"Schatzi", "Schatzi", "partner"},
		{"Tante Uschi", "Tante", "onkel"},
		{"Anne Berg", "", ""},
		{"Hala Haddad", "", ""},
		{"Hilde Oma", "", ""},
		{"Lisa Schatz", "", ""},
	} {
		wort, arten := woerter.imNamen(c.name)
		if wort != c.wort || (c.art != "" && arten[0] != c.art) {
			t.Errorf("%q: %q %v", c.name, wort, arten)
		}
	}
	if nachname("", "Oma Hilde") != "" || nachname("", "Anne Berg") != "berg" {
		t.Fatal("a relation word is no family name; a given name is not one")
	}
}

// Weg 1, group titles: the first category of the file a title has wins,
// a family word makes Familie, none without a word.
func TestWoerterImTitel(t *testing.T) {
	for _, c := range []struct{ titel, kat string }{
		{"Familie Dopp", kategorieFamilie},
		{"Fam. Berg 🏡", kategorieFamilie},
		{"Eltern Klasse 3b", "elterngruppe"},
		{"Klasse 3b", "klasse"},
		{"Abi 2005", "klasse"},
		{"Oma & Opa", kategorieFamilie},
		{"Geschwister", kategorieFamilie},
		{"FC Bayern Fans", "verein"},
		{"Team Einkauf", "arbeit"},
		{"Nachbarn Gartenweg", "nachbarschaft"},
		{"Mallorca 2025", ""},
		{"", ""},
	} {
		if _, kat := woerter.imTitel(c.titel); kat != c.kat {
			t.Errorf("%q: %q, want %q", c.titel, kat, c.kat)
		}
	}
}

// A contact saved as „Mama“, with no photo day and no conversation, is a
// question to the person, the kind chosen and the word as the reason;
// one saved as „Anne Berg“ is none.
func TestWeg1NamenFrage(t *testing.T) {
	jetzt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	st := netzTabelle{Namen: map[string]string{}, Ich: "haushalt/a",
		Kontakte: []netzKontakt{{Key: "haushalt/a", Name: "Anna Berg"}, {Key: "privat/m", Name: "Mama"}, {Key: "privat/o", Name: "Oma Hilde"}, {Key: "privat/x", Name: "Anne Wolf"}}}
	n := neuesNetz(st, nil, nil, time.UTC, func() time.Time { return jetzt })
	fs := n.Fragen()
	if got := fragenVon(fs); !slices.Equal(got, []string{"Mama>Anna Berg=eltern", "Oma Hilde>Anna Berg=grosseltern"}) {
		t.Fatalf("questions: %v", got)
	}
	if !fs[0].Ich || fs[0].Weil != "„Mama“ im Namen" {
		t.Fatalf("%+v", fs[0])
	}
}

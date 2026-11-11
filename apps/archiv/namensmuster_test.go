package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// The patterns a card's name may hold, in the family's languages; a name
// that only has „von“ or begins with a word that is also a name has none.
func TestImMuster(t *testing.T) {
	for _, c := range []struct {
		name, art, ziele string
		ueber            bool
	}{
		{"Mama von Lena", "eltern", "Lena", false},
		{"Papa vom Tom", "eltern", "Tom", false},
		{"Eltern von Lea", "eltern", "Lea", false},
		{"Sabine (Mama von Lena) 🎂", "eltern", "Lena", false},
		{"Mama von Lena und Tom", "eltern", "Lena|Tom", false},
		{"Oma von Jürgen Groß", "grosseltern", "Jürgen Groß", false},
		{"Freundin von Lena", "freund", "Lena", false},
		{"Ex-Freund von Lena", "expartner", "Lena", false},
		{"Ex Freundin von Tom", "expartner", "Tom", false},
		{"Kollegin von Uwe", "kollege", "Uwe", false},
		{"mamá de Lucía", "eltern", "Lucía", false},
		{"Peter via Lena", "", "Lena", true},
		{"Peter (über Lena Berg)", "", "Lena Berg", true},
	} {
		m, ok := woerter.imMuster(c.name)
		if !ok || m.Art != c.art || strings.Join(m.Ziele, "|") != c.ziele || m.Ueber != c.ueber {
			t.Errorf("%q: %+v %v", c.name, m, ok)
		}
	}
	for _, name := range []string{"Ursula von der Leyen", "Mama", "Oma Hilde", "Anne von Berg", "Maria de Souza", "Mama von", "Lena Berg"} {
		if m, ok := woerter.imMuster(name); ok {
			t.Errorf("%q: %+v", name, m)
		}
	}
	if w, _ := woerter.imNamen("Mama von Lena"); w != "" {
		t.Fatal("„Mama von Lena“ was asked as the person's Mama")
	}
	if nachname("", "Mama von Lena") != "" {
		t.Fatal("„Lena“ became a family name")
	}
	if slices.Contains(woerter.formen, "freundin") || slices.Contains(woerter.formen, "kollege") {
		t.Fatal("a name pattern's word sends messages to the model")
	}
}

// „Mama von Lena“ makes Lena a person when no card has her name and asks
// whether the contact is Lena's Mutter/Vater; „Papa von Tom“ finds the
// Tom Berg there is (a given name alone), „Freundin von Anna“ the person
// herself; „Peter via Lena“ is a tie with its reason and no question.
func TestNamensmusterImNetz(t *testing.T) {
	jetzt := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	st := netzTabelle{Namen: map[string]string{}, Ich: "haushalt/a",
		Kontakte: []netzKontakt{{Key: "haushalt/a", Name: "Anna Berg"}, {Key: "haushalt/t", Name: "Tom Berg"},
			{Key: "privat/s", Name: "Sabine (Mama von Lena)"}, {Key: "privat/u", Name: "Papa von Tom"}, {Key: "privat/f", Name: "Freundin von Anna Berg"},
			{Key: "privat/p", Name: "Peter via Lena"}, {Key: "privat/x", Name: "Mama von Lea und Lena"}}}
	n := neuesNetz(st, nil, nil, time.UTC, func() time.Time { return jetzt })
	lena, ok := n.Person("name:lena")
	if !ok || lena.Name != "Lena" {
		t.Fatalf("no Lena: %+v", n.knoten)
	}
	if _, ok := n.Person("name:lea"); !ok {
		t.Fatal("no Lea")
	}
	got := fragenVon(n.Fragen())
	for _, want := range []string{"Sabine (Mama von Lena)>Lena=eltern", "Papa von Tom>Tom Berg=eltern", "Freundin von Anna Berg>Anna Berg=freund",
		"Mama von Lea und Lena>Lena=eltern", "Mama von Lea und Lena>Lea=eltern"} {
		if !slices.Contains(got, want) {
			t.Errorf("no question %q: %v", want, got)
		}
	}
	if slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "Peter via Lena>") || strings.HasPrefix(s, "Lena>Peter") }) {
		t.Fatalf("„via“ asked: %v", got)
	}
	if slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "Sabine (Mama von Lena)>Anna Berg") }) {
		t.Fatalf("Sabine asked as the person's mother: %v", got)
	}
	fs := n.Fragen()
	i := slices.IndexFunc(fs, func(f Frage) bool { return f.X.Name == "Sabine (Mama von Lena)" })
	if fs[i].Weil != "„Sabine (Mama von Lena)“ im Telefonbuch" {
		t.Fatalf("the reason: %q", fs[i].Weil)
	}
	k := n.kanteZwischen("privat/p", "name:lena")
	if g := n.Gruende("privat/p", k); g != "„Peter via Lena“ im Telefonbuch" {
		t.Fatalf("via: %q", g)
	}
	if n.imBuchBeziehung("privat/s", "name:lena") {
		t.Fatal("a pattern counted as the phone book's relation")
	}
}

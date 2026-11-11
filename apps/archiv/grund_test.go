package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// grundNetz is Horst's net (owner 2026-11-11: „Warum wurde Horst eine
// Beziehung zu Christian Huesmann attestiert?“): Horst is the person;
// Christian shares only his company; Ute and Max share an address,
// Max's card names Ute as his sister, „Peter via Ute“ is known through
// her; Lisa and Jan share a company and photo days; a placeholder company
// („Privat“) and one on the cards of nine people tie nobody.
func grundNetz(antworten map[string]antwort) *Netz {
	st := netzTabelle{Namen: map[string]string{}, Ich: "h/horst"}
	st.Kontakte = []netzKontakt{
		{Key: "h/horst", Name: "Horst Brandt", Firma: "Stadtwerke Lingen"},
		{Key: "h/christian", Name: "Christian Huesmann", Firma: "Stadtwerke Lingen"},
		{Key: "h/ute", Name: "Ute Lang", Firma: "Privat"},
		{Key: "h/max", Name: "Max Lang", Firma: "Privat"},
		{Key: "h/peter", Name: "Peter via Ute"},
		{Key: "h/lisa", Name: "Lisa Berg", Person: "p-lisa", Firma: "Bäckerei Korn"},
		{Key: "h/jan", Name: "Jan Ott", Person: "p-jan", Firma: "Bäckerei Korn"},
	}
	for i := range 9 {
		st.Kontakte = append(st.Kontakte, netzKontakt{Key: fmt.Sprintf("h/b%d", i), Name: fmt.Sprintf("Bahn %d", i), Firma: "Deutsche Bahn"})
	}
	st.Namen["p-lisa"], st.Namen["p-jan"] = "Lisa Berg", "Jan Ott"
	for i, d := range []string{"2024-05-01", "2025-06-01"} {
		t, _ := time.Parse("2006-01-02", d)
		st.Fotos = append(st.Fotos, netzFoto{ID: fmt.Sprint("f", i), T: t.Add(12 * time.Hour), Stadt: "Lingen", Wer: []string{"p-lisa", "p-jan"}})
	}
	st.Fest = []festBand{
		{A: "h/horst", B: "h/christian", Art: bandFirma, Wie: "Stadtwerke Lingen"},
		{A: "h/ute", B: "h/max", Art: bandFirma}, // Privat, as a table read before Wie was kept
		{A: "h/b0", B: "h/b1", Art: bandFirma, Wie: "Deutsche Bahn"},
		{A: "h/ute", B: "h/max", Art: bandAdresse, Wie: "Mühlenweg 4"},
		{A: "h/max", B: "h/ute", Art: bandBeziehung, Wie: "Schwester"},
		{A: "h/lisa", B: "h/jan", Art: bandFirma, Wie: "Bäckerei Korn"},
	}
	return neuesNetz(st, nil, antworten, time.UTC, func() time.Time { return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) })
}

// Always say why (owner 2026-11-11): every tie that shows has a stated
// reason in its row and on its page, each phone-book reason with its
// value in plain German. Fails when a tie is belegt and says nothing.
func TestJedeKanteSagtWarum(t *testing.T) {
	n := grundNetz(nil)
	gezaehlt := 0
	for id, ks := range n.kanten {
		for _, k := range ks {
			if !k.belegt() {
				t.Fatalf("a tie without evidence: %+v", k)
			}
			gezaehlt++
			if strings.TrimSpace(n.Kurz(id, *k)) == "" {
				t.Fatalf("a row without a reason: %+v", k)
			}
			p, ok := n.Paar(k.A, k.B)
			if !ok || p.Satz == "" && len(p.Ausserdem) == 0 && p.Art == "" {
				t.Fatalf("a page without a reason: %+v", p)
			}
		}
	}
	if gezaehlt == 0 {
		t.Fatal("no tie to check")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, want := range []struct{ a, b, grund string }{
		{"h/ute", "h/max", "Gleiche Adresse auf beiden Karten: Mühlenweg 4"},
		{"h/ute", "h/max", "Max Langs Karte nennt Ute Lang als Schwester"},
		{"p-lisa", "p-jan", "Beide Kontaktkarten nennen die Firma Bäckerei Korn"},
		{"h/peter", "h/ute", "Der Kontaktname sagt: „Peter via Ute“"},
	} {
		if g := n.gruende(want.a, n.kanteZwischen(want.a, want.b), "", 1); !slices.Contains(g, want.grund) {
			t.Errorf("%s–%s: %q, want %q", want.a, want.b, g, want.grund)
		}
	}
	if g := n.buchGrund("h/horst", festBand{A: "h/horst", B: "h/max", Art: bandBeziehung, Wie: "Kollege/Kollegin"}); g != "Deine Karte nennt Max Lang als Kollege/Kollegin" {
		t.Errorf("the person's card: %q", g)
	}
	if g := n.buchGrund("h/horst", festBand{A: "h/ute", B: "h/max", Art: bandName, Wie: "Mama von Max"}); g != "Ute Langs Kontaktname sagt: „Mama von Max“" {
		t.Errorf("a card's name: %q", g)
	}
	if w := n.wessen("h/b0", "Dein", "Karte"); w != "Bahn 0s Karte" {
		t.Errorf("%q", w)
	}
}

// A shared company alone is no tie (owner 2026-11-11): Horst and
// Christian get „Kennst du Christian Huesmann?“ with the company as the
// reason, last; a placeholder or a company on many cards, no question.
// Ja makes a tie the person stated, never written to an address book;
// Nein takes the pair out for good.
func TestFirmaAlleinIstKeineKante(t *testing.T) {
	n := grundNetz(nil)
	if n.hatKante("h/horst", "h/christian") || len(n.Nahe("h/horst", 10)) != 0 {
		t.Fatalf("a company alone made a tie: %+v", n.Nahe("h/horst", 10))
	}
	if n.hatKante("h/b0", "h/b1") {
		t.Fatal("Deutsche Bahn, on nine people's cards")
	}
	fs := n.Fragen()
	var kennen []Frage
	for _, f := range fs {
		if f.Kennen {
			kennen = append(kennen, f)
		}
	}
	if len(kennen) != 1 || kennen[0].X.ID != "h/christian" || !kennen[0].Ich || kennen[0].Weil != "Beide Karten nennen die Firma Stadtwerke Lingen" || !fs[len(fs)-1].Kennen {
		t.Fatalf("only Christian, last, for Stadtwerke Lingen: %+v", kennen)
	}
	karte := (&Web{}).frageKarte(n, kennen[0], 1, false)
	for _, want := range []string{"<h2>Kennst du Christian Huesmann?</h2><p class=k>(Beide Karten nennen die Firma Stadtwerke Lingen)</p>", `value=kennen`, `>Ja</button>`, `value=fremd`, `>Nein</button>`, "kommt in kein Adressbuch"} {
		if !strings.Contains(karte, want) {
			t.Fatalf("%q missing:\n%s", want, karte)
		}
	}
	zwei := kennen[0]
	zwei.Ich, zwei.Y = false, Knoten{ID: "h/x", Name: "Horst Brandt"}
	if karte := kennenKarte(zwei, 1, false); !strings.Contains(karte, "<h2>Kennen sich Christian Huesmann und Horst Brandt?</h2>") {
		t.Fatalf("two others:\n%s", karte)
	}
	if p, ok := n.Paar("h/horst", "h/christian"); !ok || !slices.Contains(p.Ausserdem, "Beide Kontaktkarten nennen die Firma Stadtwerke Lingen") {
		t.Fatalf("„Warum?“ names the company: %+v", p)
	}

	_, antworten, err := n.Beantworten("h/christian", "h/horst", artKennen)
	if err != nil {
		t.Fatal(err)
	}
	nahe := n.Nahe("h/horst", 10)
	if len(nahe) != 1 || nahe[0].Mit.ID != "h/christian" || n.Kurz("h/horst", nahe[0].Kante) != saysKennenGesagt+" · Beide Kontaktkarten nennen die Firma Stadtwerke Lingen" {
		t.Fatalf("Ja: %+v %q", nahe, n.Kurz("h/horst", nahe[0].Kante))
	}
	if g, _ := n.Gegebenes(paarKey("h/horst", "h/christian")); gegebenSatz(g) != "Du kennst Christian Huesmann." || slices.Contains(arten, g.Art) || g.Karte.Wohin != "" {
		t.Fatalf("no kind, nothing for Homes: %+v", g)
	}
	if slices.ContainsFunc(n.Fragen(), func(f Frage) bool { return f.Kennen }) {
		t.Fatal("asked again")
	}

	nein := grundNetz(nil)
	if _, _, err := nein.Beantworten("h/christian", "h/horst", artFremd); err != nil {
		t.Fatal(err)
	}
	if nein.hatKante("h/horst", "h/christian") || slices.ContainsFunc(nein.Fragen(), func(f Frage) bool { return f.Kennen }) {
		t.Fatal("Nein: no tie, never asked again")
	}
	if wieder := grundNetz(antworten); !wieder.hatKante("h/horst", "h/christian") { // kept over a rebuild
		t.Fatal("the Ja went")
	}
}

// Shared acquaintances are context, never evidence (owner 2026-11-11:
// „Kann das auch durch gemeinsame Freunde kommen?“ — no): two people
// with five shared acquaintances and nothing of their own get no tie;
// the page names them, the strongest first.
func TestBekannteSindKeinBeleg(t *testing.T) {
	st := netzTabelle{Namen: map[string]string{"x": "Xaver", "y": "Yvonne"}}
	tag := 0
	foto := func(wer ...string) {
		tag++
		st.Fotos = append(st.Fotos, netzFoto{ID: fmt.Sprint("f", tag), T: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC).AddDate(0, 0, tag), Stadt: "Lingen", Wer: wer})
	}
	for i, name := range []string{"Anna", "Ben", "Carla", "Dora", "Emil"} {
		id := "b" + name
		st.Namen[id] = name
		for range 2 + i { // Anna the fewest days
			foto("x", id)
			foto("y", id)
		}
	}
	n := neuesNetz(st, nil, nil, time.UTC, func() time.Time { return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) })
	if n.hatKante("x", "y") {
		t.Fatal("five shared acquaintances made a tie")
	}
	p, ok := n.Paar("x", "y")
	if !ok || len(p.Bekannte) != 5 || p.Bekannte[0].Name != "Emil" || p.Bekannte[4].Name != "Anna" {
		t.Fatalf("the shared acquaintances, strongest first: %+v", p.Bekannte)
	}
	if z := bekannteZeile(n, p.Bekannte); z != `<p class=k id=bekannte>Gemeinsame Bekannte: <a href="/tresor/umfeld?mitte=bEmil">Emil</a>, <a href="/tresor/umfeld?mitte=bDora">Dora</a> und 3 weitere</p>` {
		t.Fatalf("%s", z)
	}
	if z := bekannteZeile(n, p.Bekannte[:3]); !strings.HasSuffix(z, `>Dora</a> und <a href="/tresor/umfeld?mitte=bCarla">Carla</a></p>`) {
		t.Fatalf("three are named: %s", z)
	}
	if bekannteZeile(n, nil) != "" {
		t.Fatal("none, no line")
	}
}

// The phone books' groups keep the value they share: the street as
// written, the company as written.
func TestGruppenSagenDenWert(t *testing.T) {
	ks := kartenLesen("h/", karteMit("a", "Ute Lang", "ADR:;;Mühlenweg 4;Lingen;;49808;\r\nORG:Stadtwerke Lingen\r\n")+
		karteMit("b", "Max Lang", "ADR:;;Mühlenweg 4;Lingen;;49808;\r\nORG:Stadtwerke Lingen;IT\r\n"))
	var got []string
	for _, f := range gruppen(ks) {
		got = append(got, f.Art+"="+f.Wie)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"adresse=Mühlenweg 4", "firma=Stadtwerke Lingen"}) {
		t.Fatalf("%v", got)
	}
}

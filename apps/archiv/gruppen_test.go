package main

import (
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// gruppenNetz: Anna (the person), Ben, Carla, Dora and Emil; chats
// „Familie Berg“ (Anna, Ben, Dora; a month ago), „Team Einkauf“ (Anna,
// Carla, Emil; and an older „Einkauf 2024“), „Kita Sonnenschein“ (Anna, Dora, Emil), „Alte
// Clique“ (two years ago: not active), an untitled MMS group, a mail thread.
func gruppenNetz(jetzt time.Time) netzTabelle {
	vor := func(tage int) time.Time { return jetzt.Add(-time.Duration(tage) * 24 * time.Hour) }
	return netzTabelle{Namen: map[string]string{}, Ich: "haushalt/a",
		Kontakte: []netzKontakt{{Key: "haushalt/a", Name: "Anna Berg"}, {Key: "haushalt/b", Name: "Ben Berg"}, {Key: "haushalt/c", Name: "Carla Wolf"},
			{Key: "haushalt/d", Name: "Dora Klein"}, {Key: "haushalt/e", Name: "Emil Kurz"}},
		Gespraeche: []netzGespraech{
			{ID: "1", Art: gespraechChat, Titel: "Familie Berg", Groesse: 3, Nachrichten: 50, Zuletzt: vor(30), Wer: []string{"haushalt/a", "haushalt/b", "haushalt/d"}},
			{ID: "2", Art: gespraechChat, Titel: "Team Einkauf", Groesse: 3, Nachrichten: 9, Zuletzt: vor(10), Wer: []string{"haushalt/a", "haushalt/c", "haushalt/e"}},
			{ID: "3", Art: gespraechChat, Titel: "Einkauf 2024", Groesse: 3, Nachrichten: 9, Zuletzt: vor(400), Wer: []string{"haushalt/a", "haushalt/c", "haushalt/e"}},
			{ID: "4", Art: gespraechChat, Titel: "Kita Sonnenschein", Groesse: 12, Nachrichten: 300, Zuletzt: vor(2), Wer: []string{"haushalt/a", "haushalt/d", "haushalt/e"}},
			{ID: "5", Art: gespraechChat, Titel: "Alte Clique", Groesse: 4, Nachrichten: 5, Zuletzt: vor(800), Wer: []string{"haushalt/a", "haushalt/b", "haushalt/c"}},
			{ID: "6", Art: gespraechChat, Groesse: 3, Nachrichten: 5, Zuletzt: vor(3), Wer: []string{"haushalt/a", "haushalt/b", "haushalt/c"}},
			{ID: "7", Art: gespraechMail, Groesse: 3, Nachrichten: 4, Zuletzt: vor(3), Wer: []string{"haushalt/a", "haushalt/b", "haushalt/c"}},
		}}
}

func gruppenVon(fs []Frage) (out []string) {
	for _, f := range fs {
		if f.Gruppe != nil {
			out = append(out, f.Gruppe.Titel+"="+f.Gruppe.Vorschlag)
		}
	}
	return out
}

// Active groups are asked, newest first, after the person's own pairs:
// a title, the members the phone books know, a message in the last year;
// the title's category suggested. Never an untitled or old group, a mail
// thread, or one already named; Später last.
func TestGruppenFragen(t *testing.T) {
	jetzt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	n := neuesNetz(gruppenNetz(jetzt), nil, nil, time.UTC, func() time.Time { return jetzt })
	fs := n.Fragen()
	if got := gruppenVon(fs); !slices.Equal(got, []string{"Kita Sonnenschein=elterngruppe", "Team Einkauf=arbeit", "Familie Berg=familie"}) {
		t.Fatalf("groups: %v", got)
	}
	i := slices.IndexFunc(fs, func(f Frage) bool { return f.Gruppe != nil })
	if slices.ContainsFunc(fs[i:], func(f Frage) bool { return f.Ich }) {
		t.Fatalf("a question about „du“ after a group: %v", fragenVon(fs))
	}
	if g := fs[i].Gruppe; g.Wort != "Kita" || len(g.Mitglieder) != 3 || g.Mitglieder[0].Name != "Anna Berg" {
		t.Fatalf("%+v", g)
	}
	if _, _, err := n.Einordnen("5", "freundeskreis"); err != errFrageWeg {
		t.Fatalf("an old group: %v", err)
	}
	if _, _, err := n.Einordnen("4", "sekte"); err != errFrageWeg {
		t.Fatalf("no such category: %v", err)
	}
	if _, _, err := n.Einordnen("4", artSpaeter); err != nil {
		t.Fatal(err)
	}
	if got := gruppenVon(n.Fragen()); !slices.Equal(got, []string{"Team Einkauf=arbeit", "Familie Berg=familie", "Kita Sonnenschein=elterngruppe"}) {
		t.Fatalf("Später last: %v", got)
	}
	jetzt2, weitere := n.Portion(jetzt.Add(-time.Hour))
	if slices.ContainsFunc(jetzt2, func(f Frage) bool { return f.Gruppe != nil && f.Gruppe.ID == "4" }) || !slices.ContainsFunc(weitere, func(f Frage) bool { return f.Gruppe != nil && f.Gruppe.ID == "4" }) {
		t.Fatal("Später in this visit waits behind the portion")
	}
	if _, _, err := n.Einordnen("2", "arbeit"); err != nil {
		t.Fatal(err)
	}
	if got := gruppenVon(n.Fragen()); slices.Contains(got, "Team Einkauf=arbeit") {
		t.Fatalf("named, still asked: %v", got)
	}
	if e := n.Eingeordnete(); len(e) != 1 || eingeordnetSatz(e[0]) != "„Team Einkauf“ ist für dich: Arbeit." {
		t.Fatalf("%+v", e)
	}
}

// What a category does: Familie (the person's) puts the members into the
// family circle, so their pairs are asked with „Sonstige verwandt“;
// Arbeit gives „Kollege/Kollegin“, Elterngruppe „Eltern aus …“ as reasons
// on Steht nahe; a title's category counts until the person says
// otherwise; Sonstiges says nothing.
func TestGruppenBrotkruemel(t *testing.T) {
	jetzt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	n := neuesNetz(gruppenNetz(jetzt), nil, nil, time.UTC, func() time.Time { return jetzt })
	if slices.Contains(fragenVon(n.Fragen()), "Ben Berg>Dora Klein=verwandt") {
		t.Fatal("a title alone widened the circle")
	}
	carla := slices.IndexFunc(n.Nahe("haushalt/a", 10), func(v Verbindung) bool { return v.Mit.Name == "Carla Wolf" })
	if carla < 0 {
		t.Fatalf("Carla: %v", namenVon(n.Nahe("haushalt/a", 10)))
	}
	if g := n.Gruende("haushalt/a", n.Nahe("haushalt/a", 10)[carla].Kante); g != "in 4 Gruppen-Chats · 4 Mails zusammen · Kollege/Kollegin (Gruppe „Team Einkauf“) · Freundeskreis „Alte Clique“" {
		t.Fatalf("Anna–Carla: %q", g)
	}
	if _, _, err := n.Einordnen("1", kategorieFamilie); err != nil {
		t.Fatal(err)
	}
	fs := n.Fragen()
	i := slices.IndexFunc(fs, func(f Frage) bool {
		return f.X.Name+f.Y.Name == "Ben BergDora Klein" || f.X.Name+f.Y.Name == "Dora KleinBen Berg"
	})
	if i < 0 || fs[i].Vorschlag != "verwandt" || fs[i].Weil != "beide in der Gruppe „Familie Berg“ (für dich: Familie)" {
		t.Fatalf("Familie: a question among its members: %v", fragenVon(fs))
	}
	if _, _, err := n.Einordnen("4", "elterngruppe"); err != nil {
		t.Fatal(err)
	}
	k := n.kanteZwischen("haushalt/d", "haushalt/e")
	if g := n.Gruende("haushalt/d", k); !strings.Contains(g, "Eltern aus „Kita Sonnenschein“") {
		t.Fatalf("Dora–Emil: %q", g)
	}
	if _, _, err := n.Einordnen("2", kategorieSonst); err != nil {
		t.Fatal(err)
	}
	if g := n.Gruende("haushalt/a", n.Nahe("haushalt/a", 10)[carla].Kante); strings.Contains(g, "Team Einkauf") {
		t.Fatalf("Sonstiges still a reason: %q", g)
	}
}

// One tap names a group in the vault, nothing goes to Homes; the toast
// says so and Rückgängig takes it back; the card shows the title and the
// members' initials, never a message.
func TestGruppeEinTipp(t *testing.T) {
	wt, ut, homes, innen := eintragenBox(t)
	tab, err := tabelleLaden(innen)
	if err != nil {
		t.Fatal(err)
	}
	tab.Gespraeche = []netzGespraech{{ID: "9", Art: gespraechChat, Titel: "Familie Berg", Groesse: 3, Nachrichten: 40, Zuletzt: ut.jetzt.Add(-48 * time.Hour),
		Wer: []string{"haushalt/a", "haushalt/b", "privat/urn:uuid:k2"}}}
	if err := jsonImTresor(innen.umfeldTabelle(), tab); err != nil {
		t.Fatal(err)
	}
	ut.u.laden(innen)
	fs, _ := ut.u.Netz().Portion(*ut.jetzt)
	i := slices.IndexFunc(fs, func(f Frage) bool { return f.Gruppe != nil })
	if i != 1 {
		t.Fatalf("the group after the person's own question: %v", fragenVon(fs))
	}
	karte := gruppenKarte(*fs[i].Gruppe, 5, false)
	for _, want := range []string{"Wie würdest du die Gruppe „Familie Berg“ nennen?", `name=gruppe value="9"`, `value=familie class="chip vorschlag"`, ">Elterngruppe<",
		"Vorschlag, weil „Familie“ im Namen der Gruppe steht.", esc(saysGruppeBleibt), ">AB<", ">BB<", ">FK<", "Später"} {
		if !strings.Contains(karte, want) {
			t.Fatalf("the card lacks %q:\n%s", want, karte)
		}
	}
	rec := wt.rufe("POST", antwortPfad, anna, url.Values{"gruppe": {"9"}, "art": {kategorieFamilie}, "zurueck": {"/tresor"}})
	_, seite := wt.folgen(t, rec)
	if !strings.Contains(seite, esc("✓ „Familie Berg“ ist für dich: Familie · gemerkt, kommt in kein Adressbuch")) {
		t.Fatalf("toast:\n%s", seite)
	}
	if len(homes.genommen()) != 0 {
		t.Fatalf("a group went to Homes: %+v", homes.genommen())
	}
	if g := gruppenLaden(innen); g["9"].Kategorie != kategorieFamilie {
		t.Fatalf("not in the vault: %+v", g)
	}
	if fi, err := os.Stat(innen.umfeldGruppen()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if liste := wt.rufe("GET", "/tresor/umfeld", anna, nil).Body.String(); !strings.Contains(liste, "Deine Gruppen") || !strings.Contains(liste, esc("„Familie Berg“ ist für dich: Familie.")) {
		t.Fatalf("Deine Gruppen:\n%s", liste)
	}
	rec = wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {gruppeKey("9")}, "tu": {"vergessen"}, "zurueck": {"/tresor"}})
	wt.folgen(t, rec)
	if g := gruppenLaden(innen); len(g) != 0 || !slices.ContainsFunc(ut.u.Netz().Fragen(), func(f Frage) bool { return f.Gruppe != nil }) {
		t.Fatalf("Rückgängig: %+v", g)
	}
	if len(homes.genommen()) != 0 {
		t.Fatal("Rückgängig went to Homes")
	}
}

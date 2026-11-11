package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// viele are eight contacts who each write the person three messages with
// a relation word: 24 candidates, more than two portions of modellJeSchub.
func viele(t *testing.T) (*umfeldTest, *fakeMsgvaultHinweise) {
	ut := netzBox(t)
	mv, api := neuesMsgvaultHinweise(t)
	for i := 1; i <= 8; i++ {
		ut.rad.haushalt[fmt.Sprintf("v%d.vcf", i)] = karteMit(fmt.Sprintf("v%d", i), fmt.Sprintf("Vera Viel%d", i), fmt.Sprintf("TEL:0160 100000%d\r\n", i))
		for j := 1; j <= 3; j++ {
			mv.setzen(hinweisZeile(fmt.Sprintf("%d%d", i, j), false, fmt.Sprintf("Hallo Schatz, Nummer %d", j), "von", fmt.Sprintf("+49160100000%d", i)))
		}
	}
	ut.u.Msgvault = api
	return ut, mv
}

// Faster (archiv#42): while the vault is open a round reads every
// candidate, not ten an hour; the stand counts what was read and what
// waits. A lease taken in the middle stops it at once, the rest waits
// for a later round, nothing is lost or sent twice; the log has counts only.
func TestHinweiseSchneller(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(io.MultiWriter(&logs, os.Stderr))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	ut, _ := viele(t)
	mo, modell := neuesModell(t)
	mo.antwort = func(string) string { return `{"beziehungen":[]}` }
	ut.u.Modell = modell
	ut.u.Runde(t.Context(), ut.innen)
	if n := len(mo.gefragt()); n != 24 {
		t.Fatalf("asked %d in one round, want all 24", n)
	}
	if st := ut.u.Stand(); st.ModellGelesen != 24 || st.ModellOffen != 0 || !st.ModellGezaehlt {
		t.Fatalf("stand: %+v", st)
	}

	ut2, _ := viele(t)
	mo2, modell2 := neuesModell(t)
	gefragt := 0
	mo2.antwort = func(string) string {
		if gefragt++; gefragt == 12 {
			mo2.lease = "ready" // Homes takes the card (the handler holds mo2.mu)
		}
		return `{"beziehungen":[]}`
	}
	ut2.u.Modell = modell2
	ut2.u.Runde(t.Context(), ut2.innen)
	if n := len(mo2.gefragt()); n != 12 || len(hinweiseLaden(ut2.innen)) != 12 {
		t.Fatalf("a lease did not stop the round: %d asked", n)
	}
	if st := ut2.u.Stand(); st.ModellGelesen != 12 || st.ModellOffen != 12 {
		t.Fatalf("stand: %+v", st)
	}
	mo2.mu.Lock()
	mo2.lease = "none"
	mo2.mu.Unlock()
	ut2.u.Runde(t.Context(), ut2.innen)
	if n := len(mo2.gefragt()); n != 24 || len(hinweiseLaden(ut2.innen)) != 24 {
		t.Fatalf("the rest not read later: %d", n)
	}
	if !strings.Contains(logs.String(), "12 warten") {
		t.Fatalf("the log: %s", logs.String())
	}
	for _, geheim := range []string{"Schatz", "Nummer", "Vera"} {
		if strings.Contains(logs.String(), geheim) {
			t.Errorf("the log carries %q", geheim)
		}
	}
}

// Groups by the model (archiv#42): an active group the person has not
// named goes to the model with its title, its members and its newest
// messages; a sure reading is its suggestion, before the title's; an
// unsure or unreadable one is none. No group is sent twice; none while a
// lease holds the card.
func TestGruppenModell(t *testing.T) {
	jetzt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ut := netzBox(t)
	n := neuesNetz(gruppenNetz(jetzt), nil, nil, time.UTC, func() time.Time { return jetzt })
	mv, api := neuesMsgvaultHinweise(t)
	mv.proben = [][]any{{"1", "Wer bringt Kuchen mit?"}, {"1", "Oma kommt um drei"}, {"2", "Training morgen um 18 Uhr"}, {"4", "Elternabend"}}
	mo, modell := neuesModell(t)
	mo.antwort = func(frage string) string {
		switch {
		case strings.Contains(frage, "„Familie Berg“"):
			return `{"kategorie":"familie","sicher":0.9}`
		case strings.Contains(frage, "„Team Einkauf“"):
			return "```json\n{\"kategorie\":\"verein\",\"sicher\":0.85}\n```"
		}
		return `{"kategorie":"klasse","sicher":0.3}`
	}
	ut.u.Msgvault, ut.u.Modell = api, modell
	ut.u.gruppenRunde(t.Context(), ut.innen, n)
	fragen := mo.gefragt()
	if len(fragen) != 3 {
		t.Fatalf("asked %d groups, want the 3 active: %q", len(fragen), fragen)
	}
	i := slices.IndexFunc(fragen, func(f string) bool { return strings.Contains(f, "Familie Berg") })
	t.Logf("the prompt:\n%s", fragen[i])
	for _, want := range []string{"Gruppe: „Familie Berg“", "- Ben Berg", "- die Person selbst", "«Wer bringt Kuchen mit?»", "«Oma kommt um drei»"} {
		if !strings.Contains(fragen[i], want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	if got := gruppenVon(n.Fragen()); !slices.Equal(got, []string{"Kita Sonnenschein=elterngruppe", "Team Einkauf=verein", "Familie Berg=familie"}) {
		t.Fatalf("suggestions: %v", got)
	}
	fs := n.Fragen()
	j := slices.IndexFunc(fs, func(f Frage) bool { return f.Gruppe != nil && f.Gruppe.ID == "2" })
	if !fs[j].Gruppe.Modell || !strings.Contains(gruppenKarte(*fs[j].Gruppe, 3, false), esc(saysGruppeModell)) {
		t.Fatalf("the model's suggestion not said: %+v", fs[j].Gruppe)
	}
	if gm := gruppenModellLaden(ut.innen); len(gm) != 3 || gm["4"].Kategorie != "klasse" || gm["4"].Sicher != 0.3 {
		t.Fatalf("the vault keeps: %+v", gm)
	}
	ut.u.gruppenRunde(t.Context(), ut.innen, n)
	if len(mo.gefragt()) != 3 {
		t.Fatal("a group sent twice")
	}
	n2 := neuesNetz(gruppenNetz(jetzt), nil, nil, time.UTC, func() time.Time { return jetzt })
	os.Remove(ut.innen.umfeldGruppenModell())
	mo.mu.Lock()
	mo.lease = "ready"
	mo.mu.Unlock()
	ut.u.gruppenRunde(t.Context(), ut.innen, n2)
	if len(mo.gefragt()) != 3 {
		t.Fatal("asked while a lease holds the card")
	}
}

// The model's answer about a group is read strictly.
func TestGruppenAntwortLesen(t *testing.T) {
	for _, c := range []struct {
		antwort, kat string
	}{
		{`{"kategorie":"familie","sicher":0.9}`, "familie"},
		{"```json\n{\"kategorie\":\"Arbeit\",\"sicher\":1}\n```", "arbeit"},
		{`{"kategorie":"sekte","sicher":0.9}`, ""},
		{`{"kategorie":"familie"}`, ""},
		{`{"kategorie":"familie","sicher":2}`, ""},
		{`{"kategorie":"familie","sicher":0.9} Gern!`, ""},
		{`Das ist eine Familie.`, ""},
	} {
		if got := gruppenAntwortLesen(c.antwort); got.Kategorie != c.kat {
			t.Errorf("%q: %+v", c.antwort, got)
		}
	}
}

// Groups by hand (archiv#42): any group of the list, old ones too, at
// any time, one of the chips or the person's own word; her words come
// back as chips and as reasons; Rückgängig gives back what a change
// replaced. Untitled groups and mail threads are not on the list.
func TestGruppeVonHand(t *testing.T) {
	jetzt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	n := neuesNetz(gruppenNetz(jetzt), nil, nil, time.UTC, func() time.Time { return jetzt })
	var titel []string
	for _, z := range n.Gruppenliste() {
		titel = append(titel, z.Titel)
	}
	if !slices.Equal(titel, []string{"Kita Sonnenschein", "Team Einkauf", "Familie Berg", "Einkauf 2024", "Alte Clique"}) {
		t.Fatalf("the list: %v", titel)
	}
	if _, err := n.GruppeSetzen("5", "freundeskreis", ""); err != nil {
		t.Fatalf("an old group: %v", err)
	}
	for _, c := range [][3]string{{"6", "familie", ""}, {"7", "familie", ""}, {"2", kategorieEigen, "  "}, {"2", "sekte", ""}} {
		if _, err := n.GruppeSetzen(c[0], c[1], c[2]); err != errFrageWeg {
			t.Errorf("%v: %v", c, err)
		}
	}
	if _, err := n.GruppeSetzen("2", kategorieEigen, "  Doppel  kopf "); err != nil {
		t.Fatal(err)
	}
	if e, _ := n.Eingeordnetes("2"); eingeordnetSatz(e) != "„Team Einkauf“ ist für dich: Doppel kopf." {
		t.Fatalf("%+v", e)
	}
	if got := n.EigeneKategorien(); !slices.Equal(got, []string{"Doppel kopf"}) {
		t.Fatalf("own words: %v", got)
	}
	carla := slices.IndexFunc(n.Nahe("haushalt/a", 10), func(v Verbindung) bool { return v.Mit.Name == "Carla Wolf" })
	if g := n.Gruende("haushalt/a", n.Nahe("haushalt/a", 10)[carla].Kante); !strings.Contains(g, "Doppel kopf (Gruppe „Team Einkauf“)") {
		t.Fatalf("the own word as a reason: %q", g)
	}
	if _, err := n.GruppeSetzen("2", "arbeit", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Ausordnen("2"); err != nil {
		t.Fatal(err)
	}
	if e, _ := n.Eingeordnetes("2"); e.Wort != "Doppel kopf" {
		t.Fatalf("Rückgängig did not give back the own word: %+v", e)
	}
	n.Ausordnen("2")
	if _, da := n.Eingeordnetes("2"); da {
		t.Fatal("not forgotten")
	}
}

// The tab „Gruppen“ lists the groups, a tap names one, her own word is
// kept and offered again; the toast's Rückgängig takes it back.
func TestGruppenTab(t *testing.T) {
	wt, ut, homes, innen := eintragenBox(t)
	tab, err := tabelleLaden(innen)
	if err != nil {
		t.Fatal(err)
	}
	tab.Gespraeche = []netzGespraech{{ID: "9", Art: gespraechChat, Titel: "Skatrunde", Groesse: 4, Nachrichten: 40, Zuletzt: ut.jetzt.Add(-48 * time.Hour),
		Wer: []string{"haushalt/a", "haushalt/b", "privat/urn:uuid:k2"}}}
	if err := jsonImTresor(innen.umfeldTabelle(), tab); err != nil {
		t.Fatal(err)
	}
	ut.u.laden(innen)
	page := wt.rufe("GET", "/tresor?tab=gruppen", anna, nil).Body.String()
	for _, want := range []string{`aria-current=page>Gruppen`, `<summary>Skatrunde</summary>`, "noch nicht eingeordnet", `name=liste value=1`, `name=eigen`, esc(saysGruppenTab)} {
		if !strings.Contains(page, want) {
			t.Fatalf("the tab lacks %q:\n%s", want, page)
		}
	}
	rec := wt.rufe("POST", antwortPfad, anna, url.Values{"gruppe": {"9"}, "liste": {"1"}, "art": {kategorieEigen}, "eigen": {"Skat"}, "zurueck": {gruppenTabPfad}})
	ziel, seite := wt.folgen(t, rec)
	if !strings.HasPrefix(ziel, "/tresor?") || !strings.Contains(ziel, "tab=gruppen") || !strings.Contains(seite, esc("✓ „Skatrunde“ ist für dich: Skat")) {
		t.Fatalf("%s:\n%s", ziel, seite)
	}
	if !strings.Contains(seite, `value="eigen:Skat" class="chip gewaehlt"`) {
		t.Fatalf("the own word not a chip:\n%s", seite)
	}
	if g := gruppenLaden(innen); g["9"].Kategorie != kategorieEigen || g["9"].Name != "Skat" {
		t.Fatalf("the vault: %+v", g)
	}
	rec = wt.rufe("POST", antwortPfad, anna, url.Values{"gruppe": {"9"}, "liste": {"1"}, "art": {"verein"}, "zurueck": {gruppenTabPfad}})
	wt.folgen(t, rec)
	rec = wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {gruppeKey("9")}, "tu": {"vergessen"}, "zurueck": {gruppenTabPfad}})
	wt.folgen(t, rec)
	if g := gruppenLaden(innen); g["9"].Name != "Skat" {
		t.Fatalf("Rückgängig: %+v", g)
	}
	if len(homes.genommen()) != 0 {
		t.Fatal("a group went to Homes")
	}
}

// What was recognised: one line under Fragen, leading to the
// suggestions; a card's name that makes a person („Mama von Lena“, no
// Lena in the book) is „Lena anlegen?“, and one tap makes her and the
// relation and comes back to the list with „Lena angelegt, Mama: …“ and
// Rückgängig, never to the edge page (owner 2026-11-11: „Das Verhältnis
// sollte aber direkt da sein“); „Ansehen“ opens the edge page, whose
// Fertig leads back here.
func TestErkannt(t *testing.T) {
	wt, ut, _, innen := eintragenBox(t)
	tab, err := tabelleLaden(innen)
	if err != nil {
		t.Fatal(err)
	}
	tab.Kontakte = append(tab.Kontakte, netzKontakt{Key: "privat/s", Name: "Mama von Lena"})
	if err := jsonImTresor(innen.umfeldTabelle(), tab); err != nil {
		t.Fatal(err)
	}
	ut.u.laden(innen)
	page := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if !strings.Contains(page, `<a href="/tresor/erkannt">Erkannt: 1 Vorschlag</a>`) {
		t.Fatalf("the line:\n%s", page)
	}
	page = wt.rufe("GET", erkanntPfad, anna, nil).Body.String()
	ansehen := kanteVon("privat/s", "name:lena", vonErkannt)
	for _, want := range []string{"Beziehungen (1)", esc("Lena anlegen?"), esc("Mama: „Mama von Lena“ aus deinem Telefonbuch."), ">Anlegen<",
		`<input type=hidden name=zurueck value="/tresor/erkannt">`, `href="` + esc(ansehen) + `">Ansehen<`, esc(saysErkanntRegel)} {
		if !strings.Contains(page, want) {
			t.Fatalf("the page lacks %q:\n%s", want, page)
		}
	}
	kante := wt.rufe("GET", ansehen, anna, nil).Body.String()
	if !strings.Contains(kante, `<a href="/tresor/erkannt">Fertig, zurück zu Erkannt</a>`) || !strings.Contains(kante, `name=zurueck value="`+esc(ansehen)+`"`) {
		t.Fatalf("the edge page has no way back:\n%s", kante)
	}
	rec := wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"privat/s"}, "y": {"name:lena"}, "art": {"eltern"}, "zurueck": {erkanntPfad}})
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), erkanntPfad+"?") {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	if g, da := ut.u.Netz().Gegebenes(paarKey("privat/s", "name:lena")); !da || g.Art != "eltern" || g.X.ID != "privat/s" {
		t.Fatalf("not kept: %+v", g)
	}
	ziel, page := wt.folgen(t, rec)
	if !strings.HasPrefix(ziel, erkanntPfad) || !strings.Contains(page, esc("✓ Lena angelegt, Mama: „Mama von Lena“ · ")) || !strings.Contains(page, ">Rückgängig<") ||
		!strings.Contains(page, esc(saysErkanntLeer)) {
		t.Fatalf("not back on the list with the confirmation (%s):\n%s", ziel, page)
	}
	// the same yes as a question card goes on with the next question, with the same line
	wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {paarKey("privat/s", "name:lena")}, "tu": {"vergessen"}, "zurueck": {erkanntPfad}}))
	ziel, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"privat/s"}, "y": {"name:lena"}, "art": {"eltern"}, "zurueck": {"/tresor"}}))
	if !strings.HasPrefix(ziel, "/tresor?") || !strings.Contains(page, esc("✓ Lena angelegt, Mama: „Mama von Lena“ · ")) {
		t.Fatalf("the question's yes (%s):\n%s", ziel, page)
	}
}

func wunschZeile(id string, mir bool, am, text, rolle, tel string) []any {
	return []any{id, "chat", am, mir, text, rolle, tel, ""}
}

// Birthdays from wishes: a wish to one person alone on the same day in
// two years is that day; a belated one, one to a group, one year with one
// sender is none. Stimmt keeps it in the vault, Stimmt nicht keeps that it
// is not; neither goes to Homes.
func TestGeburtstageAusWuenschen(t *testing.T) {
	ut := netzBox(t)
	ut.rad.haushalt["b.vcf"] = karteMit("b", "Ben Berg", "TEL:0152 2222 2222\r\n")
	ut.rad.haushalt["k.vcf"] = karteMit("k", "Kim Berg", "TEL:0153 3333 3333\r\n")
	ut.rad.haushalt["c.vcf"] = karteMit("c", "Carla Wolf", "TEL:0154 4444 4444\r\n")
	mv, api := neuesMsgvaultHinweise(t)
	mv.wuensche = [][]any{
		wunschZeile("1", false, "2024-03-14T08:00:00Z", "Alles Gute zum Geburtstag, Anna!", "von", "+4915333333333"),
		wunschZeile("2", false, "2025-03-14T09:00:00Z", "Happy Birthday 🎉", "von", "+4915333333333"),
		wunschZeile("3", true, "2023-07-02T10:00:00Z", "Alles Liebe zum Geburtstag!", "an", "+4915222222222"),
		wunschZeile("4", true, "2025-07-02T10:00:00Z", "happy birthday bro", "an", "+4915222222222"),
		wunschZeile("5", true, "2024-07-05T10:00:00Z", "Alles Gute nachträglich zum Geburtstag", "an", "+4915222222222"),
		wunschZeile("6", true, "2024-07-05T10:00:00Z", "Alles Gute zum Geburtstag", "an", "+4915222222222"),
		wunschZeile("7", false, "2024-09-09T10:00:00Z", "Alles Gute zum Geburtstag euch", "von", "+4915333333333"),
		wunschZeile("7", false, "2024-09-09T10:00:00Z", "Alles Gute zum Geburtstag euch", "an", "+4915222222222"),
		wunschZeile("7", false, "2024-09-09T10:00:00Z", "Alles Gute zum Geburtstag euch", "an", "+4915444444444"),
		wunschZeile("8", true, "2024-10-10T10:00:00Z", "Alles Gute zum Geburtstag", "an", "+4915444444444"),
	}
	ut.u.Msgvault = api
	ut.u.Runde(t.Context(), ut.innen)
	var got []string
	for _, v := range ut.u.Netz().GeburtstagVorschlaege() {
		got = append(got, fmt.Sprintf("%s=%s/%d/%v", v.Wer.Name, v.Tag, v.Belege, v.Ich))
	}
	if !slices.Equal(got, []string{"Anna Berg=03-14/2/true", "Ben Berg=07-02/2/false"}) {
		t.Fatalf("suggestions: %v", got)
	}
	if h := gebHinweiseLaden(ut.innen); len(h) != 2 {
		t.Fatalf("the vault: %+v", h)
	}
	if tagText("07-02") != "2. Juli" || gebGrund(ut.u.Netz().GeburtstagVorschlaege()[1]) != "2 Glückwünsche an diesem Tag, 2023 bis 2025" {
		t.Fatal(tagText("07-02"), gebGrund(ut.u.Netz().GeburtstagVorschlaege()[1]))
	}

	wt, ut2, homes, innen := eintragenBox(t)
	b, _ := os.ReadFile(ut.innen.umfeldGebHinweise())
	os.WriteFile(innen.umfeldGebHinweise(), b, 0o600)
	ut2.u.laden(innen)
	page := wt.rufe("GET", erkanntPfad, anna, nil).Body.String()
	for _, want := range []string{"Geburtstage (2)", esc("Ben Berg: 2. Juli?"), esc("Dein Geburtstag: 14. März?"), ">Stimmt nicht<"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the page lacks %q:\n%s", want, page)
		}
	}
	rec := wt.rufe("POST", erkanntPfad+"/geburtstag", anna, url.Values{"id": {"p-ben"}, "tag": {"07-02"}, "ja": {"1"}})
	_, page = wt.folgen(t, rec)
	if !strings.Contains(page, meldungen["geb-ja"]) || !strings.Contains(page, "Bestätigte Geburtstage (1)") || strings.Contains(page, esc("Ben Berg: 2. Juli?")) {
		t.Fatalf("Stimmt:\n%s", page)
	}
	rec = wt.rufe("POST", erkanntPfad+"/geburtstag", anna, url.Values{"id": {"p-anna"}, "tag": {"03-14"}, "ja": {"0"}})
	wt.folgen(t, rec)
	if g := geburtstageLaden(innen); !g["p-ben"].Ja || g["p-anna"].Ja || g["p-anna"].Tag != "03-14" {
		t.Fatalf("the vault: %+v", g)
	}
	if len(ut2.u.Netz().GeburtstagVorschlaege()) != 0 {
		t.Fatal("an answered birthday suggested again")
	}
	if rec := wt.rufe("POST", erkanntPfad+"/geburtstag", anna, url.Values{"id": {"p-ben"}, "tag": {"01-01"}, "ja": {"1"}}); rec.Code != 409 {
		t.Fatalf("a day not suggested: %d", rec.Code)
	}
	if len(homes.genommen()) != 0 {
		t.Fatal("a birthday went to Homes")
	}
}

// `archiv umfeld-gruppen`: the statement takes msgvault's ids only, and
// the answer reads as a round reads it, without a text.
func TestGruppenWerkzeug(t *testing.T) {
	var aus bytes.Buffer
	if gruppenWerkzeug([]string{"-sql", "12", "1; DROP", "7"}, nil, &aus) != 0 || !strings.Contains(aus.String(), "IN (12, 7)") || strings.Contains(aus.String(), "DROP") {
		t.Fatalf("%s", aus.String())
	}
	aus.Reset()
	ein := strings.NewReader(`{"columns":["gespraech","text"],"rows":[["12","Kuchen?"],["12","Ja"],["7",null]]}`)
	if gruppenWerkzeug(nil, ein, &aus) != 0 || aus.String() != "12 texte=2\n" {
		t.Fatalf("%q", aus.String())
	}
}

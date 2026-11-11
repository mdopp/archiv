package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// grossesNetz is a net of the box's real size: personen named people on
// photos, each tied to the next je (two photo days per pair), so
// personen×je ties. Person 0 is the person; 1 is „Jürgen Müller“, whom
// her phone book calls „Papa“; 2 is „Björn Weiß“; a card without a face
// is „Juergen Burger“.
func grossesNetz(personen, je int) *Netz {
	st := netzTabelle{Namen: map[string]string{}, Ich: "privat/ich"}
	id := func(i int) string { return fmt.Sprintf("p%03d", i%personen) }
	for i := range personen {
		st.Namen[id(i)] = fmt.Sprintf("Vorname%d Familie%d", i, i%37)
	}
	st.Namen[id(0)], st.Namen[id(1)], st.Namen[id(2)] = "Anna Berg", "Jürgen Müller", "Björn Weiß"
	st.Kontakte = []netzKontakt{{Key: "privat/ich", Name: "Anna Berg", Person: id(0)}, {Key: "privat/papa", Name: "Papa", Person: id(1)}, {Key: "privat/jb", Name: "Juergen Burger"}}
	tag := time.Date(2015, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range personen {
		for d := 1; d <= je; d++ {
			for k := range 2 {
				st.Fotos = append(st.Fotos, netzFoto{ID: fmt.Sprintf("f%d-%d-%d", i, d, k), T: tag.AddDate(0, 0, (i*7+d*13+k*400)%4000), Stadt: fmt.Sprintf("Ort %d/%d", i, d), Wer: []string{id(i), id(i + d)}})
			}
		}
	}
	return neuesNetz(st, nil, nil, time.UTC, func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) })
}

// One field finds any person by name (owner 2026-11-11: „Ich kann nicht
// nach einer Person suchen“): umlauts in every spelling, part of a name,
// the phone book's name for somebody Immich names otherwise; an exact
// name first, then a word that starts so, the closer first. And fast at
// the box's size: 400 persons, 6,000 ties.
func TestPersonSuchen(t *testing.T) {
	n := grossesNetz(400, 15)
	if p, k := n.Groesse(); p != 400 || k != 6000 {
		t.Fatalf("the net: %d persons, %d ties", p, k)
	}
	for _, c := range []struct{ q, erster, als string }{
		{"Jürgen Müller", "Jürgen Müller", ""}, {"juergen mueller", "Jürgen Müller", ""}, {"Jurgen Muller", "Jürgen Müller", ""}, {"MÜLLER", "Jürgen Müller", ""},
		{"jür", "Jürgen Müller", ""}, {"müll jür", "Jürgen Müller", ""}, {"papa", "Jürgen Müller", "Papa"}, {"Pap", "Jürgen Müller", "Papa"},
		{"bjorn", "Björn Weiß", ""}, {"Weiss", "Björn Weiß", ""}, {"weiß b", "Björn Weiß", ""}, {"Jürgen Burger", "Juergen Burger", ""}, {"burger", "Juergen Burger", ""},
		{"du", "Anna Berg", ""}, {"ich", "Anna Berg", ""}, {"Vorname17", "Vorname17 Familie17", ""}, {"vorname399 familie", "Vorname399 Familie29", ""},
	} {
		ts, _ := n.Suchen(c.q, sucheHoechstens)
		if len(ts) == 0 || ts[0].Name != c.erster || ts[0].Als != c.als {
			t.Fatalf("%q finds %+v, not %q (as %q)", c.q, ts, c.erster, c.als)
		}
	}
	// „Jürgen“ is two people: the one tied to her first, the card after; nobody twice
	ts, mehr := n.Suchen("jürgen", sucheHoechstens)
	if len(ts) != 2 || ts[0].Name != "Jürgen Müller" || ts[1].Name != "Juergen Burger" || mehr {
		t.Fatalf("jürgen: %+v", ts)
	}
	// a word that starts so before one that only holds it; more than fit are cut and said
	ts, mehr = n.Suchen("familie1", sucheHoechstens)
	if len(ts) != sucheHoechstens || !mehr {
		t.Fatalf("familie1: %d, more %t", len(ts), mehr)
	}
	for _, q := range []string{"", "  ", "…", "Niemand Hier", "jürgen xyz"} {
		if ts, _ := n.Suchen(q, sucheHoechstens); len(ts) != 0 {
			t.Fatalf("%q finds %+v", q, ts)
		}
	}
	// her other selves are her, once
	if ts, _ := n.Suchen("Anna Berg", sucheHoechstens); len(ts) != 1 || ts[0].ID != n.Ich() {
		t.Fatalf("Anna Berg: %+v", ts)
	}

	const runden = 200
	start := time.Now()
	for i := range runden {
		n.Suchen([]string{"jür", "familie1", "vorname3", "papa", "weiss b"}[i%5], sucheHoechstens)
	}
	if je := time.Since(start) / runden; je > 20*time.Millisecond {
		t.Fatalf("a search takes %s at 400 persons and 6,000 ties", je)
	} else {
		t.Logf("a search: %s", je)
	}
}

// The field on the pages: on the Umfeld page and on „Steht nahe“; one
// person is opened at once, several are offered with what each is to her,
// nobody leaves ways on; the search on top finds persons too.
func TestPersonSuchenSeite(t *testing.T) {
	wt, _, _, _, _ := kanteBox(t)
	feld := `<form method=get action="/tresor/umfeld" role=search class=person-suche data-weiter><input type=search name=suche list=namen data-namen`
	for _, pfad := range []string{"/tresor/umfeld", "/tresor?tab=nahe", "/tresor/umfeld?mitte=p-ben", "/tresor/umfeld?suche=berg"} {
		if page := wt.rufe("GET", pfad, anna, nil).Body.String(); strings.Count(page, feld) != 1 || strings.Count(page, "<datalist id=namen>") != 1 {
			t.Fatalf("%s: the field %d times, the names %d times:\n%s", pfad, strings.Count(page, feld), strings.Count(page, "<datalist id=namen>"), page)
		}
	}
	for q, ziel := range map[string]string{"Fritz": "p-fritz", "ben berg": "p-ben", "kur": "p-fritz", "Carla": "haushalt/c", "du": "p-anna"} {
		rec := wt.rufe("GET", "/tresor/umfeld?suche="+url.QueryEscape(q), anna, nil)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != umfeldMitte(ziel) {
			t.Fatalf("%q: %d %q", q, rec.Code, rec.Header().Get("Location"))
		}
	}
	page := wt.rufe("GET", "/tresor/umfeld?suche=berg", anna, nil).Body.String()
	for _, want := range []string{"<h1>Person suchen</h1>", "<h2>Wen meinst du?</h2>", `value="berg"`,
		`<a href="/tresor/umfeld?mitte=p-ben"><span class=av`, `<span class=wer>Ben Berg<span class=k>Partner/in · 3 Tage auf deinen Fotos seit 2019 · in einem Gruppen-Chat · 3 Mails zusammen</span>`,
		`<span class=wer>Kim Berg<span class=k>2 Tage auf deinen Fotos seit 2019</span>`, `<span class=wer>Du<span class=k></span>`, `<a href="/tresor/umfeld">Zurück zum Umfeld</a>`} {
		if !strings.Contains(page, want) {
			t.Fatalf("%q missing:\n%s", want, page)
		}
	}
	if i, j := strings.Index(page, ">Ben Berg<"), strings.Index(page, ">Kim Berg<"); i < 0 || j < i {
		t.Fatalf("the closer is not first:\n%s", page)
	}
	rec := wt.rufe("GET", "/tresor/umfeld?suche=Zacharias", anna, nil)
	for _, want := range []string{saysSucheNiemand, `value="Zacharias"`, "In den Nachrichten nach „Zacharias“ suchen", `<a href="/tresor/umfeld">Zurück zum Umfeld</a>`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("nobody: %d, %q missing:\n%s", rec.Code, want, rec.Body)
		}
	}
	if page := wt.rufe("GET", "/tresor/suche?q=fritz", anna, nil).Body.String(); !strings.Contains(page, `<h2>Personen</h2><ul class=leute><li><a href="/tresor/umfeld?mitte=p-fritz">`) {
		t.Fatalf("the search on top does not find the person:\n%s", page)
	}
	wt.w.Tresor.Abschliessen(t.Context(), "test")
	if rec := wt.rufe("GET", "/tresor/umfeld?suche=Fritz", anna, nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/tresor" {
		t.Fatalf("closed: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// What two share in one sentence: each kind once, with its count; the
// photos' years, the last one's month only while it is fresh.
func TestGemeinsamSatz(t *testing.T) {
	jetzt := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	tag := func(j int, m time.Month) time.Time { return time.Date(j, m, 3, 12, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		g    Gemeinsam
		want string
	}{
		{Gemeinsam{Fotos: 37, Erstes: tag(2014, 7), Letztes: tag(2026, 5), Gruppen: 3, Nachrichten: 1204}, "37 gemeinsame Fotos (2014–2026, zuletzt im Mai) · 3 Gruppen · 1.204 Nachrichten"},
		{Gemeinsam{Fotos: 1, Erstes: tag(2019, 5), Letztes: tag(2019, 5)}, "1 gemeinsames Foto (2019)"},
		{Gemeinsam{Fotos: 2, Erstes: tag(2019, 5), Letztes: tag(2025, 11)}, "2 gemeinsame Fotos (2019–2025)"},
		{Gemeinsam{Fotos: 2, Erstes: tag(2026, 3), Letztes: tag(2026, 10)}, "2 gemeinsame Fotos (2026, zuletzt im Oktober)"},
		{Gemeinsam{Gruppen: 1, Faeden: 12, Nachrichten: 1}, "1 Gruppe · 12 Mail-Verläufe · 1 Nachricht"},
		{Gemeinsam{Faeden: 1, Nachrichten: 3}, "1 Mail-Verlauf · 3 Nachrichten"},
		{Gemeinsam{}, ""},
	} {
		if got := c.g.Satz(jetzt, time.UTC); got != c.want {
			t.Fatalf("%+v: %q, not %q", c.g, got, c.want)
		}
	}
}

// kanteViel gives Anna and Ben of kanteBox five more group chats (Carla
// in them too) and eleven more mail threads.
func kanteViel(t *testing.T, ut *umfeldTest, innen Innen) {
	tab, err := tabelleLaden(innen)
	if err != nil {
		t.Fatal(err)
	}
	beide := []string{"haushalt/a", "haushalt/b"}
	for i := range 5 {
		tab.Gespraeche = append(tab.Gespraeche, netzGespraech{ID: fmt.Sprint(100 + i), Art: gespraechChat, Titel: fmt.Sprintf("Chor %d", i), Groesse: 4, Nachrichten: 100,
			Zuletzt: time.Date(2026, 6, 1+i, 8, 0, 0, 0, time.UTC), Wer: append([]string{"haushalt/c"}, beide...)})
	}
	for i := range 11 {
		tab.Gespraeche = append(tab.Gespraeche, netzGespraech{ID: fmt.Sprint(200 + i), Art: gespraechMail, Groesse: 2, Nachrichten: 2 + i, Zuletzt: time.Date(2026, 1, 1+i, 8, 0, 0, 0, time.UTC), Wer: beide})
	}
	if err := jsonImTresor(innen.umfeldTabelle(), tab); err != nil {
		t.Fatal(err)
	}
	ut.u.laden(innen)
}

var zeileFett = regexp.MustCompile(`<li><a href="[^"]*"><b>([^<]*)</b>`)

// Less with more (owner 2026-11-11: „unendliche Ketten von gleich
// klingenden Meldungen“): two people in six group chats and twelve mail
// threads get one sentence, the cue, three groups and one row for the
// mail; everything else is folded, no row says what another says, and a
// list's row is one short line.
func TestKanteWenigerMitMehr(t *testing.T) {
	wt, ut, _, innen, _ := kanteBox(t)
	kanteViel(t, ut, innen)
	page := wt.rufe("GET", kanteAdresse("p-anna", "p-ben"), anna, nil).Body.String()
	vorn, alle, ok := strings.Cut(page, `<details class=alle id=alle-nachrichten><summary>alle zeigen (15)</summary>`)
	if !ok || !strings.Contains(page, "<p class=summe>4 gemeinsame Fotos (2019–2025, zuletzt im Dezember) · 6 Gruppen · 12 Mail-Verläufe · 620 Nachrichten</p>") {
		t.Fatalf("the sentence or „alle zeigen“:\n%s", page)
	}
	_, vorn, _ = strings.Cut(vorn, "<ul class=gespraeche>")
	var fett []string
	for _, m := range zeileFett.FindAllStringSubmatch(vorn, -1) {
		fett = append(fett, m[1])
	}
	if got := strings.Join(fett, " | "); got != "„Hallo Schatz“ | Familie Berg | Chor 4 (Verein) | Chor 3 (Verein) | 12 Mail-Verläufe" {
		t.Fatalf("the rows before „alle zeigen“: %s", got)
	}
	if !strings.Contains(vorn, `<a href="/messages/4300"><b>12 Mail-Verläufe</b><span class=k>Zuletzt: Du: Anbei die Fotos · 02.10.2026</span>`) {
		t.Fatalf("the mail's one row does not carry the newest line:\n%s", vorn)
	}
	alle, _, _ = strings.Cut(alle, "</details>")
	if n := strings.Count(alle, "<li>"); n != 15 || strings.Count(alle, "<b>Chor ") != 3 || strings.Count(alle, "<b>Mail-Verlauf, ") != 12 {
		t.Fatalf("folded: %d rows:\n%s", n, alle)
	}
	gesehen := map[string]bool{}
	for _, z := range regexp.MustCompile(`<li>.*?</li>`).FindAllString(page, -1) {
		if gesehen[z] && strings.Contains(z, "class=k") {
			t.Fatalf("a row twice: %s", z)
		}
		gesehen[z] = true
	}
	// a list's row: the relation and at most kurzTeile reasons, the phone book's relation not a second time
	nahe := wt.rufe("GET", "/tresor/umfeld", anna, nil).Body.String()
	if !strings.Contains(nahe, `>Ben Berg <span class=k>Partner/in · 3 Tage auf deinen Fotos seit 2019 · in 6 Gruppen-Chats · 80 Mails zusammen</span></a></li>`) || strings.Contains(nahe, "Was verbindet die beiden?") {
		t.Fatalf("the row:\n%s", nahe)
	}
}

// A phone book's nickname finds the person (owner 2026-11-11): NICKNAME is
// a list (an escaped comma stays in its name), each name once; the round
// keeps them in the vault's table, the search finds by them and says
// „auch: Hasi“, and the person's page says them discreetly. A table kept
// before nicknames were read has none and still loads.
func TestSpitzname(t *testing.T) {
	k := kartenLesen("privat/", karteMit("s", "Sabine Lang", "NICKNAME:Bine,Hasi\\, mein Schatz\r\nNICKNAME:bine,Sabine Lang\r\n"))
	if len(k) != 1 || strings.Join(k[0].spitz, "|") != "Bine|Hasi, mein Schatz|Sabine Lang" {
		t.Fatalf("NICKNAME read: %+v", k)
	}

	wt, ut, _, innen, _ := kanteBox(t)
	tab, err := tabelleLaden(innen)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(tab.Kontakte, func(c netzKontakt) bool { return c.Key == "haushalt/b" }); i < 0 || !slices.Equal(tab.Kontakte[i].Spitz, []string{"Hasi"}) {
		t.Fatalf("the round did not keep Ben's nickname: %+v", tab.Kontakte)
	}
	if roh, _ := os.ReadFile(innen.umfeldTabelle()); !strings.Contains(string(roh), `"sp":["Hasi"]`) {
		t.Fatal("the nickname is not in the vault's table")
	}
	n := ut.u.Netz()
	if ts, _ := n.Suchen("has", sucheHoechstens); len(ts) != 1 || ts[0].ID != "p-ben" || ts[0].Als != "Hasi" || !ts[0].Spitz {
		t.Fatalf("hasi finds %+v", ts)
	}
	if ts, _ := n.Suchen("ben", sucheHoechstens); len(ts) != 1 || ts[0].Als != "" || ts[0].Spitz {
		t.Fatalf("ben finds %+v", ts)
	}
	if rec := wt.rufe("GET", "/tresor/umfeld?suche=Hasi", anna, nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != umfeldMitte("p-ben") {
		t.Fatalf("Hasi: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if page := wt.rufe("GET", umfeldMitte("p-ben"), anna, nil).Body.String(); !strings.Contains(page, `<h2>Ben Berg</h2></div><p class="k auch">auch: Hasi</p>`) {
		t.Fatalf("Ben's page does not say his nickname:\n%s", page)
	}
	var b strings.Builder
	personenZeilen(&b, n, []Treffer{{Knoten: n.knoten["p-ben"], Als: "Hasi", Spitz: true}})
	if !strings.Contains(b.String(), `<span class=k>auch: Hasi · Partner/in`) {
		t.Fatalf("the row: %s", b.String())
	}

	// a table kept before: no "sp"; it loads, nobody has a nickname, nothing else changes
	var alt map[string]any
	roh, _ := os.ReadFile(innen.umfeldTabelle())
	if err := json.Unmarshal(roh, &alt); err != nil {
		t.Fatal(err)
	}
	for _, c := range alt["kontakte"].([]any) {
		delete(c.(map[string]any), "sp")
	}
	if err := jsonImTresor(innen.umfeldTabelle(), alt); err != nil {
		t.Fatal(err)
	}
	ut.u.laden(innen)
	n = ut.u.Netz()
	if ts, _ := n.Suchen("hasi", sucheHoechstens); len(ts) != 0 || len(n.Spitznamen("p-ben")) != 0 {
		t.Fatalf("an old table has a nickname: %+v", ts)
	}
	if ts, _ := n.Suchen("Ben", sucheHoechstens); len(ts) != 1 || ts[0].ID != "p-ben" {
		t.Fatalf("an old table: Ben is %+v", ts)
	}
	if page := wt.rufe("GET", umfeldMitte("p-ben"), anna, nil).Body.String(); strings.Contains(page, "auch:") || !strings.Contains(page, "<h2>Ben Berg</h2>") {
		t.Fatalf("an old table's page:\n%s", page)
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAbfrage is msgvault's POST /api/v1/query: it checks the key and
// that the archive sends the one statement, and answers rows (or a status).
type fakeAbfrage struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	zeilen [][]any
	sql    []string
}

var abfrageSpalten = []string{"gespraech", "art", "titel", "groesse", "nachrichten", "zuletzt", "liste", "teilnehmer", "telefon", "mail"}

func neueAbfrage(t *testing.T) (*fakeAbfrage, *MsgvaultAPI) {
	f := &fakeAbfrage{status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("X-API-Key") != "vault-key" || r.Method != http.MethodPost || r.URL.Path != "/api/v1/query" {
			jsonAus(w, http.StatusUnauthorized, map[string]string{"error": "nein"})
			return
		}
		var q struct {
			SQL string `json:"sql"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		f.sql = append(f.sql, q.SQL)
		switch f.status {
		case http.StatusOK:
			jsonAus(w, http.StatusOK, map[string]any{"columns": abfrageSpalten, "rows": f.zeilen, "row_count": len(f.zeilen)})
		case http.StatusAccepted:
			jsonAus(w, http.StatusAccepted, map[string]any{"status": "queued", "job_id": "j1"})
		default:
			jsonAus(w, f.status, map[string]string{"error": "engine_unavailable", "message": "Analytics engine is initializing"})
		}
	}))
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.URL)
	port, _ := strconv.Atoi(u.Port())
	return f, &MsgvaultAPI{Port: port, Key: func() string { return "vault-key" }}
}

// abfrageZeile is one row: conversation, kind, title, size, messages, last, list, participant, phone, mail.
func abfrageZeile(g, art, titel string, groesse, nachrichten int, zuletzt string, liste bool, wer, tel, mail string) []any {
	return []any{g, art, titel, groesse, nachrichten, zuletzt, liste, wer, tel, mail}
}

func gespraechsKarten() []buchKarte {
	return []buchKarte{
		{key: "haushalt/a", name: "Anna Berg", tels: []string{"0151 1111 1111"}, emails: []string{"Anna@Box.test"}},
		{key: "privat/a", name: "Anna Berg", tels: []string{"+49 151 11111111"}},
		{key: "haushalt/b", name: "Ben Berg", tels: []string{"+49 152 2222 2222"}},
		{key: "haushalt/j", name: "Jürgen Groß", emails: []string{"juergen@example.org"}},
		{key: "privat/j", name: "Juergen Gross", tels: []string{"0170 3333333"}},
		{key: "haushalt/f", name: "Festnetz Berg", tels: []string{"0681 444444"}},
		{key: "haushalt/g", name: "Gerd Berg", tels: []string{"0681 444444"}}, // the same landline
	}
}

func teilnehmer(tel, mail string) gespraechsTeilnehmer {
	return gespraechsTeilnehmer{ID: tel + mail, Telefon: tel, Mail: mail}
}

// Each participant to the cards with their number (last nine digits) or
// address (case aside), across both books and both spellings of a name;
// one not in a book, or a number of differently named contacts, is none.
func TestTeilnehmerKarten(t *testing.T) {
	ks := gespraechsKarten()
	for _, tc := range []struct {
		name string
		t    gespraechsTeilnehmer
		want []string
	}{
		{"number, both books", teilnehmer("+4915111111111", ""), []string{"haushalt/a", "privat/a"}},
		{"address, case aside", teilnehmer("", "anna@box.TEST"), []string{"haushalt/a"}},
		{"Jürgen by address", teilnehmer("", "juergen@example.org"), []string{"haushalt/j"}},
		{"Juergen by number", teilnehmer("+491703333333", ""), []string{"privat/j"}},
		{"a shared landline: nobody", teilnehmer("+49681444444", ""), nil},
		{"not in a book: nobody", teilnehmer("+4915999999999", "fremd@example.org"), nil},
		{"too short to compare", teilnehmer("1111", ""), nil},
	} {
		if got := teilnehmerKarten(tc.t, ks); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	// a number of Jürgen's phone card and the address of his household card name one person
	both := teilnehmer("+491703333333", "JUERGEN@example.org")
	if got := teilnehmerKarten(both, ks); !slices.Equal(got, []string{"haushalt/j", "privat/j"}) {
		t.Errorf("Jürgen's two cards: %v", got)
	}
}

// The one name comparison (klar, archiv#30) step 3 relies on: umlauts
// spelled out, case and accents aside.
func TestKlarUmlaute(t *testing.T) {
	for _, p := range [][2]string{{"Jürgen", "Juergen"}, {"JÜRGEN", "juergen"}, {"Groß", "Gross"}, {"Bärbel Möller", "Baerbel Moeller"},
		{"René", "Rene"}, {"Gartenstraße 2", "Gartenstrasse 2"}} {
		if klar(p[0]) != klar(p[1]) {
			t.Errorf("%q and %q differ: %q %q", p[0], p[1], klar(p[0]), klar(p[1]))
		}
	}
	if klar("Muller") == klar("Müller") || klar("Anna") == klar("Anne") {
		t.Error("different names made equal")
	}
	// a relation a phone wrote names its other in the other spelling
	ks := slices.Concat(kartenLesen("haushalt/", karteMit("j", "Jürgen Groß", "")),
		kartenLesen("privat/", karteMit("m", "Maria Gross", "RELATED;TYPE=spouse;VALUE=text:Juergen Gross\r\n")))
	if bs := beziehungen(ks); len(bs) != 1 || bs[0].B != "haushalt/j" || bs[0].Wie != "Ehepartner/in" {
		t.Errorf("Juergen Gross not found as Jürgen Groß: %+v", bs)
	}
}

// What counts: a chat of 3..30 members, a mail thread of 2..10
// addresses, no mailing list, no bulk sender; only matched participants
// are kept (as card keys), a chat's title too, a thread's not.
func TestGespraecheZuordnen(t *testing.T) {
	ks := gespraechsKarten()
	anna, ben, jue := teilnehmer("+4915111111111", ""), teilnehmer("+4915222222222", ""), teilnehmer("", "juergen@example.org")
	fremd := teilnehmer("+4915999999999", "")
	viele := func(n int, mit ...gespraechsTeilnehmer) []gespraechsTeilnehmer {
		out := slices.Clone(mit)
		for i := len(mit); i < n; i++ {
			out = append(out, teilnehmer(fmt.Sprintf("+49159%08d", i), ""))
		}
		return out
	}
	zu := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	roh := []rohGespraech{
		{ID: "1", Art: gespraechChat, Titel: "Familie Berg", Groesse: 3, Nachrichten: 40, Zuletzt: zu, Wer: viele(3, anna, ben, fremd)},
		{ID: "2", Art: gespraechChat, Titel: "Zwei", Groesse: 2, Wer: viele(2, anna, ben)},                             // two: no group
		{ID: "3", Art: gespraechChat, Titel: "Verein", Groesse: 30, Wer: viele(30, anna, ben)},                         // the most
		{ID: "4", Art: gespraechChat, Titel: "Schule", Groesse: 31, Wer: viele(31, anna, ben)},                         // too many
		{ID: "5", Art: gespraechMail, Titel: "Re: Grillen", Groesse: 10, Nachrichten: 12, Wer: viele(10, anna, jue)},   // the most
		{ID: "6", Art: gespraechMail, Titel: "Rundmail", Groesse: 11, Nachrichten: 1, Wer: viele(11, anna, jue)},       // too many
		{ID: "7", Art: gespraechMail, Groesse: 2, Nachrichten: 3, Liste: true, Wer: []gespraechsTeilnehmer{anna, jue}}, // a list
		{ID: "8", Art: gespraechMail, Groesse: 3, Nachrichten: 2, Wer: []gespraechsTeilnehmer{anna, jue, teilnehmer("", "no-reply@shop.example")}},
		{ID: "9", Art: gespraechMail, Groesse: 3, Nachrichten: 2, Wer: []gespraechsTeilnehmer{anna, jue, teilnehmer("", "newsletter+de@shop.example")}},
		{ID: "10", Art: gespraechMail, Groesse: 2, Nachrichten: 2, Wer: []gespraechsTeilnehmer{anna, fremd}}, // one matched: no pair
		{ID: "11", Art: "kanal", Groesse: 5, Wer: viele(5, anna, ben)},
	}
	got := gespraecheZuordnen(roh, ks)
	var ids []string
	for _, g := range got {
		ids = append(ids, g.ID)
	}
	if !slices.Equal(ids, []string{"1", "3", "5"}) {
		t.Fatalf("kept %v", ids)
	}
	if g := got[0]; g.Titel != "Familie Berg" || g.Groesse != 3 || g.Nachrichten != 40 || !g.Zuletzt.Equal(zu) ||
		!slices.Equal(g.Wer, []string{"haushalt/a", "privat/a", "haushalt/b"}) {
		t.Fatalf("the family chat: %+v", g)
	}
	if got[2].Titel != "" || !slices.Equal(got[2].Wer, []string{"haushalt/a", "privat/a", "haushalt/j"}) {
		t.Fatalf("a thread keeps no subject: %+v", got[2])
	}
	for _, a := range []string{"noreply@x.de", "No-Reply@x.de", "info@verein.de", "notifications+abc@github.example", "mailer-daemon@x"} {
		if !massenAdresse(a) {
			t.Errorf("%s is a bulk sender", a)
		}
	}
	for _, a := range []string{"anna@box.test", "information.anna@x.de", "nora@x.de", ""} {
		if massenAdresse(a) {
			t.Errorf("%s is no bulk sender", a)
		}
	}
}

// The rows msgvault answers, read by column name; the statement is one
// read-only SELECT with the limits in it; still building (202) or no
// engine yet (503) is „later“, not an error.
func TestGespraecheLesen(t *testing.T) {
	f, api := neueAbfrage(t)
	f.zeilen = [][]any{
		abfrageZeile("7", "chat", "Familie", 3, 40, "2026-10-01T08:00:00Z", false, "1", "+4915111111111", ""),
		abfrageZeile("7", "chat", "Familie", 3, 40, "2026-10-01T08:00:00Z", false, "2", "+4915222222222", ""),
		abfrageZeile("9", "mail", "", 2, 12, "2025-01-01T00:00:00Z", true, "1", "", "anna@box.test"),
	}
	gs, err := api.Gespraeche(t.Context())
	if err != nil || len(gs) != 2 || len(gs[0].Wer) != 2 || gs[0].Groesse != 3 || gs[0].Nachrichten != 40 || gs[0].Titel != "Familie" ||
		!gs[0].Zuletzt.Equal(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)) || !gs[1].Liste || gs[1].Wer[0].Mail != "anna@box.test" {
		t.Fatalf("%+v %v", gs, err)
	}
	sql := f.sql[0]
	if !strings.HasPrefix(sql, "WITH ") || strings.Count(sql, ";") != 0 || !strings.Contains(sql, "BETWEEN 3 AND 30") || !strings.Contains(sql, "BETWEEN 2 AND 10") {
		t.Fatalf("the statement: %s", sql)
	}
	for _, s := range []int{http.StatusAccepted, http.StatusServiceUnavailable} {
		f.status = s
		if _, err := api.Gespraeche(t.Context()); err != errGespraecheSpaeter {
			t.Errorf("%d: %v", s, err)
		}
	}
	if _, err := gespraecheLesen(abfrageAntwort{Columns: []string{"gespraech"}}); err == nil {
		t.Error("a missing column was read")
	}
	zu := &MsgvaultAPI{Port: api.Port, Key: func() string { return "" }}
	if _, err := zu.Gespraeche(t.Context()); err != errNichtBereit {
		t.Errorf("closed: %v", err)
	}
}

// netzMitGespraechen is a net of three contacts (no photos) and
// conversations; privat/a is a key of no contact here (left out).
func netzMitGespraechen(gs []netzGespraech, jetzt time.Time) *Netz {
	st := netzTabelle{Namen: map[string]string{}, Gespraeche: gs, Ich: "haushalt/a",
		Kontakte: []netzKontakt{{Key: "haushalt/a", Name: "Anna Berg"}, {Key: "haushalt/b", Name: "Ben Berg"}, {Key: "haushalt/c", Name: "Carla Wolf"}}}
	return neuesNetz(st, nil, nil, time.UTC, func() time.Time { return jetzt })
}

// A pair's chats and mails: each conversation 2 (chat) or 0.5 (mail)
// over size-1, halved every four years since its last message, at most
// gespraechDeckel; a new tie from two conversations on; the reasons
// count the chats and the mails; a hidden person leaves out.
func TestGespraecheImNetz(t *testing.T) {
	jetzt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	vor4 := jetzt.Add(-netzHalbwert)
	gs := []netzGespraech{
		{ID: "1", Art: gespraechChat, Titel: "Familie", Groesse: 5, Nachrichten: 100, Zuletzt: jetzt, Wer: []string{"haushalt/a", "privat/a", "haushalt/b"}},
		{ID: "2", Art: gespraechChat, Titel: "Urlaub", Groesse: 3, Nachrichten: 10, Zuletzt: vor4, Wer: []string{"haushalt/a", "haushalt/b"}},
		{ID: "3", Art: gespraechMail, Groesse: 2, Nachrichten: 12, Zuletzt: jetzt, Wer: []string{"haushalt/a", "haushalt/c"}},
	}
	n := netzMitGespraechen(gs, jetzt)
	nahe := n.Nahe("haushalt/a", 10)
	if len(nahe) != 1 || nahe[0].Mit.Name != "Ben Berg" {
		t.Fatalf("Ben from two chats; Carla from one thread is no tie yet: %v", namenVon(nahe))
	}
	k := nahe[0].Kante
	if want := 2.0/4 + 2.0/2*0.5; math.Abs(k.Staerke-want) > 1e-9 || k.Rang != k.Staerke || k.Chats != 2 || k.Mails != 0 {
		t.Fatalf("Anna–Ben: %+v, want %v", k, want)
	}
	if g := n.Gruende("haushalt/a", k); g != "in 2 Gruppen-Chats · Familie (Gruppe „Familie“) · Reisegruppe „Urlaub“" { // the titles' categories (gruppen.go)
		t.Fatalf("reasons: %q", g)
	}

	gs = append(gs, netzGespraech{ID: "4", Art: gespraechMail, Groesse: 2, Nachrichten: 3, Zuletzt: jetzt, Wer: []string{"haushalt/a", "haushalt/c"}})
	n = netzMitGespraechen(gs, jetzt)
	carla := n.Nahe("haushalt/c", 10)
	if len(carla) != 1 || n.Gruende("haushalt/c", carla[0].Kante) != "15 Mails zusammen" || math.Abs(carla[0].Kante.Staerke-1) > 1e-9 {
		t.Fatalf("two threads make a tie: %+v", carla)
	}

	var viel []netzGespraech
	for i := range 40 {
		viel = append(viel, netzGespraech{ID: strconv.Itoa(i), Art: gespraechMail, Groesse: 2, Nachrichten: 1, Zuletzt: jetzt, Wer: []string{"haushalt/a", "haushalt/c"}})
	}
	viel = append(viel, netzGespraech{ID: "x", Art: gespraechChat, Groesse: 3, Nachrichten: 1, Zuletzt: jetzt, Wer: []string{"haushalt/a", "haushalt/c"}})
	n = netzMitGespraechen(viel, jetzt)
	if k := n.Nahe("haushalt/a", 1)[0].Kante; k.Staerke != gespraechDeckel || n.Gruende("haushalt/a", k) != "in einem Gruppen-Chat · 40 Mails zusammen" {
		t.Fatalf("a busy inbox is capped: %+v %q", k, n.Gruende("haushalt/a", k))
	}

	n = netzMitGespraechen(gs, jetzt)
	n.Verbergen("haushalt/b", verbergenNicht)
	if got := namenVon(n.Nahe("haushalt/a", 10)); !slices.Equal(got, []string{"Carla Wolf"}) {
		t.Fatalf("Ben taken out: %v", got)
	}

	// too big or too small (an old table, a changed limit) counts nothing
	n = netzMitGespraechen([]netzGespraech{
		{ID: "1", Art: gespraechChat, Groesse: 31, Zuletzt: jetzt, Wer: []string{"haushalt/a", "haushalt/b"}},
		{ID: "2", Art: gespraechChat, Groesse: 31, Zuletzt: jetzt, Wer: []string{"haushalt/a", "haushalt/b"}},
		{ID: "3", Art: gespraechMail, Groesse: 11, Zuletzt: jetzt, Wer: []string{"haushalt/a", "haushalt/b"}},
	}, jetzt)
	if got := n.Nahe("haushalt/a", 10); len(got) != 0 {
		t.Fatalf("over the limits: %v", namenVon(got))
	}
}

// A round reads msgvault into the table in the vault (card keys, no
// number, no address), the net and its reasons show the chats; msgvault
// still building keeps what was read last.
func TestUmfeldRundeMitGespraechen(t *testing.T) {
	ut := netzBox(t)
	ut.rad.haushalt["b.vcf"] = karteMit("b", "Ben Berg", "ADR;TYPE=HOME:;;Gartenweg 2;Saarbrücken;;66111;Deutschland\r\nTEL:0152 2222 2222\r\n")
	ut.rad.haushalt["c.vcf"] = karteMit("c", "Carla Wolf", "ORG:Werk AG;Einkauf\r\nEMAIL:carla@werk.example\r\n")
	f, api := neueAbfrage(t)
	f.zeilen = [][]any{
		abfrageZeile("7", "chat", "Familie Berg", 3, 40, "2026-11-01T08:00:00Z", false, "1", "", "anna@box.test"),
		abfrageZeile("7", "chat", "Familie Berg", 3, 40, "2026-11-01T08:00:00Z", false, "2", "+4915222222222", ""),
		abfrageZeile("7", "chat", "Familie Berg", 3, 40, "2026-11-01T08:00:00Z", false, "3", "+4915999999999", ""),
		abfrageZeile("8", "mail", "", 2, 5, "2026-10-01T08:00:00Z", false, "1", "", "anna@box.test"),
		abfrageZeile("8", "mail", "", 2, 5, "2026-10-01T08:00:00Z", false, "4", "", "Carla@Werk.example"),
		abfrageZeile("9", "mail", "", 2, 7, "2026-10-02T08:00:00Z", false, "1", "", "anna@box.test"),
		abfrageZeile("9", "mail", "", 2, 7, "2026-10-02T08:00:00Z", false, "4", "", "carla@werk.example"),
	}
	ut.u.Msgvault = api
	ut.u.Runde(t.Context(), ut.innen)
	n := ut.u.Netz()
	var ben, carla Kante
	for _, v := range n.Nahe("p-anna", 10) {
		switch v.Mit.Name {
		case "Ben Berg":
			ben = v.Kante
		case "Carla Wolf":
			carla = v.Kante
		}
	}
	if ben.Chats != 1 || !strings.Contains(n.Gruende("p-anna", ben), "in einem Gruppen-Chat") {
		t.Fatalf("Anna–Ben: %+v %q", ben, n.Gruende("p-anna", ben))
	}
	if carla.Mails != 12 || n.Gruende("p-anna", carla) != "12 Mails zusammen · gleiche Firma" {
		t.Fatalf("Anna–Carla: %+v %q", carla, n.Gruende("p-anna", carla))
	}
	b, err := os.ReadFile(ut.innen.umfeldTabelle())
	if err != nil {
		t.Fatal(err)
	}
	for _, geheim := range []string{"2222", "9999", "carla@", "anna@box.test\"", "Re:"} {
		if strings.Contains(string(b[strings.Index(string(b), `"gespraeche"`):]), geheim) {
			t.Errorf("the table keeps %q of a conversation", geheim)
		}
	}
	tab, _ := tabelleLaden(ut.innen)
	if len(tab.Gespraeche) != 3 || tab.Gespraeche[0].Titel != "Familie Berg" {
		t.Fatalf("kept: %+v", tab.Gespraeche)
	}
	f.status = http.StatusServiceUnavailable
	ut.u.Runde(t.Context(), ut.innen)
	if tab, _ := tabelleLaden(ut.innen); len(tab.Gespraeche) != 3 {
		t.Fatalf("msgvault building: the last read gone: %+v", tab.Gespraeche)
	}
}

// The tool integration.sh uses: the statement as msgvault's body, and an
// answer read back as lines without a number or an address.
func TestGespraecheWerkzeug(t *testing.T) {
	var b strings.Builder
	if gespraecheWerkzeug([]string{"-sql"}, nil, &b) != 0 || !strings.Contains(b.String(), `"sql":"WITH m AS`) {
		t.Fatalf("-sql: %s", b.String())
	}
	antwort, _ := json.Marshal(map[string]any{"columns": abfrageSpalten, "rows": [][]any{
		abfrageZeile("7", "chat", "Familie", 3, 1, "2026-10-01T08:00:00Z", false, "1", "+4915111111111", ""),
		abfrageZeile("8", "mail", "Betreff", 2, 2, "2026-10-01T08:00:00Z", false, "2", "", "ben@example.org"),
		abfrageZeile("9", "chat", "Zwei", 2, 2, "2026-10-01T08:00:00Z", false, "3", "+4915111111111", ""),
	}})
	b.Reset()
	if gespraecheWerkzeug(nil, strings.NewReader(string(antwort)), &b) != 0 || b.String() != "chat 3 1 false Familie\nmail 2 2 false\n" {
		t.Fatalf("read: %q", b.String())
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI is msgvault's HTTP API as far as the archive uses it (routes of
// 0.22.0's api/openapi.yaml), with its key checked on every request.
type fakeAPI struct {
	*httptest.Server
	mu          sync.Mutex
	anfragen    []string
	koerper     map[string]string // path -> last body
	kopf        map[string]http.Header
	buecher     []CardDAVBuch
	personen    map[int64]Person
	identitaet  map[int64][]Identitaet
	vorschlaege []map[string]any
	token       int  // the review token's generation
	veraltet    bool // the next decision finds the token stale
	teilnehmer  map[int64]map[string]any
}

func neueAPI(t *testing.T) (*fakeAPI, *MsgvaultAPI) {
	f := &fakeAPI{koerper: map[string]string{}, kopf: map[string]http.Header{}, personen: map[int64]Person{}, identitaet: map[int64][]Identitaet{}, teilnehmer: map[int64]map[string]any{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.URL)
	port, _ := strconv.Atoi(u.Port())
	return f, &MsgvaultAPI{Port: port, Key: func() string { return "vault-key" }}
}

func jsonAus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.anfragen = append(f.anfragen, r.Method+" "+r.URL.Path)
	f.koerper[r.URL.Path] = string(b)
	f.kopf[r.URL.Path] = r.Header.Clone()
	if r.Header.Get("X-API-Key") != "vault-key" {
		jsonAus(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	teile := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/api/v1/carddav/books":
		jsonAus(w, 200, map[string]any{"books": f.buecher})
	case r.URL.Path == "/api/v1/people":
		var l []Person
		for _, p := range f.personen {
			l = append(l, p)
		}
		jsonAus(w, 200, map[string]any{"people": l})
	case len(teile) == 4 && teile[2] == "people" && r.Method == "GET":
		id, _ := strconv.ParseInt(teile[3], 10, 64)
		p, ok := f.personen[id]
		if !ok {
			jsonAus(w, 404, map[string]string{"error": "person_profile_not_found"})
			return
		}
		jsonAus(w, 200, p)
	case len(teile) == 5 && teile[2] == "people" && teile[4] == "profile":
		jsonAus(w, 200, map[string]any{"contact_points": []any{map[string]any{"address_kind": "phone", "original_value": "+49 151 7777777"}, map[string]any{"address_kind": "email", "original_value": "carla@example.org"}}})
	case len(teile) == 5 && teile[2] == "people" && teile[4] == "identities":
		id, _ := strconv.ParseInt(teile[3], 10, 64)
		jsonAus(w, 200, map[string]any{"person_id": id, "identities": f.identitaet[id]})
	case len(teile) == 5 && teile[2] == "people" && teile[4] == "merge":
		id, _ := strconv.ParseInt(teile[3], 10, 64)
		var rein struct {
			Absorbed int64 `json:"absorbed_person_id"`
		}
		json.Unmarshal(b, &rein)
		a, bb := f.personen[id], f.personen[rein.Absorbed]
		want := fmt.Sprintf(`"person-%d-r%d", "person-%d-r%d"`, a.ID, a.Revision, bb.ID, bb.Revision)
		if r.Header.Get("Idempotency-Key") == "" {
			jsonAus(w, 428, map[string]string{"error": "idempotency_key_required"})
			return
		}
		if r.Header.Get("If-Match") != want {
			jsonAus(w, 409, map[string]string{"error": "person_merge_revision_conflict", "message": "changed"})
			return
		}
		delete(f.personen, rein.Absorbed)
		a.Revision++
		f.personen[id] = a
		jsonAus(w, 200, map[string]any{"person": a})
	case r.URL.Path == "/api/v1/identity/match-candidates":
		state := r.URL.Query().Get("state")
		switch state {
		case "candidate", "accepted", "rejected", "conflict":
		default: // 0.22.0's API takes no "all" (its CLI does)
			jsonAus(w, 400, map[string]string{"error": "invalid_state"})
			return
		}
		var l []map[string]any
		for _, v := range f.vorschlaege {
			v["review_token"] = fmt.Sprintf("token-%d-%d", int(v["id"].(float64)), f.token)
			if v["state"] == state {
				l = append(l, v)
			}
		}
		jsonAus(w, 200, map[string]any{"candidates": l, "limit": 200, "offset": 0})
	case len(teile) == 7 && teile[2] == "identity" && teile[5] == "review":
		var rein struct {
			Token string `json:"review_token"`
		}
		json.Unmarshal(b, &rein)
		id := teile[4]
		if f.veraltet || rein.Token != fmt.Sprintf("token-%s-%d", id, f.token) {
			f.veraltet = false
			f.token++
			jsonAus(w, 409, map[string]string{"error": "identity_match_review_stale", "message": "evidence changed"})
			return
		}
		jsonAus(w, 200, map[string]any{"candidate": map[string]any{"id": id, "state": "accepted"}})
	case len(teile) == 4 && teile[2] == "participants":
		id, _ := strconv.ParseInt(teile[3], 10, 64)
		jsonAus(w, 200, f.teilnehmer[id])
	default:
		jsonAus(w, 404, map[string]string{"error": "not_found"})
	}
}

func (f *fakeAPI) gesehen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.anfragen...)
}

// abgleichRun plays msgvault's CLI for the Abgleich.
type abgleichRun struct {
	mu      sync.Mutex
	befehle []Befehl
	syncAus string
	fehlt   string // a command that fails
}

func (r *abgleichRun) Run(ctx context.Context, b Befehl) (Ausgabe, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.befehle = append(r.befehle, b)
	if r.fehlt != "" && b.Args[0] == r.fehlt {
		return Ausgabe{Code: 1, Text: "Error: discovery failed for " + string(b.Eingabe)}, nil
	}
	switch b.Args[0] {
	case "add-carddav":
		return Ausgabe{Text: "Configured CardDAV account msgvault with 1 discovered address books\n"}, nil
	case "sync-carddav":
		return Ausgabe{Text: r.syncAus}, nil
	}
	return Ausgabe{Text: "{}"}, nil
}

func (r *abgleichRun) args() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, b := range r.befehle {
		out = append(out, strings.Join(b.Args, " "))
	}
	return out
}

type abgleichTest struct {
	rad   *fakeRadicale
	api   *fakeAPI
	run   *abgleichRun
	a     *Abgleich
	innen Innen
}

func neuerAbgleich(t *testing.T) *abgleichTest {
	at := &abgleichTest{rad: neuesRadicale(t), run: &abgleichRun{syncAus: "CardDAV sync: 1 books, 2 created, 0 updated, 0 removed\n"}}
	var api *MsgvaultAPI
	at.api, api = neueAPI(t)
	r := at.rad.radicale(t)
	at.innen = Innen{Wurzel: t.TempDir()}
	os.MkdirAll(at.innen.Home(), 0o700)
	os.WriteFile(at.innen.Home()+"/config.toml", []byte(msgvaultConfig(8090)), 0o600)
	at.a = &Abgleich{Radicale: r, API: api, Run: at.run, Msgvault: "msgvault", Now: func() time.Time { return t0 },
		Relais: &Relais{Person: "anna", Port: 18443, Adressen: func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("192.168.178.20")}, nil }, SystemCAs: "/nicht/da"}}
	if err := at.a.Vorbereiten(at.innen); err != nil {
		t.Fatal(err)
	}
	return at
}

// The first round at an opening: the calendars into the vault; msgvault
// connected to the relay once (password on stdin), the book subscribed
// and a lookup source but never the write target, then synced. Radicale
// sees only reads, all with the archive's own login.
func TestAbgleichFirstRound(t *testing.T) {
	at := neuerAbgleich(t)
	at.rad.setzen("handy-kontakte", fakeSammlung{art: "adressbuch"})
	at.rad.setzen("google-arbeit", fakeSammlung{art: "kalender", anzeig: "Arbeit", ics: ics(vevent("a", "", "A", "x"), vevent("b", "", "B", "x"))})
	at.rad.setzen("privat-sport", fakeSammlung{art: "kalender", ics: ics(vevent("c", "", "C", "x"))})
	at.rad.setzen("abo-ferien", fakeSammlung{art: "kalender", ics: ics(vevent("d", "", "D", "x"))}) // not the archive's
	at.api.buecher = []CardDAVBuch{{ID: 3, URL: "https://kontakte.archiv.invalid:18443/anna/handy-kontakte/", WriteTarget: true, Subscribed: true}}
	at.api.personen = map[int64]Person{1: {ID: 1, DisplayName: "Ben"}, 2: {ID: 2, DisplayName: "Carla"}}

	at.a.Runde(t.Context(), at.innen)

	st := at.a.Stand()
	if st.Fehler != "" || st.Kontakte.Fehler != "" || st.KalenderFehler != "" {
		t.Fatalf("errors: %+v", st)
	}
	if len(st.Kalender) != 2 || st.Kalender[0].Name != "google-arbeit" || st.Kalender[0].Termine != 2 || st.Kalender[1].Termine != 1 {
		t.Fatalf("calendars: %+v", st.Kalender)
	}
	if _, err := os.Stat(kalenderDatei(at.innen, "abo-ferien")); err == nil {
		t.Fatal("a calendar that is not google-*/privat-* came in")
	}
	args := at.run.args()
	want := []string{"add-carddav https://kontakte.archiv.invalid:18443/ msgvault", "carddav books set-role 3 --subscribed --lookup-source", "sync-carddav"}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("commands:\n%q\nwant\n%q", args, want)
	}
	add := at.run.befehle[0]
	pw := at.a.Relais.Passwort()
	if string(add.Eingabe) != string(pw)+"\n" || strings.Contains(strings.Join(append(add.Args, add.Env...), " "), string(pw)) {
		t.Fatal("the relay's password goes on stdin only")
	}
	if strings.Contains(string(add.Eingabe), at.rad.passwort) {
		t.Fatal("Radicale's password reached msgvault")
	}
	if st.Kontakte.Neu != 2 || !st.Kontakte.Verbunden || st.Personen != 2 || st.Kontakte.Karten != 1 {
		t.Fatalf("contacts: %+v, %d persons", st.Kontakte, st.Personen)
	}
	methoden, wer := at.rad.gesehen()
	for i, m := range methoden {
		if !strings.HasPrefix(m, "PROPFIND ") && !strings.HasPrefix(m, "GET ") && !strings.HasPrefix(m, "REPORT ") {
			t.Errorf("Radicale got %s", m)
		}
		if wer[i] != "archiv-anna" {
			t.Errorf("%s as %q", m, wer[i])
		}
	}

	// the next round: connected already, the roles are right, only the sync
	at.api.buecher[0].WriteTarget, at.api.buecher[0].LookupSource = false, true
	at.run.befehle = nil
	at.rad.setzen("google-arbeit", fakeSammlung{art: "kalender", ics: ics(vevent("a", "", "A", "y"))}) // b deleted at the source
	at.rad.weg("privat-sport")                                                                         // the whole calendar gone
	at.a.Runde(t.Context(), at.innen)
	if args := at.run.args(); strings.Join(args, "|") != "sync-carddav" {
		t.Fatalf("second round: %q", args)
	}
	st = at.a.Stand()
	if st.Kalender[0].Termine != 2 || st.Kalender[0].Geloescht != 1 {
		t.Fatalf("a deleted event left the archive: %+v", st.Kalender[0])
	}
	if st.Kalender[1].Name != "privat-sport" || st.Kalender[1].QuelleWegSeit == nil || st.Kalender[1].Termine != 1 {
		t.Fatalf("a calendar gone at the source: %+v", st.Kalender[1])
	}
}

func TestAbgleichSaysWhatIsMissing(t *testing.T) {
	at := neuerAbgleich(t)
	at.a.Runde(t.Context(), at.innen)
	if st := at.a.Stand(); !st.Kontakte.Leer || len(at.run.args()) != 0 {
		t.Fatalf("no address book yet: %+v %v", st.Kontakte, at.run.args())
	}

	at.rad.setzen("handy-kontakte", fakeSammlung{art: "adressbuch"})
	at.run.fehlt = "add-carddav"
	at.a.Runde(t.Context(), at.innen)
	if st := at.a.Stand(); st.Kontakte.Fehler != saysKontakteVerbinden {
		t.Fatalf("a failed connect: %+v", st.Kontakte)
	}
	if _, err := os.Stat(at.innen.KontakteVerbunden()); err == nil {
		t.Fatal("marked connected after a failure")
	}

	at.rad.mu.Lock()
	at.rad.passwort = "geaendert"
	at.rad.mu.Unlock()
	at.a.Runde(t.Context(), at.innen)
	if st := at.a.Stand(); st.Fehler != saysRadicaleAnmeldung {
		t.Fatalf("a refused login: %+v", st)
	}
}

// The card: not set up, closed (nothing from inside the vault), open
// (a count per calendar, deleted ones said, searching later).
func TestTheContactsCardPerState(t *testing.T) {
	wt := neuesWeb(t)
	wt.oeffnen(t)
	if body := wt.rufe("GET", "/tresor", anna, nil).Body.String(); !strings.Contains(body, saysRadicaleFehlt) {
		t.Fatalf("not set up: %s", body)
	}
	at := neuerAbgleich(t)
	wt.w.Abgleich = at.a
	at.rad.setzen("handy-kontakte", fakeSammlung{art: "adressbuch"})
	at.rad.setzen("google-arbeit", fakeSammlung{art: "kalender", anzeig: "Arbeit", ics: ics(vevent("a", "", "A", "x"), vevent("b", "", "B", "x"))})
	at.api.buecher = []CardDAVBuch{{ID: 3, URL: "https://kontakte.archiv.invalid:18443/anna/handy-kontakte/", Subscribed: true, LookupSource: true}}
	at.a.Runde(t.Context(), at.innen)
	at.rad.setzen("google-arbeit", fakeSammlung{art: "kalender", anzeig: "Arbeit", ics: ics(vevent("a", "", "A", "x"))})
	at.a.Runde(t.Context(), at.innen)
	body := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	for _, want := range []string{"Kontakte und Kalender", "Google-Kalender „Arbeit“: 2 Termine, davon 1 an der Quelle gelöscht (bleiben im Archiv)", "Kontakte zuletzt abgeglichen um", "In den Terminen suchen kannst du in einem späteren Schritt", "Jetzt abgleichen"} {
		if !strings.Contains(body, want) {
			t.Fatalf("open page lacks %q:\n%s", want, body)
		}
	}
	wt.tt.Abschliessen(t.Context(), "test")
	body = wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if strings.Contains(body, "Termine,") || !strings.Contains(body, "Die Termine liegen im Tresor") {
		t.Fatalf("closed page: %s", body)
	}
}

// A card deleted on the phone stays in the store and is served to
// msgvault marked; back on the phone, the mark goes.
func TestAContactDeletedOnThePhoneStays(t *testing.T) {
	at := neuerAbgleich(t)
	at.rad.setzen("handy-kontakte", fakeSammlung{art: "adressbuch"})
	at.rad.karte("carla.vcf", vcard("u-carla", "Carla", "+4917011111111"))
	at.a.Runde(t.Context(), at.innen)
	at.rad.karte("ben.vcf", "")
	at.a.Runde(t.Context(), at.innen)
	k, err := kontakteLaden(kontakteDatei(at.innen))
	if err != nil || len(k.Karten) != 2 {
		t.Fatalf("%v %v", k, err)
	}
	ben := k.Karten["uid:u-ben"]
	if ben.GeloeschtAm == nil || !strings.Contains(ben.Ausgabe(), "CATEGORIES:Auf dem Handy gelöscht am ") || !strings.HasSuffix(ben.Ausgabe(), "END:VCARD\r\n") {
		t.Fatalf("Ben: %+v\n%s", ben, ben.Ausgabe())
	}
	st := at.a.Stand()
	if st.Kontakte.Karten != 2 || len(st.Kontakte.Weg) != 1 || st.Kontakte.Weg[0].Name != "Ben" {
		t.Fatalf("%+v", st.Kontakte)
	}
	if GeloeschtNachUID(at.innen)["u-ben"].IsZero() {
		t.Fatal("the persons cannot be marked")
	}
	alterTag := ben.ETag()
	at.rad.karte("ben-neu.vcf", vcard("u-ben", "Ben", "+4915199999999")) // back, under another file name
	at.a.Runde(t.Context(), at.innen)
	k, _ = kontakteLaden(kontakteDatei(at.innen))
	ben = k.Karten["uid:u-ben"]
	if ben.GeloeschtAm != nil || strings.Contains(ben.Ausgabe(), "gelöscht") || ben.Href != "ben.vcf" || ben.ETag() == alterTag {
		t.Fatalf("back on the phone: %+v", ben)
	}
	if len(at.a.Stand().Kontakte.Weg) != 0 {
		t.Fatal("still listed as deleted")
	}
}

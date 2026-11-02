package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeRadicale is a CalDAV/CardDAV server as Radicale answers: Basic auth,
// PROPFIND Depth 1 on a home lists the collections, GET on a calendar
// collection gives all its events as one iCalendar, PROPFIND/REPORT on the
// address book answer with its cards. It records every method it got, so
// a test can say that nothing but reads ever reached it.
type fakeRadicale struct {
	*httptest.Server
	t        *testing.T
	benutzer string
	passwort string

	mu       sync.Mutex
	sammlung map[string]fakeSammlung // name -> collection in /anna/
	methoden []string
	auth     []string // the user of each request
}

type fakeSammlung struct {
	art    string // "kalender" or "adressbuch"
	anzeig string
	ics    string // a calendar's content
}

func neuesRadicale(t *testing.T) *fakeRadicale {
	f := &fakeRadicale{t: t, benutzer: "archiv-anna", passwort: "radicale-geheim-123", sammlung: map[string]fakeSammlung{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeRadicale) setzen(name string, s fakeSammlung) {
	f.mu.Lock()
	f.sammlung[name] = s
	f.mu.Unlock()
}

func (f *fakeRadicale) weg(name string) {
	f.mu.Lock()
	delete(f.sammlung, name)
	f.mu.Unlock()
}

func (f *fakeRadicale) gesehen() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methoden...), append([]string(nil), f.auth...)
}

func (f *fakeRadicale) serve(w http.ResponseWriter, r *http.Request) {
	u, p, ok := r.BasicAuth()
	f.mu.Lock()
	f.methoden = append(f.methoden, r.Method+" "+r.URL.Path)
	f.auth = append(f.auth, u)
	richtig := ok && u == f.benutzer && p == f.passwort
	f.mu.Unlock()
	if !richtig {
		w.Header().Set("WWW-Authenticate", `Basic realm="Radicale"`)
		http.Error(w, "Access to the requested resource forbidden.", http.StatusUnauthorized)
		return
	}
	io.Copy(io.Discard, r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "PROPFIND" && r.URL.Path == "/anna/":
		var b strings.Builder
		b.WriteString(`<?xml version='1.0' encoding='utf-8'?>` + "\n" + `<multistatus xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CR="urn:ietf:params:xml:ns:carddav"><response><href>/anna/</href><propstat><prop><resourcetype><principal/><collection/></resourcetype></prop><status>HTTP/1.1 200 OK</status></propstat></response>`)
		namen := make([]string, 0, len(f.sammlung))
		for n := range f.sammlung {
			namen = append(namen, n)
		}
		sort.Strings(namen)
		for _, n := range namen {
			s := f.sammlung[n]
			typ := "<C:calendar/>"
			if s.art == "adressbuch" {
				typ = "<CR:addressbook/>"
			}
			fmt.Fprintf(&b, `<response><href>/anna/%s/</href><propstat><prop><resourcetype><collection/>%s</resourcetype><displayname>%s</displayname></prop><status>HTTP/1.1 200 OK</status></propstat></response>`, url.PathEscape(n), typ, s.anzeig)
		}
		b.WriteString(`</multistatus>`)
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, b.String())
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/anna/"):
		s, ok := f.sammlung[strings.Trim(strings.TrimPrefix(r.URL.Path, "/anna/"), "/")]
		if !ok || s.art != "kalender" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/calendar")
		io.WriteString(w, s.ics)
	case (r.Method == "PROPFIND" || r.Method == "REPORT") && strings.HasPrefix(r.URL.Path, "/anna/handy-kontakte"):
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version='1.0' encoding='utf-8'?>`+"\n"+`<multistatus xmlns="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav"><response><href>/anna/handy-kontakte/ben.vcf</href><propstat><prop><getetag>"1"</getetag><CR:address-data>BEGIN:VCARD&#13;
VERSION:3.0&#13;
FN:Ben&#13;
TEL:+4915199999999&#13;
END:VCARD&#13;
</CR:address-data></prop><status>HTTP/1.1 200 OK</status></propstat></response></multistatus>`)
	default:
		http.Error(w, "nicht in diesem Fake", http.StatusForbidden)
	}
}

// radicale gives an archive client for the fake, its password in a file.
func (f *fakeRadicale) radicale(t *testing.T) *Radicale {
	t.Helper()
	datei := filepath.Join(t.TempDir(), "radicale-password")
	os.WriteFile(datei, []byte(f.passwort+"\n"), 0o600)
	t.Setenv("RADICALE_URL", f.URL+"/")
	t.Setenv("RADICALE_USER", f.benutzer)
	t.Setenv("RADICALE_PASSWORD_FILE", datei)
	r, err := RadicaleAusUmgebung("anna")
	if err != nil || r == nil {
		t.Fatalf("RadicaleAusUmgebung: %v %v", r, err)
	}
	return r
}

func ics(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Europe/Berlin\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
		strings.Join(events, "") + "END:VCALENDAR\r\n"
}

func vevent(uid, rid, titel, stamp string) string {
	s := "BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:" + stamp + "\r\nDTSTART;TZID=Europe/Berlin:20261103T090000\r\nSUMMARY:" + titel + "\r\n"
	if rid != "" {
		s += "RECURRENCE-ID;TZID=Europe/Berlin:" + rid + "\r\n"
	}
	return s + "BEGIN:VALARM\r\nACTION:DISPLAY\r\nUID:alarm-im-termin\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT\r\n"
}

func TestRadicaleConfigFromTheEnvironment(t *testing.T) {
	for _, k := range []string{"RADICALE_URL", "RADICALE_USER", "RADICALE_PASSWORD_FILE"} {
		t.Setenv(k, "")
	}
	if r, err := RadicaleAusUmgebung("anna"); r != nil || err != nil {
		t.Fatalf("nothing set: %v %v", r, err)
	}
	t.Setenv("RADICALE_URL", "http://host.containers.internal:5232/radicale/")
	t.Setenv("RADICALE_USER", "archiv-anna")
	if _, err := RadicaleAusUmgebung("anna"); err == nil {
		t.Fatal("a URL with a path must be refused: Radicale's hrefs are from its root")
	}
	t.Setenv("RADICALE_URL", "http://host.containers.internal:5232")
	r, err := RadicaleAusUmgebung("anna")
	if err != nil || r.Basis.String() != "http://host.containers.internal:5232/" || r.PasswortDatei != "/run/secrets/radicale-password" || r.Home() != "/anna/" {
		t.Fatalf("%+v %v", r, err)
	}
}

// The archive reads only: the client refuses to send anything but a read.
func TestRadicaleClientOnlyReads(t *testing.T) {
	f := neuesRadicale(t)
	r := f.radicale(t)
	for _, m := range []string{"PUT", "DELETE", "PROPPATCH", "MKCOL", "MOVE", "POST"} {
		if _, err := r.anfrage(t.Context(), m, "/anna/handy-kontakte/x.vcf", "", nil, nil); err == nil {
			t.Errorf("%s went out", m)
		}
	}
	if m, _ := f.gesehen(); len(m) != 0 {
		t.Fatalf("reached Radicale: %v", m)
	}
}

func TestRadicaleListsTheHomeAndFetchesACalendar(t *testing.T) {
	f := neuesRadicale(t)
	f.setzen("handy-kontakte", fakeSammlung{art: "adressbuch", anzeig: "Handy-Kontakte"})
	f.setzen("google-arbeit", fakeSammlung{art: "kalender", anzeig: "Arbeit", ics: ics(vevent("a1", "", "Termin", "20261101T000000Z"))})
	f.setzen("google-kontakte", fakeSammlung{art: "adressbuch"})
	f.setzen("privat-sport", fakeSammlung{art: "kalender"})
	r := f.radicale(t)
	s, err := r.Sammlungen(t.Context())
	if err != nil || len(s) != 4 {
		t.Fatalf("%+v %v", s, err)
	}
	lesen := map[string]bool{}
	for _, x := range s {
		if x.KalenderZumLesen() {
			lesen[x.Name] = true
		}
	}
	if !lesen["google-arbeit"] || !lesen["privat-sport"] || len(lesen) != 2 {
		t.Fatalf("calendars to read: %v", lesen)
	}
	b, err := r.KalenderHolen(t.Context(), "google-arbeit")
	if err != nil || !strings.Contains(string(b), "UID:a1") {
		t.Fatalf("%s %v", b, err)
	}
	// a refused login is its own sentence
	f.mu.Lock()
	f.passwort = "anders"
	f.mu.Unlock()
	if _, err := r.Sammlungen(t.Context()); err != errRadicaleAnmeldung {
		t.Fatalf("refused login: %v", err)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type webTest struct {
	tt     *testTresor
	w      *Web
	inbox  string
	msgv   *httptest.Server
	mu     sync.Mutex
	gesehn []*http.Request // what reached "msgvault"
}

func neuesWeb(t *testing.T) *webTest {
	t.Helper()
	tt := neuerTresor(t)
	wt := &webTest{tt: tt, inbox: t.TempDir()}
	wt.msgv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		wt.mu.Lock()
		wt.gesehn = append(wt.gesehn, r.Clone(context.Background()))
		wt.mu.Unlock()
		rw.Write([]byte("msgvault " + r.URL.Path))
	}))
	t.Cleanup(wt.msgv.Close)
	u, _ := url.Parse(wt.msgv.URL)
	port, _ := strconv.Atoi(u.Port())
	eingang := &Eingang{Dir: wt.inbox}
	prot := LadeProtokoll(filepath.Join(t.TempDir(), "archiv-stand.json"))
	im := &Importeur{Eingang: eingang, Protokoll: prot, Run: &fakeLauf{}}
	wt.w = &Web{Person: "anna", Secret: "s3cret", Tresor: tt.Tresor, Importeur: im, Eingang: eingang, Protokoll: prot, Archiv: archivProxy(port)}
	return wt
}

var anna = map[string]string{proxyHeader: "s3cret", "Remote-User": "anna"}

func (wt *webTest) rufe(method, pfad string, hdr map[string]string, form url.Values) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, pfad, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	wt.w.ServeHTTP(rec, r)
	return rec
}

func (wt *webTest) oeffnen(t *testing.T) {
	t.Helper()
	if rec := wt.rufe("POST", "/tresor/anlegen", anna, url.Values{"passwort": {geheim}, "wiederholung": {geheim}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("anlegen: %d %s", rec.Code, rec.Body)
	}
	wt.tt.bereit(t)
}

// Every POST route answers in every state: a page with a sentence or a
// redirect to one, never a 404/405, an empty body or a raw error.
func TestEveryPostRouteAnswers(t *testing.T) {
	routen := map[string]url.Values{
		"/tresor/anlegen":      {"passwort": {"kurz"}, "wiederholung": {"kurz"}},
		"/tresor/oeffnen":      {"passwort": {"falsch-falsch-falsch"}},
		"/tresor/abschliessen": {},
		"/tresor/signal":       {"passphrase": {"12"}},
		"/tresor/imap":         {"host": {""}},
	}
	for _, zustand := range []string{"uneingerichtet", "offen", "verschlossen"} {
		wt := neuesWeb(t)
		switch zustand {
		case "offen":
			wt.oeffnen(t)
		case "verschlossen":
			wt.oeffnen(t)
			wt.tt.Abschliessen(context.Background(), "test")
		}
		for pfad, form := range routen {
			rec := wt.rufe("POST", pfad, anna, form)
			if rec.Code == 404 || rec.Code == 405 || rec.Code == 500 {
				t.Errorf("%s %s: %d", zustand, pfad, rec.Code)
				continue
			}
			if rec.Code == http.StatusSeeOther {
				if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/tresor") {
					t.Errorf("%s %s: redirects to %q", zustand, pfad, loc)
				}
				continue
			}
			body := rec.Body.String()
			if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(body, `class="meldung`) {
				t.Errorf("%s %s: %d without a sentence: %.200s", zustand, pfad, rec.Code, body)
			}
			if strings.Contains(strings.ToLower(body), "error") || strings.Contains(body, "%!") {
				t.Errorf("%s %s: a raw error on the page: %.300s", zustand, pfad, body)
			}
		}
	}
}

func TestPagesPerState(t *testing.T) {
	wt := neuesWeb(t)
	body := wt.rufe("GET", "/", anna, nil).Body.String()
	for _, want := range []string{"Tresor anlegen", "Vaultwarden", "Admin der Box es lesen", `data-laeuft=`} {
		if !strings.Contains(body, want) {
			t.Fatalf("uneingerichtet lacks %q", want)
		}
	}
	if rec := wt.rufe("POST", "/tresor/anlegen", anna, url.Values{"passwort": {geheim}, "wiederholung": {geheim + "x"}}); !strings.Contains(rec.Body.String(), saysPasswortAnders) {
		t.Fatalf("mismatch: %s", rec.Body)
	}
	wt.oeffnen(t)
	body = wt.rufe("GET", "/tresor?m=angelegt", anna, nil).Body.String()
	for _, want := range []string{"Abschließen", "Archiv durchsuchen", "Signal-Passphrase", "IMAP", "fehlt", "um 12:00"} {
		if !strings.Contains(body, want) {
			t.Fatalf("offen lacks %q:\n%s", want, body)
		}
	}
	if rec := wt.rufe("POST", "/tresor/signal", anna, url.Values{"passphrase": {"12345 12345 12345 12345 12345 12345"}, "telefon": {"0049 151 1234567"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("signal: %d %s", rec.Code, rec.Body)
	}
	body = wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if strings.Contains(body, "123451234512345") || strings.Contains(body, "1511234567") {
		t.Fatal("a stored key is shown again")
	}
	if strings.Count(body, "hinterlegt am") != 2 {
		t.Fatalf("passphrase and number not marked as stored:\n%s", body)
	}
	innen, _ := wt.tt.Innen()
	if tel, _ := (Schluessel{innen.Schluessel()}).Lesen(SchluesselTelefon); string(tel) != "+491511234567" {
		t.Fatalf("number stored as %q", tel)
	}
	if rec := wt.rufe("POST", "/tresor/abschliessen", anna, url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("abschliessen: %d", rec.Code)
	}
	body = wt.rufe("GET", "/", anna, nil).Body.String()
	if !strings.Contains(body, "Archiv öffnen") || strings.Contains(body, "Signal-Passphrase (30") {
		t.Fatalf("verschlossen:\n%s", body)
	}
	if rec := wt.rufe("POST", "/tresor/signal", anna, url.Values{"passphrase": {passphrase}}); rec.Code != http.StatusLocked {
		t.Fatalf("a key while closed: %d", rec.Code)
	}
}

func TestWhoMayIn(t *testing.T) {
	wt := neuesWeb(t)
	if rec := wt.rufe("GET", "/healthz", nil, nil); rec.Code != 200 {
		t.Fatalf("healthz: %d", rec.Code)
	}
	for name, hdr := range map[string]map[string]string{
		"no header":      nil,
		"forged user":    {"Remote-User": "anna"},
		"wrong secret":   {proxyHeader: "falsch", "Remote-User": "anna"},
		"secret no user": {proxyHeader: "s3cret"},
	} {
		want := http.StatusUnauthorized
		if name == "secret no user" {
			want = http.StatusForbidden
		}
		if rec := wt.rufe("GET", "/tresor", hdr, nil); rec.Code != want {
			t.Errorf("%s: %d, want %d", name, rec.Code, want)
		}
	}
	if rec := wt.rufe("GET", "/tresor", map[string]string{proxyHeader: "s3cret", "Remote-User": "ben"}, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "jemand anderem") {
		t.Fatalf("another person: %d", rec.Code)
	}
	wt.w.Secret = ""
	if rec := wt.rufe("GET", "/tresor", map[string]string{proxyHeader: "", "Remote-User": "anna"}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no secret configured: %d", rec.Code)
	}
}

func TestPostFromAnotherSiteIsRefused(t *testing.T) {
	wt := neuesWeb(t)
	wt.oeffnen(t)
	hdr := map[string]string{proxyHeader: "s3cret", "Remote-User": "anna", "Origin": "https://boese.example", "X-Forwarded-Host": "archiv.dopp.cloud"}
	if rec := wt.rufe("POST", "/tresor/abschliessen", hdr, url.Values{}); rec.Code != http.StatusForbidden || wt.tt.Stand().Zustand != Offen {
		t.Fatalf("cross-site close: %d %v", rec.Code, wt.tt.Stand().Zustand)
	}
	hdr["Origin"] = "https://archiv.dopp.cloud"
	if rec := wt.rufe("POST", "/tresor/abschliessen", hdr, url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("same-site close: %d", rec.Code)
	}
}

// The person's browser reaches msgvault with the key added by the
// supervisor; Caddy's headers and Authelia's cookie stay behind.
func TestTheBrowserGetsMsgvaultWithTheKey(t *testing.T) {
	wt := neuesWeb(t)
	if rec := wt.rufe("GET", "/api/v1/stats", anna, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("closed: %d", rec.Code)
	}
	wt.oeffnen(t)
	hdr := map[string]string{proxyHeader: "s3cret", "Remote-User": "anna", "Remote-Groups": "family", "Cookie": "authelia_session=abc; andere=1", "X-Api-Key": "von-aussen", "X-Forwarded-Proto": "https"}
	rec := wt.rufe("GET", "/api/v1/stats", hdr, nil)
	if rec.Code != 200 || rec.Body.String() != "msgvault /api/v1/stats" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := wt.rufe("GET", "/", anna, nil); rec.Body.String() != "msgvault /" {
		t.Fatalf("/ while open is msgvault's UI: %s", rec.Body)
	}
	r := wt.gesehn[0]
	if r.Header.Get("X-Api-Key") != wt.tt.APIKey() || wt.tt.APIKey() == "" {
		t.Fatalf("key %q", r.Header.Get("X-Api-Key"))
	}
	for _, h := range []string{proxyHeader, "Remote-User", "Remote-Groups"} {
		if r.Header.Get(h) != "" {
			t.Errorf("%s reached msgvault", h)
		}
	}
	if c := r.Header.Get("Cookie"); strings.Contains(c, "authelia") || !strings.Contains(c, "andere=1") {
		t.Errorf("cookies %q", c)
	}
	if r.Header.Get("X-Forwarded-Proto") != "https" {
		t.Error("the scheme Caddy saw is lost")
	}
}

// An agent's bearer goes to msgvault as it came, only while open.
func TestAgentsBearer(t *testing.T) {
	wt := neuesWeb(t)
	agent := map[string]string{"Authorization": "Bearer der-schluessel"}
	rec := wt.rufe("GET", "/api/v1/search?q=x", agent, nil)
	var f map[string]string
	json.Unmarshal(rec.Body.Bytes(), &f)
	if rec.Code != http.StatusLocked || f["fehler"] != saysAbgeschlossen {
		t.Fatalf("closed: %d %s", rec.Code, rec.Body)
	}
	wt.oeffnen(t)
	rec = wt.rufe("GET", "/api/v1/search?q=x", agent, nil)
	if rec.Code != 200 {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	r := wt.gesehn[len(wt.gesehn)-1]
	if r.Header.Get("Authorization") != "Bearer der-schluessel" || r.Header.Get("X-Api-Key") != "" {
		t.Fatalf("the agent's request was changed: auth %q key %q", r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"))
	}
	// a forged Remote-User on the agent's way gets no key either
	agent["Remote-User"] = "anna"
	wt.rufe("GET", "/api/v1/search?q=y", agent, nil)
	if r := wt.gesehn[len(wt.gesehn)-1]; r.Header.Get("X-Api-Key") != "" {
		t.Fatal("a forged Remote-User got msgvault's key")
	}
}

// The password reaches no log, also when a program puts it in its error,
// and the handler wipes its copy.
func TestPasswordNeverInTheLog(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	wt := neuesWeb(t)
	wt.oeffnen(t)
	wt.rufe("POST", "/tresor/abschliessen", anna, url.Values{})
	wt.rufe("POST", "/tresor/oeffnen", anna, url.Values{"passwort": {"Falsches-Passwort-99"}})
	wt.tt.mounter.mountErr = errors.New("gocryptfs said: bad password " + geheim)
	if rec := wt.rufe("POST", "/tresor/oeffnen", anna, url.Values{"passwort": {geheim}}); rec.Code != 500 || strings.Contains(rec.Body.String(), geheim) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	wt.rufe("POST", "/tresor/oeffnen", anna, url.Values{"passwort": {geheim}})
	wt.tt.bereit(t)
	wt.rufe("POST", "/tresor/signal", anna, url.Values{"passphrase": {passphrase}, "telefon": {"+4915112345678"}})
	for _, s := range []string{geheim, "Falsches-Passwort-99", passphrase} {
		if strings.Contains(buf.String(), s) {
			t.Fatalf("%q in the log:\n%s", s, buf.String())
		}
	}
	if !strings.Contains(buf.String(), "***") {
		t.Fatalf("the program's error was not logged at all, so the test proves nothing:\n%s", buf.String())
	}
	for _, pw := range wt.tt.mounter.pws {
		if strings.Contains(string(pw), "Passwort") {
			t.Fatalf("a password stayed in memory after its request: %q", pw)
		}
	}
}

// --- the status file

func TestStatusSaysWaitingBackupsAndErrors(t *testing.T) {
	dir := t.TempDir()
	legen(t, dir, "signal/signal-1.backup", 5, tag)
	legen(t, dir, "signal/signal-2.backup", 5, tag)
	e := &Eingang{Dir: dir}
	w := wartende(e)
	prot := LadeProtokoll(filepath.Join(t.TempDir(), "s.json"))
	zu := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	prot.Zu(zu)
	ankunft := *w["signal"].AeltesteSeit

	// two days: no finding yet
	s := statusBauen("anna", Stand{Zustand: Verschlossen}, w, prot, "", false, "https://archiv.dopp.cloud", ankunft.Add(48*time.Hour))
	if len(s.Findings) != 0 || s.Wartend["signal"].Wartend != 2 || s.Zustand != Verschlossen {
		t.Fatalf("two days: %+v", s)
	}
	s = statusBauen("anna", Stand{Zustand: Verschlossen}, w, prot, "", false, "https://archiv.dopp.cloud", ankunft.Add(73*time.Hour))
	if len(s.Findings) != 1 || s.Findings[0].Severity != "gelb" || !strings.Contains(s.Findings[0].Detail, "Dein Archiv ist seit") || !strings.Contains(s.Findings[0].Detail, "2 Sicherungen warten") {
		t.Fatalf("three days: %+v", s.Findings)
	}
	// open: the waiting is the import's business, not a finding
	if s := statusBauen("anna", Stand{Zustand: Offen, Bereit: true}, w, prot, "", false, "", ankunft.Add(100*time.Hour)); len(s.Findings) != 0 {
		t.Fatalf("open: %+v", s.Findings)
	}
	prot.Fehlschlag(Fehler{Am: tag, Quelle: "signal", Datei: "signal-1.backup", Satz: saysSignalFalsch})
	s = statusBauen("anna", Stand{Zustand: Offen, Bereit: true}, w, prot, "", false, "https://archiv.dopp.cloud", tag)
	if len(s.Findings) != 1 || s.Findings[0].Detail != saysSignalFalsch || !strings.Contains(s.Findings[0].Fix, "neu ein") || s.LetzterFehler == nil {
		t.Fatalf("error: %+v", s)
	}
	prot.Eingelesen("signal", "signal-0.backup", Zahlen{SMS: 7, MMS: 3}, tag)
	b, _ := json.Marshal(statusBauen("anna", Stand{Zustand: Offen, Bereit: true}, w, prot, "", false, "", tag))
	for _, want := range []string{`"zustand":"offen"`, `"wartend":{`, `"letzter_import":{"signal":{`, `"sms":7`, `"at":`, `"detail":"Offen; 2 Signal-Sicherungen warten; Signal zuletzt am`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("status lacks %s:\n%s", want, b)
		}
	}
}

func TestStatusFileIsWrittenOutsideAndWhole(t *testing.T) {
	p := filepath.Join(t.TempDir(), "laib-status.json")
	statusSchreiben(p, statusDatei{At: "x", Detail: "y", Zustand: Uneingerichtet})
	b, err := os.ReadFile(p)
	var s map[string]any
	if err != nil || json.Unmarshal(b, &s) != nil || s["zustand"] != "uneingerichtet" {
		t.Fatalf("%v %s", err, b)
	}
}

func TestStatusSaysOneBackupInTheSingular(t *testing.T) {
	dir := t.TempDir()
	legen(t, dir, "signal/signal-1.backup", 5, tag)
	s := statusBauen("anna", Stand{Zustand: Verschlossen}, wartende(&Eingang{Dir: dir}), LadeProtokoll(filepath.Join(t.TempDir(), "s.json")), "", false, "", tag)
	if s.Detail != "Abgeschlossen; 1 Signal-Sicherung wartet" {
		t.Fatalf("%q", s.Detail)
	}
}

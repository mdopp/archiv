package main

import (
	"bufio"
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
	data   string
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
	data := t.TempDir()
	prot := LadeProtokoll(filepath.Join(data, "archiv-stand.json"))
	sch := Schluessel{Dir: filepath.Join(data, "schluessel")}
	tt.Empfaenger = filepath.Join(data, "briefkasten.pub")
	bk := &Briefkasten{Eingang: eingang, Letzte: filepath.Join(wt.inbox, "letzte"), Post: filepath.Join(data, "briefkasten"), Empfaenger: tt.Empfaenger,
		Gesehen: &Gesehen{Dir: filepath.Join(data, "gesehen")}, Protokoll: prot}
	pb := &Postbote{Post: bk.Post, Eingang: eingang, Letzte: bk.Letzte, Schluessel: sch, Protokoll: prot, Run: &fakeMsgvault{}}
	wt.data = data
	wt.w = &Web{Passkeys: &Passkeys{Datei: filepath.Join(data, "schluessel", "passkeys.json")}, Person: "anna", Secret: "s3cret", HausSecret: "haus-geheim", Tresor: tt.Tresor, Briefkasten: bk, Postbote: pb, Eingang: eingang,
		Protokoll: prot, Schluessel: sch, Letzte: bk.Letzte, Archiv: archivProxy(port)}
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
		"/tresor/anlegen":              {"passwort": {"kurz"}, "wiederholung": {"kurz"}},
		"/tresor/oeffnen":              {"passwort": {"falsch-falsch-falsch"}},
		"/tresor/abschliessen":         {},
		"/tresor/signal":               {"passphrase": {"12"}},
		"/tresor/imap":                 {"host": {""}},
		"/tresor/abgleich":             {},
		"/tresor/personen/entscheiden": {"id": {"7"}, "token": {"t"}, "antwort": {"ja"}},
		"/tresor/personen/zusammen":    {"a": {"1"}, "b": {"2"}},
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
	for _, want := range []string{"Abschließen", "Archiv durchsuchen", "Signal-Passphrase", "Postfach eintragen", "Passphrase fehlt", "Kein Postfach eingetragen", "um 12:00", "64-stelligen Schlüssel"} {
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
	if !strings.Contains(body, "Passphrase hinterlegt am") || !strings.Contains(body, "Nummer +49 … 67") || !strings.Contains(body, "Passphrase oder Nummer ändern") || !strings.Contains(body, "ersetzt das Bisherige") {
		t.Fatalf("the page does not say what is stored and that a new entry replaces it:\n%s", body)
	}
	if rec := wt.rufe("POST", "/tresor/imap", anna, url.Values{"host": {"imap.gmail.com"}, "benutzer": {"anna@example.org"}, "passwort": {"abcd efgh ijkl mnop"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("imap: %d %s", rec.Code, rec.Body)
	}
	body = wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if !strings.Contains(body, "Postfach <b>anna@example.org</b> auf imap.gmail.com") || !strings.Contains(body, "ersetzt anna@example.org") || strings.Contains(body, "abcd efgh") {
		t.Fatalf("the mail card does not name the stored mailbox, or shows the password:\n%s", body)
	}
	// outside the vault, owner-only
	if tel, _ := wt.w.Schluessel.Lesen(SchluesselTelefon); string(tel) != "+491511234567" || !strings.HasPrefix(wt.w.Schluessel.Dir, wt.data) {
		t.Fatalf("number stored as %q in %s", tel, wt.w.Schluessel.Dir)
	}
	// the passphrase inside the vault, owner-only, and nowhere outside
	innen, _ := wt.tt.Innen()
	if fi, _ := os.Stat(innen.SignalPassphrase()); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("the passphrase is not in the vault, 0600")
	}
	if b, _ := os.ReadFile(innen.SignalPassphrase()); string(b) != "123451234512345123451234512345" {
		t.Fatalf("the passphrase in the vault: %d bytes", len(b))
	}
	if _, err := os.Stat(filepath.Join(wt.w.Schluessel.Dir, altSignal)); !os.IsNotExist(err) {
		t.Fatal("the passphrase was also stored outside the vault")
	}
	if !strings.Contains(body, "Deine Signal-Passphrase liegt im Tresor") || !strings.Contains(body, "während es offen ist") {
		t.Fatal("the page does not say where the passphrase is")
	}
	if rec := wt.rufe("POST", "/tresor/abschliessen", anna, url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("abschliessen: %d", rec.Code)
	}
	body = wt.rufe("GET", "/", anna, nil).Body.String()
	if !strings.Contains(body, "Archiv öffnen") || strings.Contains(body, "Signal-Passphrase (30") || !strings.Contains(body, saysSignalZu) || strings.Contains(body, "Passphrase fehlt") {
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

func TestStatusSaysWaitingPacketsAndErrors(t *testing.T) {
	dir := t.TempDir()
	legen(t, dir, "signal/signal-1.backup", 5, tag)
	w := wartende(&Eingang{Dir: dir})
	post := t.TempDir()
	os.WriteFile(filepath.Join(post, "20261001T030000Z-signal.age"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(post, "20261002T030000Z-signal.age"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(post, ".20261003T030000Z-signal.age.tmp"), []byte("x"), 0o600) // unfinished: not counted
	bk := briefkastenStand(post)
	if bk.Wartend != 2 || bk.Aeltestes == nil || !bk.Aeltestes.Equal(tag) {
		t.Fatalf("briefkasten %+v", bk)
	}
	prot := LadeProtokoll(filepath.Join(t.TempDir(), "s.json"))
	prot.Zu(time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC))
	lage := Lage{Person: "anna", Stand: Stand{Zustand: Verschlossen}, Wartend: w, Briefkasten: bk, Protokoll: prot, URL: "https://archiv.dopp.cloud"}

	lage.Jetzt = tag.Add(48 * time.Hour) // two days: no finding yet
	if s := statusBauen(lage); len(s.Findings) != 0 || s.Briefkasten.Wartend != 2 || s.Zustand != Verschlossen {
		t.Fatalf("two days: %+v", s)
	}
	lage.Jetzt = tag.Add(73 * time.Hour)
	s := statusBauen(lage)
	if len(s.Findings) != 1 || s.Findings[0].Severity != "gelb" || s.Findings[0].Detail != "Dein Archiv ist seit 30.09.2026 18:00 zu, 2 Pakete mit neuen Nachrichten warten." {
		t.Fatalf("three days: %+v", s.Findings)
	}
	lage.Stand = Stand{Zustand: Offen, Bereit: true} // open: delivering is the Postbote's business
	if s := statusBauen(lage); len(s.Findings) != 0 {
		t.Fatalf("open: %+v", s.Findings)
	}
	lage.Stand, lage.FehlenSchluessel = Stand{Zustand: Verschlossen}, true
	lage.Jetzt = tag
	if s := statusBauen(lage); len(s.Findings) != 1 || !strings.Contains(s.Findings[0].Fix, "Schlüssel") {
		t.Fatalf("keys missing: %+v", s.Findings)
	}
	lage.FehlenSchluessel = false
	prot.Fehlschlag(Fehler{Am: tag, Quelle: "signal", Datei: "signal-1.backup", Satz: saysSignalFalsch})
	s = statusBauen(lage)
	if len(s.Findings) != 1 || s.Findings[0].Detail != saysSignalFalsch || !strings.Contains(s.Findings[0].Fix, "neu ein") || s.LetzterFehler == nil {
		t.Fatalf("error: %+v", s)
	}
	prot.Gepackt("signal", Gepackt{Am: tag, Sicherung: "signal-1.backup", Paket: "p", Nachrichten: 4})
	prot.Eingelesen("signal", "20261001T030000Z-signal.age", Zahlen{SMS: 7, MMS: 3}, tag)
	b, _ := json.Marshal(statusBauen(lage))
	for _, want := range []string{`"zustand":"verschlossen"`, `"briefkasten":{"wartend":2,"aeltestes":"2026-10-01T03:00:00Z"}`, `"letztes_paket":{"signal":{`, `"letzter_import":{"signal":{`, `"sms":7`,
		`"detail":"Abgeschlossen; 2 Pakete warten im Briefkasten; 1 Signal-Sicherung wartet im Eingang; Signal zuletzt am`} {
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
	s := statusBauen(Lage{Person: "anna", Stand: Stand{Zustand: Verschlossen}, Wartend: wartende(&Eingang{Dir: dir}), Protokoll: LadeProtokoll(filepath.Join(t.TempDir(), "s.json")), Jetzt: tag})
	if s.Detail != "Abgeschlossen; 1 Signal-Sicherung wartet im Eingang" {
		t.Fatalf("%q", s.Detail)
	}
}

// Haus' way to the restore files: only with its secret; media only while
// open (they are age-encrypted in letzte/, the private key is in the vault).
func TestHausGetsMediaOnlyWhileOpen(t *testing.T) {
	wt := neuesWeb(t)
	haus := map[string]string{hausHeader: "haus-geheim"}
	if rec := wt.rufe("GET", "/intern/empfaenger", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no secret: %d", rec.Code)
	}
	if rec := wt.rufe("GET", "/intern/empfaenger", map[string]string{hausHeader: "falsch"}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %d", rec.Code)
	}
	wt.oeffnen(t)
	rec := wt.rufe("GET", "/intern/empfaenger", haus, nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "age1") {
		t.Fatalf("empfaenger: %d %s", rec.Code, rec.Body)
	}
	empf, _ := empfaengerLesen(wt.tt.Empfaenger)
	src := filepath.Join(t.TempDir(), "IMG-1.jpg")
	os.WriteFile(src, []byte("BILD-INHALT"), 0o644)
	if err := verschluesseln(src, filepath.Join(wt.w.Letzte, "whatsapp", "Media", "WhatsApp Images", "IMG-1.jpg.age"), empf); err != nil {
		t.Fatal(err)
	}
	rec = wt.rufe("GET", "/intern/whatsapp/medien/WhatsApp%20Images/IMG-1.jpg", haus, nil)
	if rec.Code != 200 || rec.Body.String() != "BILD-INHALT" || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("medium: %d %q %s", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	for _, p := range []string{"/intern/whatsapp/medien/../../../tresor/gocryptfs.conf", "/intern/whatsapp/medien/%2e%2e/%2e%2e/x"} {
		if rec := wt.rufe("GET", p, haus, nil); rec.Code == 200 {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	wt.tt.Abschliessen(context.Background(), "t")
	if rec := wt.rufe("GET", "/intern/whatsapp/medien/WhatsApp%20Images/IMG-1.jpg", haus, nil); rec.Code != http.StatusLocked {
		t.Fatalf("closed: %d", rec.Code)
	}
}

func TestThePageShowsWhatRunsAndHowFar(t *testing.T) {
	wt := neuesWeb(t)
	wt.oeffnen(t)
	wt.w.Mailholer = &Mailholer{}
	wt.w.Mailholer.gang = Gang{Was: "anna@example.org", Quelle: "mail", Seit: time.Now(), Fertig: 2300, Gesamt: 48000, Bis: time.Date(2023, 3, 9, 0, 0, 0, 0, time.Local)}
	wt.w.Postbote.gang = Gang{Was: "20261102T080204Z-signal.age", Quelle: "signal", Seit: time.Now(), Gesamt: 41437}
	body := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	for _, want := range []string{"Gerade in Arbeit", `<progress value=2300 max=48000>`, "2.300 von 48.000 Mails abgeholt, bisher bis März 2023", "41.437 Signal-Nachrichten ins Archiv übernehmen", "id=jetzt data-live", "id=karte-mail data-live", "new EventSource('/tresor/live')"} {
		if !strings.Contains(body, want) {
			t.Fatalf("lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "auffrischen") || strings.Contains(body, "setInterval") {
		t.Fatal("the page still reloads itself on a timer")
	}
	if p := os.Getenv("SEITE_NACH"); p != "" {
		os.WriteFile(p, []byte(body), 0o644)
	}
	// WhatsApp counts as it goes, without a total (owner, 2026-11-02: „seitdem keine Updates“)
	wt.w.Postbote.gang = Gang{Was: "Databases/msgstore.db.crypt15", Quelle: "whatsapp", Seit: time.Now(), Fertig: 123456, Neu: 120001}
	body = wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if !strings.Contains(body, "WhatsApp-Nachrichten ins Archiv übernehmen") || !strings.Contains(body, "Bisher 123.456 durchgesehen, davon 120.001 neu im Archiv.") || strings.Contains(body, "erst am Ende") {
		t.Fatalf("WhatsApp's progress:\n%s", body)
	}
}

// The page listens instead of reloading: a change reaches an open stream
// at once; nobody else gets one.
func TestTheLiveStreamSaysEveryChange(t *testing.T) {
	wt := neuesWeb(t)
	wt.w.Puls = &Puls{}
	srv := httptest.NewServer(wt.w)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/tresor/live", nil)
	for k, v := range anna {
		req.Header.Set(k, v)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%v %+v", err, res)
	}
	r := bufio.NewReader(res.Body)
	r.ReadString('\n') // retry:
	r.ReadString('\n')
	wt.w.Puls.Senden()
	zeile, err := r.ReadString('\n')
	if err != nil || zeile != "event: neu\n" {
		t.Fatalf("no event after a change: %q %v", zeile, err)
	}
	wt.w.Puls.Schluss()
	if _, err := io.ReadAll(r); err != nil {
		t.Fatalf("the stream did not end on shutdown: %v", err)
	}
	res.Body.Close()
	fremd := wt.rufe("GET", "/tresor/live", map[string]string{proxyHeader: "s3cret", "Remote-User": "ben"}, nil)
	if fremd.Code == 200 {
		t.Fatal("another person gets the stream")
	}
	if zaehltAlsNutzung(httptest.NewRequest("GET", "/tresor/live", nil)) {
		t.Fatal("an open tab would keep the vault open forever")
	}
}

// A passkey opens the vault: the password that just opened a closed vault
// is kept, wrapped under the passkey's PRF secret; only that secret
// unwraps it, and a vault already open never stores a password unchecked.
func TestAPasskeyOpensTheVaultWithThePasswordItWasGiven(t *testing.T) {
	wt := neuesWeb(t)
	wt.oeffnen(t)
	prf := bytes.Repeat([]byte{7}, 32)
	salt := bytes.Repeat([]byte{9}, 32)
	pk := url.Values{"passwort": {"irgendwas-falsches"}, "pk_neu": {"1"}, "pk_id": {"cred-1"}, "pk_salt": {b64.EncodeToString(salt)}, "pk_prf": {b64.EncodeToString(prf)}}
	wt.rufe("POST", "/tresor/oeffnen", anna, pk) // already open: Oeffnen does not check the password
	if len(wt.w.Passkeys.Liste()) != 0 {
		t.Fatal("a password was kept that never opened the vault")
	}
	if rec := wt.rufe("POST", "/tresor/abschliessen", anna, url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("abschliessen: %d", rec.Code)
	}
	body := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if strings.Contains(body, `id=mit-passkey`) || !strings.Contains(body, "Künftig mit Passkey öffnen") {
		t.Fatalf("closed, no passkey yet: %s", body)
	}
	pk.Set("passwort", geheim)
	rec := wt.rufe("POST", "/tresor/oeffnen", anna, pk)
	if !strings.Contains(rec.Body.String(), saysPasskeyNeu) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	roh, _ := os.ReadFile(wt.w.Passkeys.Datei)
	if bytes.Contains(roh, []byte(geheim)) {
		t.Fatal("the password lies in the clear")
	}
	if fi, _ := os.Stat(wt.w.Passkeys.Datei); fi.Mode().Perm() != 0o600 {
		t.Fatal("passkeys.json is not 0600")
	}
	wt.tt.bereit(t)
	wt.rufe("POST", "/tresor/abschliessen", anna, url.Values{})
	zu := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if !strings.Contains(zu, "Mit Passkey öffnen") {
		t.Fatal("the closed page offers no passkey")
	}
	if p := os.Getenv("SEITE_ZU"); p != "" {
		os.WriteFile(p, []byte(zu), 0o644)
	}
	falsch := wt.rufe("POST", "/tresor/oeffnen-passkey", anna, url.Values{"id": {"cred-1"}, "prf": {b64.EncodeToString(bytes.Repeat([]byte{8}, 32))}})
	if falsch.Code != http.StatusForbidden || wt.w.Tresor.Stand().Zustand != Verschlossen {
		t.Fatalf("another secret opened it: %d", falsch.Code)
	}
	if rec := wt.rufe("POST", "/tresor/oeffnen-passkey", anna, url.Values{"id": {"cred-1"}, "prf": {b64.EncodeToString(prf)}}); rec.Code != http.StatusSeeOther || wt.w.Tresor.Stand().Zustand != Offen {
		t.Fatalf("the passkey did not open it: %d %s", rec.Code, rec.Body)
	}
	wt.tt.bereit(t)
	if !strings.Contains(wt.rufe("GET", "/tresor", anna, nil).Body.String(), "Entfernen") {
		t.Fatal("the open page does not list the passkey")
	}
	wt.rufe("POST", "/tresor/passkey/weg", anna, url.Values{"id": {"cred-1"}})
	if len(wt.w.Passkeys.Liste()) != 0 {
		t.Fatal("not removed")
	}
	if o := wt.rufe("GET", "/tresor/passkey/optionen", map[string]string{proxyHeader: "s3cret", "Remote-User": "ben"}, nil); o.Code == 200 {
		t.Fatal("another person gets the options")
	}
}

// A passphrase left in the old outside store (before 2026-11-02) moves into
// the vault on the next opening: written inside, the outside file
// overwritten and removed, one log line without the secret; the next
// Signal delivery uses the inside one, and nothing outside the vault — the
// key folder, the status file, the record, the log, the page — holds it.
func TestTheSignalPassphraseMovesIntoTheVaultOnOpening(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	wt := neuesWeb(t)
	wt.tt.Einziehen = wt.w.Schluessel.SignalEinziehen
	wt.oeffnen(t)
	wt.rufe("POST", "/tresor/signal", anna, url.Values{"telefon": {annaTel}})
	wt.tt.Abschliessen(context.Background(), "test")

	alt := filepath.Join(wt.w.Schluessel.Dir, altSignal)
	if err := os.WriteFile(alt, []byte(passphrase+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	zweiterName := filepath.Join(wt.data, "zweiter-name") // a hard link sees what is written into the file's blocks
	if err := os.Link(alt, zweiterName); err != nil {
		t.Fatal(err)
	}
	vorher := buf.Len()
	if rec := wt.rufe("POST", "/tresor/oeffnen", anna, url.Values{"passwort": {geheim}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("öffnen: %d", rec.Code)
	}
	wt.tt.bereit(t)
	innen, _ := wt.tt.Innen()
	if _, err := os.Stat(alt); !os.IsNotExist(err) {
		t.Fatal("the passphrase is still outside the vault")
	}
	if b, _ := os.ReadFile(zweiterName); len(b) != len(passphrase)+1 || strings.Trim(string(b), "\x00") != "" {
		t.Fatalf("the outside file was removed without being overwritten: %q", b)
	}
	if b, _ := os.ReadFile(innen.SignalPassphrase()); string(b) != passphrase {
		t.Fatalf("not in the vault: %d bytes", len(b))
	}
	if fi, _ := os.Stat(innen.SignalPassphrase()); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("the passphrase in the vault is not 0600")
	}
	if n := strings.Count(buf.String()[vorher:], "Signal-Passphrase in den Tresor übernommen"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, buf.String())
	}

	// again: nothing outside, nothing to do, nothing logged
	wt.tt.Abschliessen(context.Background(), "test")
	vorher = buf.Len()
	wt.rufe("POST", "/tresor/oeffnen", anna, url.Values{"passwort": {geheim}})
	wt.tt.bereit(t)
	if b, _ := os.ReadFile(innen.SignalPassphrase()); string(b) != passphrase || strings.Contains(buf.String()[vorher:], "Signal-Passphrase") {
		t.Fatalf("the second opening: %d bytes inside, log:\n%s", len(b), buf.String()[vorher:])
	}

	// the next Signal delivery reads the inside one
	run := &fakeSignalImport{pb: wt.w.Postbote}
	wt.w.Postbote.Run = run
	wt.w.Eingang.Stabil("signal")
	legen(t, wt.inbox, "signal/signal-1.backup", 5, tag)
	wt.w.Eingang.Stabil("signal")
	wt.w.Postbote.Runde(context.Background(), innen)
	if run.anzahl() != 2 || string(run.befehle[0].Terminal) != passphrase+"\n" {
		t.Fatalf("the delivery did not use the passphrase from the vault: %d commands", run.anzahl())
	}
	if l, _ := wt.w.Eingang.Liste("signal"); len(l) != 0 {
		t.Fatalf("not imported: %v", l)
	}

	// nowhere outside the vault
	wt.w.Protokoll.Fehlschlag(Fehler{Am: time.Now(), Quelle: "signal", Datei: "signal-2.backup", Satz: saysSignalFalsch}) // the record written once more
	status := filepath.Join(wt.data, "laib-status.json")
	statusSchreiben(status, statusBauen(wt.w.lage(wt.tt.Stand())))
	for _, dir := range []string{wt.data, wt.inbox} {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(passphrase)) {
				t.Errorf("the passphrase outside the vault in %s", p)
			}
			return nil
		})
	}
	if _, err := os.Stat(status); err != nil {
		t.Fatal("no status file, so the walk proves nothing about it")
	}
	for _, pfad := range []string{"/tresor", "/tresor/stand"} {
		if body := wt.rufe("GET", pfad, anna, nil).Body.String(); strings.Contains(body, passphrase) {
			t.Fatalf("the passphrase on %s", pfad)
		}
	}
	if strings.Contains(buf.String(), passphrase) {
		t.Fatalf("the passphrase in the log:\n%s", buf.String())
	}
}

// A passphrase already in the vault is newer than one left outside (the
// page writes only inside): the outside one goes, the inside one stays.
func TestAnOldOutsidePassphraseDoesNotReplaceTheOneInTheVault(t *testing.T) {
	wt := neuesWeb(t)
	wt.tt.Einziehen = wt.w.Schluessel.SignalEinziehen
	wt.oeffnen(t)
	neu := "999999999999999999999999999999"
	if rec := wt.rufe("POST", "/tresor/signal", anna, url.Values{"passphrase": {neu}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("signal: %d", rec.Code)
	}
	wt.tt.Abschliessen(context.Background(), "test")
	alt := filepath.Join(wt.w.Schluessel.Dir, altSignal)
	os.WriteFile(alt, []byte(passphrase), 0o600)
	wt.rufe("POST", "/tresor/oeffnen", anna, url.Values{"passwort": {geheim}})
	wt.tt.bereit(t)
	innen, _ := wt.tt.Innen()
	if b, _ := os.ReadFile(innen.SignalPassphrase()); string(b) != neu {
		t.Fatal("the old outside passphrase replaced the newer one in the vault")
	}
	if _, err := os.Stat(alt); !os.IsNotExist(err) {
		t.Fatal("the old passphrase stayed outside")
	}
}

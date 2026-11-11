package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

// pakete opens every packet: its account and its Maildir entries with content.
func (mt *mhTest) pakete(t *testing.T) map[string]map[string][]byte {
	t.Helper()
	out := map[string]map[string][]byte{}
	for _, name := range Pakete(mt.h.Post) {
		f, _ := os.Open(filepath.Join(mt.h.Post, name))
		inhalt := map[string][]byte{}
		m, err := PaketLesen(f, mt.id, func(n string, r io.Reader) error {
			b, _ := io.ReadAll(r)
			inhalt[n] = b
			return nil
		}, func(Nachricht) error { return nil })
		f.Close()
		if err != nil || m.Quelle != "mail" {
			t.Fatalf("%s: %v %+v", name, err, m)
		}
		if out[m.Konto] == nil {
			out[m.Konto] = map[string][]byte{}
		}
		for n, b := range inhalt {
			out[m.Konto][n] = b
		}
	}
	return out
}

func zweiPostfaecher(t *testing.T, mt *mhTest) {
	t.Helper()
	k := []imapZugang{
		{ID: "a1", Name: "Privat", Host: "imap.privat.example", Benutzer: "anna@privat.example", Passwort: "p1"},
		{ID: "b2", Name: "Arbeit", Host: "imap.arbeit.example", Port: 143, Benutzer: "anna@arbeit.example", Passwort: "p2"},
	}
	if err := imapKontenSpeichern(mt.h.Schluessel, k); err != nil {
		t.Fatal(err)
	}
}

func fakeMit(texte ...string) *fakePostfach {
	var ms []Mail
	for i, x := range texte {
		ms = append(ms, Mail{UID: uint32(i + 1), Roh: mailText(x, x)})
	}
	return &fakePostfach{ordner: []PostOrdner{{Name: "INBOX", Trenner: "/"}}, mails: map[string][]Mail{"INBOX": ms}}
}

func TestSeveralMailboxesAreEachFetchedWithTheirOwnState(t *testing.T) {
	mt := neuerMailholer(t, "x", "y")
	zweiPostfaecher(t, mt)
	server := map[string]*fakePostfach{"imap.privat.example": fakeMit("privat1", "privat2"), "imap.arbeit.example": fakeMit("arbeit1")}
	var gefragt []string
	mt.h.Verbinden = func(_ context.Context, z imapZugang) (Postfach, error) {
		gefragt = append(gefragt, fmt.Sprintf("%s:%d %s %s", z.Host, z.Port, z.Benutzer, z.Passwort))
		if z.Host == "imap.arbeit.example" && z.Passwort == "p2" && len(gefragt) < 3 {
			return nil, ErrMailAnmeldung
		}
		return server[z.Host], nil
	}
	mt.h.Runde(context.Background())
	if fmt.Sprint(gefragt) != "[imap.privat.example:0 anna@privat.example p1 imap.arbeit.example:143 anna@arbeit.example p2]" {
		t.Fatalf("%v", gefragt)
	}
	p := mt.pakete(t)
	if len(p) != 1 || len(p["anna@privat.example"]) != 2 {
		t.Fatalf("a refused login stopped the other mailbox: %v", p)
	}
	if _, f, _ := mt.h.Protokoll.Kopie(); f == nil || f.Satz != saysMailAnmeldung || f.Datei != "anna@arbeit.example" {
		t.Fatalf("%+v", f)
	}
	// the next round: the refused one waits for a new password, the other's success does not clear its error
	mt.h.Now = func() time.Time { return tag.Add(time.Hour) }
	mt.h.Runde(context.Background())
	if len(gefragt) != 3 {
		t.Fatalf("tried the refused password again: %v", gefragt)
	}
	if _, f, _ := mt.h.Protokoll.Kopie(); f == nil || f.Datei != "anna@arbeit.example" {
		t.Fatalf("another mailbox's round cleared the error: %+v", f)
	}
	mt.h.NeuerSchluessel()
	mt.h.Now = func() time.Time { return tag.Add(2 * time.Hour) }
	mt.h.Runde(context.Background())
	p = mt.pakete(t)
	if len(p["anna@arbeit.example"]) != 1 || len(p["anna@privat.example"]) != 2 {
		t.Fatalf("%v", p)
	}
	if _, f, _ := mt.h.Protokoll.Kopie(); f != nil {
		t.Fatalf("%+v", f)
	}
	for _, d := range []string{"mail-stand-a1.json", "mail-stand-b2.json"} {
		if _, err := os.Stat(filepath.Join(mt.data, "gesehen", d)); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(mt.data, "gesehen", "mail-stand.json")); err == nil {
		t.Fatal("a new mailbox wrote the first mailbox's state")
	}
	// one more mail in each: only that one comes, per mailbox
	server["imap.privat.example"].mails["INBOX"] = append(server["imap.privat.example"].mails["INBOX"], Mail{UID: 3, Roh: mailText("privat3", "privat3")})
	n := len(Pakete(mt.h.Post))
	mt.h.Now = func() time.Time { return tag.Add(3 * time.Hour) }
	mt.h.Runde(context.Background())
	if len(Pakete(mt.h.Post)) != n+1 || len(mt.pakete(t)["anna@privat.example"]) != 3 {
		t.Fatalf("%v", Pakete(mt.h.Post))
	}
	// removed: its state goes, what it brought stays
	mt.h.KontoWeg("b2")
	if _, err := os.Stat(filepath.Join(mt.data, "gesehen", "mail-stand-b2.json")); err == nil {
		t.Fatal("the removed mailbox's state stayed")
	}
}

// The one mailbox stored before 2026-11-11 goes on as it was: the same
// fetch state (nothing fetched twice), the same file until a change.
func TestASingleStoredMailboxMigratesUnchanged(t *testing.T) {
	mt := neuerMailholer(t, "imap.example.org", "geheim") // imapJSON: the old single object
	pf := fakeMit("eins", "zwei")
	mt.h.Verbinden = func(_ context.Context, z imapZugang) (Postfach, error) {
		if z.ID != kontoErstes || z.Passwort != "geheim" || z.Benutzer != "username" {
			t.Fatalf("%+v", z)
		}
		return pf, nil
	}
	mt.h.Runde(context.Background())
	if len(Pakete(mt.h.Post)) != 1 {
		t.Fatal("no packet")
	}
	stand, err := os.ReadFile(filepath.Join(mt.data, "gesehen", "mail-stand.json"))
	if err != nil || len(stand) < 10 {
		t.Fatalf("the first mailbox's state is not where it was: %v", err)
	}
	roh, _ := mt.h.Schluessel.Lesen(SchluesselIMAP)
	if !bytes.HasPrefix(roh, []byte("{")) {
		t.Fatal("a round rewrote the stored mailbox")
	}
	mt.h.Now = func() time.Time { return tag.Add(time.Hour) }
	mt.h.Runde(context.Background())
	if len(Pakete(mt.h.Post)) != 1 {
		t.Fatal("the migrated mailbox was fetched from the start")
	}
}

func TestTheSourcesPageListsAddsChangesAndRemovesMailboxes(t *testing.T) {
	wt := neuesWeb(t)
	wt.oeffnen(t)
	sch := wt.w.Schluessel
	alt, _ := imapJSON("imap.gmail.com", "anna@gmail.com", "altes-pw")
	sch.Speichern(SchluesselIMAP, alt) // as stored before several were possible
	mh := &Mailholer{Gesehen: &Gesehen{Dir: filepath.Join(wt.data, "gesehen")}, Protokoll: wt.w.Protokoll}
	wt.w.Mailholer = mh
	os.MkdirAll(mh.Gesehen.Dir, 0o700)
	os.WriteFile(mh.standDatei(kontoErstes), []byte(`{"x":{"validity":1,"uid":9}}`), 0o600)

	body := wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String()
	for _, want := range []string{"Postfach <b>anna@gmail.com</b> auf imap.gmail.com", "Weiteres Postfach hinzufügen", `name=konto value="erstes"`, "Alte Mails beim Anbieter löschen"} {
		if !strings.Contains(body, want) {
			t.Fatalf("lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "altes-pw") {
		t.Fatal("the password on the page")
	}
	// a name for the old one, the password kept: the fetch state stays
	if rec := wt.rufe("POST", "/tresor/imap", anna, url.Values{"konto": {kontoErstes}, "name": {"Gmail"}, "host": {"imap.gmail.com"}, "benutzer": {"anna@gmail.com"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	k, _ := imapKonten(sch)
	if len(k) != 1 || k[0].ID != kontoErstes || k[0].Name != "Gmail" || k[0].Passwort != "altes-pw" {
		t.Fatalf("%+v", ohnePasswort(k))
	}
	mh.Runde(context.Background()) // no briefkasten.pub: returns at once, but would have dropped the state on a change
	if _, err := os.Stat(mh.standDatei(kontoErstes)); err != nil {
		t.Fatal("renaming the mailbox dropped its fetch state")
	}
	// a second one; the same again is refused; without a password refused
	neu := url.Values{"name": {"Arbeit"}, "host": {"imap.firma.example"}, "port": {"143"}, "benutzer": {"anna@firma.example"}, "passwort": {"pw2"}}
	if rec := wt.rufe("POST", "/tresor/imap", anna, neu); rec.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := wt.rufe("POST", "/tresor/imap", anna, neu); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), saysIMAPDoppelt) {
		t.Fatalf("%d", rec.Code)
	}
	if rec := wt.rufe("POST", "/tresor/imap", anna, url.Values{"host": {"h"}, "benutzer": {"b@c"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
	if rec := wt.rufe("POST", "/tresor/imap", anna, url.Values{"host": {"h"}, "benutzer": {"b@c"}, "passwort": {"x"}, "port": {"99999"}}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Port") {
		t.Fatalf("%d", rec.Code)
	}
	k, _ = imapKonten(sch)
	if len(k) != 2 || k[1].Port != 143 || k[1].Name != "Arbeit" || !kontoIDMuster.MatchString(k[1].ID) || k[1].Am.IsZero() {
		t.Fatalf("%+v", ohnePasswort(k))
	}
	roh, _ := sch.Lesen(SchluesselIMAP)
	var liste []map[string]any
	if json.Unmarshal(roh, &liste) != nil || len(liste) != 2 {
		t.Fatalf("not written as a list: %s", roh)
	}
	body = wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String()
	if !strings.Contains(body, "<b>Gmail</b>: Postfach <b>anna@gmail.com</b>") || !strings.Contains(body, "<b>Arbeit</b>: Postfach <b>anna@firma.example</b> auf imap.firma.example:143") || strings.Contains(body, "pw2") {
		t.Fatalf("%s", body)
	}
	// another server for the first one wants its password, then starts its state over
	if rec := wt.rufe("POST", "/tresor/imap", anna, url.Values{"konto": {kontoErstes}, "host": {"imap.gmx.net"}, "benutzer": {"anna@gmx.de"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
	if rec := wt.rufe("POST", "/tresor/imap", anna, url.Values{"konto": {kontoErstes}, "host": {"imap.gmx.net"}, "benutzer": {"anna@gmx.de"}, "passwort": {"gmx"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("%d", rec.Code)
	}
	if !mh.wechsel[kontoErstes] {
		t.Fatal("another mailbox under the same entry keeps the old fetch state")
	}
	// removed
	if rec := wt.rufe("POST", "/tresor/imap/weg", anna, url.Values{"konto": {k[1].ID}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	k, _ = imapKonten(sch)
	if len(k) != 1 || k[0].Benutzer != "anna@gmx.de" {
		t.Fatalf("%+v", ohnePasswort(k))
	}
	// closed: no change
	wt.tt.Abschliessen(context.Background(), "test")
	if rec := wt.rufe("POST", "/tresor/imap/weg", anna, url.Values{"konto": {kontoErstes}}); rec.Code != http.StatusLocked {
		t.Fatalf("%d", rec.Code)
	}
}

func dokuPostfach(t *testing.T) *Dokumentenpostfach {
	t.Helper()
	pw := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(pw, []byte("haushalt-pw\n"), 0o600)
	d := DokumentenpostfachAus("Anna", func(k string) string {
		return map[string]string{"DOKUMENTE_MAIL_HOST": "imap.strato.de", "DOKUMENTE_MAIL_USER": "webmaster@example.org",
			"DOKUMENTE_MAIL_DOMAIN": "Example.org", "DOKUMENTE_MAIL_PASSWORD_FILE": pw}[k]
	})
	if d == nil {
		t.Fatal("no document mailbox")
	}
	return d
}

func docMail(id, kopf string) []byte {
	return []byte(kopf + "\r\nFrom: shop@example.com\r\nSubject: Rechnung " + id + "\r\nMessage-ID: <" + id + "@example.com>\r\n\r\nim Anhang\r\n")
}

func TestTheDocumentMailboxGivesOnlyThePersonsMail(t *testing.T) {
	d := dokuPostfach(t)
	if d.Adresse() != "anna+doc@example.org" {
		t.Fatal(d.Adresse())
	}
	for kopf, want := range map[string]bool{
		"To: anna+doc@example.org":                                        true,
		"To: Anna <ANNA+DOC@Example.ORG>":                                 true,
		"To: anna+doc+mueller-sanitaer@example.org":                       true,
		"To: dokumente@example.org\r\nCc: anna+doc@example.org":           true,
		"To: webmaster@example.org\r\nDelivered-To: anna+doc@example.org": true,
		"To: hanna+doc@example.org":                                       false, // the server's substring takes it
		"To: anna@example.org":                                            false, // the person's own mail, never through here
		"To: anna+docs@example.org":                                       false,
		"To: anna+doc@example.org.evil.com":                               false,
		"To: anna+doc@other.org":                                          false,
		"To: dokumente@example.org\r\nX-Note: anna+doc@example.org":       false,
	} {
		if got := d.Passt(docMail("x", kopf)); got != want {
			t.Errorf("%q: %v", kopf, got)
		}
	}
	if (&Dokumentenpostfach{}).Passt([]byte("kaputt")) {
		t.Fatal("a broken mail passed")
	}
	if DokumentenpostfachAus("anna", func(string) string { return "" }) != nil {
		t.Fatal("a document mailbox without a host")
	}

	mt := neuerMailholer(t, "x", "y")
	os.Remove(filepath.Join(mt.data, "schluessel", SchluesselIMAP)) // the person has no mailbox of their own: the document mailbox alone
	mt.h.Dokumente = d
	inbox := []Mail{
		{UID: 1, Flags: []string{imap.FlaggedFlag, imap.SeenFlag}, Roh: docMail("eins", "To: anna+doc@example.org")},
		{UID: 2, Roh: docMail("zwei", "To: hanna+doc@example.org")},
		{UID: 3, Roh: docMail("drei", "To: anna+doc+arzt@example.org")},
		{UID: 4, Roh: docMail("vier", "To: dokumente@example.org")},
		{UID: 5, Roh: docMail("fuenf", "To: anna@example.org")},
	}
	pf := &fakePostfach{ordner: []PostOrdner{{Name: "INBOX"}, {Name: "Privat"}}, mails: map[string][]Mail{"INBOX": inbox,
		"Privat": {{UID: 1, Roh: docMail("privat", "To: anna+doc@example.org")}}}}
	var zugang imapZugang
	mt.h.Verbinden = func(_ context.Context, z imapZugang) (Postfach, error) { zugang = z; return pf, nil }
	mt.h.Runde(context.Background())
	if zugang.Host != "imap.strato.de" || zugang.Benutzer != "webmaster@example.org" || zugang.Passwort != "haushalt-pw" {
		t.Fatalf("%+v", zugang)
	}
	p := mt.pakete(t)
	doku := p["anna+doc@example.org"]
	if len(p) != 1 || len(doku) != 2 {
		t.Fatalf("%v", p)
	}
	var namen []string
	for n, b := range doku {
		namen = append(namen, n)
		if !bytes.Contains(b, []byte("Rechnung eins")) && !bytes.Contains(b, []byte("Rechnung drei")) {
			t.Fatalf("another's mail: %s", b)
		}
		if !strings.HasPrefix(n, "maildir/Dokumente/cur/") || !strings.HasSuffix(n, ":2,") {
			t.Fatalf("label or Paperless' flags: %s", n)
		}
	}
	sort.Strings(namen)
	// the next round: nothing again
	mt.h.Now = func() time.Time { return tag.Add(time.Hour) }
	mt.h.Runde(context.Background())
	if len(Pakete(mt.h.Post)) != 1 {
		t.Fatal("fetched twice")
	}
	if _, err := os.Stat(filepath.Join(mt.data, "gesehen", "mail-stand-dokumente.json")); err != nil {
		t.Fatal(err)
	}
	// the box took the password back: nothing is read
	os.Remove(d.PasswortDatei)
	zugang = imapZugang{}
	mt.h.Runde(context.Background())
	if zugang.Host != "" {
		t.Fatal("read without a password from the box")
	}
}

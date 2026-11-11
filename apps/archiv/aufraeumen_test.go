package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
)

var heute = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

// fakeLoesch is a writable server: it records SELECT and every delete.
// Its SEARCH is sloppy on purpose (it ignores flags), so the archive's own
// checks are what keeps a starred mail or a draft.
type fakeLoesch struct {
	*fakePostfach
	schreibend []string
	geloescht  map[string][]uint32
	korb       []string
}

func (f *fakeLoesch) Schreibend(n string) (uint32, error) {
	f.schreibend = append(f.schreibend, n)
	return f.Waehlen(n)
}

func (f *fakeLoesch) Alte(vor time.Time) ([]uint32, error) {
	var out []uint32
	for _, m := range f.mails[f.aktuell] {
		if m.Datum.Before(vor) {
			out = append(out, m.UID)
		}
	}
	return out, nil
}

func (f *fakeLoesch) Koepfe(uids []uint32, fn func(Kopf) error) error {
	for _, u := range uids {
		for _, m := range f.mails[f.aktuell] {
			if m.UID == u {
				if err := fn(Kopf{UID: u, Flags: m.Flags, Datum: m.Datum, MessageID: kopfID(m.Roh)}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (f *fakeLoesch) Loeschen(uids []uint32, korb string) error {
	if f.geloescht == nil {
		f.geloescht = map[string][]uint32{}
	}
	f.geloescht[f.aktuell] = append(f.geloescht[f.aktuell], uids...)
	f.korb = append(f.korb, korb)
	weg := map[uint32]bool{}
	for _, u := range uids {
		weg[u] = true
	}
	var rest []Mail
	for _, m := range f.mails[f.aktuell] {
		if !weg[m.UID] {
			rest = append(rest, m)
		}
	}
	f.mails[f.aktuell] = rest
	return nil
}

// fakeBeleg is msgvault: Message-ID → content ids.
type fakeBeleg struct {
	hat    map[string][]string
	fehler error
	fragen int
}

func (b *fakeBeleg) MailsImArchiv(_ context.Context, ids []string) (map[string][]string, error) {
	b.fragen++
	if b.fehler != nil {
		return nil, b.fehler
	}
	out := map[string][]string{}
	for _, id := range ids {
		if s, ok := b.hat[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func inhaltID(roh []byte) string {
	s := sha256.Sum256(roh)
	return "maildir-" + hex.EncodeToString(s[:])
}

func alteMail(uid uint32, id string, alter time.Duration, flags ...string) Mail {
	roh := mailText(id, "Mail "+id)
	if id == "" {
		roh = []byte("From: a@example.org\r\nSubject: ohne ID\r\n\r\nx\r\n")
	}
	return Mail{UID: uid, Flags: flags, Datum: heute.Add(-alter), Roh: roh}
}

const jahr = 365 * 24 * time.Hour

type aufrTest struct {
	a     *Aufraeumer
	innen Innen
	pf    *fakeLoesch
	beleg *fakeBeleg
	z     imapZugang
}

func neuerAufraeumer(t *testing.T, ordner []PostOrdner, mails map[string][]Mail) *aufrTest {
	t.Helper()
	sch := Schluessel{Dir: filepath.Join(t.TempDir(), "schluessel")}
	z := imapZugang{ID: "k1", Host: "imap.example.org", Benutzer: "anna@example.org", Passwort: "pw"}
	imapKontenSpeichern(sch, []imapZugang{z})
	at := &aufrTest{innen: Innen{Wurzel: t.TempDir()}, pf: &fakeLoesch{fakePostfach: &fakePostfach{ordner: ordner, mails: mails}}, beleg: &fakeBeleg{hat: map[string][]string{}}, z: z}
	at.a = &Aufraeumer{Schluessel: sch, Beleg: at.beleg, Now: func() time.Time { return heute }, Buendel: 2,
		Verbinden: func(context.Context, imapZugang) (Postfach, error) { return at.pf, nil }}
	// what msgvault holds: every mail with a Message-ID, but „nicht-importiert“
	for _, ms := range mails {
		for _, m := range ms {
			if id := kopfID(m.Roh); id != "" && id != "nicht-importiert@example.org" {
				at.beleg.hat[id] = append(at.beleg.hat[id], inhaltID(m.Roh))
			}
		}
	}
	return at
}

func (at *aufrTest) stand(t *testing.T) Aufraeumen {
	t.Helper()
	return at.a.Einstellungen(at.innen)["k1"]
}

func uids(u []uint32) []int {
	var out []int
	for _, x := range u {
		out = append(out, int(x))
	}
	sort.Ints(out)
	return out
}

func TestOldArchivedMailIsDeletedOnlyAfterTheDryRunAndTheTap(t *testing.T) {
	inbox := []Mail{
		alteMail(1, "alt1", 3*jahr),                       // old, in the archive: goes
		alteMail(2, "alt2", 3*jahr, imap.SeenFlag),        // old, read, in the archive: goes
		alteMail(3, "stern", 3*jahr, imap.FlaggedFlag),    // starred: stays
		alteMail(4, "entwurf", 3*jahr, imap.DraftFlag),    // a draft: stays
		alteMail(5, "nicht-importiert", 3*jahr),           // not in msgvault yet: stays
		alteMail(6, "", 3*jahr),                           // no Message-ID: stays
		alteMail(7, "neu", 30*24*time.Hour),               // younger than two years: stays
		alteMail(8, "anders", 3*jahr),                     // msgvault has this Message-ID with other content: stays
		alteMail(9, "markiert", 3*jahr, imap.DeletedFlag), // marked deleted by someone else: stays
	}
	at := newAufrMitAnders(t, inbox)

	// off by default: nothing happens
	at.a.Runde(context.Background(), at.innen)
	if at.beleg.fragen != 0 || len(at.pf.schreibend) != 0 {
		t.Fatal("ran while off")
	}
	if err := at.a.Freigeben(at.innen, "k1"); !errors.Is(err, errNichtFrei) {
		t.Fatalf("a tap without a dry run: %v", err)
	}
	if err := at.a.Einstellen(at.innen, "k1", true, 24); err != nil {
		t.Fatal(err)
	}
	at.a.Runde(context.Background(), at.innen)
	e := at.stand(t) // old and not starred, a draft or marked: 1, 2, 5, 6, 8; proven: 1, 2
	if e.Probe == nil || e.Probe.Alt != 5 || e.Probe.ImArchiv != 2 || e.Probe.Geloescht != 0 || e.Probe.Fehler != "" || e.Freigabe != nil {
		t.Fatalf("dry run: %+v", e.Probe)
	}
	if len(at.pf.schreibend) != 0 || len(at.pf.geloescht) != 0 {
		t.Fatal("the dry run selected writable or deleted")
	}
	// the dry run is not repeated within a day; no deletion without the tap
	at.a.Runde(context.Background(), at.innen)
	if len(at.pf.geloescht) != 0 {
		t.Fatal("deleted without the tap")
	}

	if err := at.a.Freigeben(at.innen, "k1"); err != nil {
		t.Fatal(err)
	}
	at.a.Runde(context.Background(), at.innen)
	if got := uids(at.pf.geloescht["INBOX"]); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("deleted %v, want [1 2]", got)
	}
	if at.pf.korb[0] != "" || len(at.pf.schreibend) != 1 {
		t.Fatalf("not SELECT + \\Deleted + EXPUNGE: %q %v", at.pf.korb, at.pf.schreibend)
	}
	e = at.stand(t)
	if e.Letzter == nil || e.Letzter.Geloescht != 2 || e.Letzter.Fehler != "" {
		t.Fatalf("%+v", e.Letzter)
	}
	// monthly: not again within the month, again after it
	at.pf.mails["INBOX"] = append(at.pf.mails["INBOX"], alteMail(10, "spaeter", 3*jahr))
	at.beleg.hat["spaeter@example.org"] = []string{inhaltID(alteMail(10, "spaeter", 3*jahr).Roh)}
	at.a.Now = func() time.Time { return heute.Add(20 * 24 * time.Hour) }
	at.a.Runde(context.Background(), at.innen)
	if len(at.pf.geloescht["INBOX"]) != 2 {
		t.Fatal("ran again within the month")
	}
	at.a.Now = func() time.Time { return heute.Add(31 * 24 * time.Hour) }
	at.a.Runde(context.Background(), at.innen)
	if got := uids(at.pf.geloescht["INBOX"]); len(got) != 3 || got[2] != 10 {
		t.Fatalf("the monthly run: %v", got)
	}

	// another limit asks again: the tap is gone
	if err := at.a.Einstellen(at.innen, "k1", true, 36); err != nil {
		t.Fatal(err)
	}
	if e := at.stand(t); e.Freigabe != nil || e.Probe != nil {
		t.Fatalf("a new limit kept the tap: %+v", e)
	}
	if err := at.a.Einstellen(at.innen, "k1", false, 36); err != nil || at.stand(t).An {
		t.Fatal("not switched off")
	}
}

// newAufrMitAnders: msgvault holds „anders“ with other content than the server's.
func newAufrMitAnders(t *testing.T, inbox []Mail) *aufrTest {
	at := neuerAufraeumer(t, []PostOrdner{{Name: "INBOX"}, {Name: "Entwürfe", Attribute: []string{imap.DraftsAttr}}, {Name: "Papierkorb", Attribute: []string{imap.TrashAttr}}},
		map[string][]Mail{"INBOX": inbox, "Entwürfe": {alteMail(1, "im-entwurfsordner", 3*jahr)}, "Papierkorb": {alteMail(1, "im-papierkorb", 3*jahr)}})
	at.beleg.hat["anders@example.org"] = []string{"maildir-" + strings.Repeat("0", 64)}
	return at
}

func TestGmailMovesToItsTrash(t *testing.T) {
	alle := "[Gmail]/Alle Nachrichten"
	at := neuerAufraeumer(t, []PostOrdner{{Name: "INBOX"}, {Name: alle, Attribute: []string{imap.AllAttr}}, {Name: "[Gmail]/Papierkorb", Attribute: []string{imap.TrashAttr}},
		{Name: "[Gmail]/Entwürfe", Attribute: []string{imap.DraftsAttr}}},
		map[string][]Mail{alle: {alteMail(1, "g1", 3*jahr), alteMail(2, "g2", 3*jahr, imap.FlaggedFlag)}, "INBOX": {alteMail(1, "g1", 3*jahr)}})
	at.a.Einstellen(at.innen, "k1", true, 24)
	at.a.Runde(context.Background(), at.innen)
	at.a.Freigeben(at.innen, "k1")
	at.a.Runde(context.Background(), at.innen)
	if len(at.pf.geloescht) != 1 || uids(at.pf.geloescht[alle])[0] != 1 || len(at.pf.geloescht[alle]) != 1 {
		t.Fatalf("%v", at.pf.geloescht)
	}
	if at.pf.korb[0] != "[Gmail]/Papierkorb" {
		t.Fatalf("not moved to Gmail's trash: %q", at.pf.korb)
	}
	// Gmail without a trash folder it names: nothing
	at2 := neuerAufraeumer(t, []PostOrdner{{Name: alle, Attribute: []string{imap.AllAttr}}}, map[string][]Mail{alle: {alteMail(1, "g1", 3*jahr)}})
	at2.a.Einstellen(at2.innen, "k1", true, 24)
	at2.a.Runde(context.Background(), at2.innen)
	if p := at2.stand(t).Probe; p == nil || p.Fehler != saysAufrKorb {
		t.Fatalf("%+v", p)
	}
}

func TestNothingIsDeletedWhenTheArchiveCannotProveIt(t *testing.T) {
	inbox := []Mail{alteMail(1, "alt1", 3*jahr), alteMail(2, "alt2", 3*jahr)}
	at := neuerAufraeumer(t, []PostOrdner{{Name: "INBOX"}}, map[string][]Mail{"INBOX": inbox})
	at.a.Einstellen(at.innen, "k1", true, 24)
	at.a.Runde(context.Background(), at.innen)
	if err := at.a.Freigeben(at.innen, "k1"); err != nil {
		t.Fatal(err)
	}
	// msgvault's query fails
	at.beleg.fehler = errors.New("query_error")
	at.a.Runde(context.Background(), at.innen)
	if len(at.pf.geloescht) != 0 {
		t.Fatal("deleted without the proof")
	}
	if l := at.stand(t).Letzter; l == nil || l.Fehler != saysAufrBeleg || l.Geloescht != 0 {
		t.Fatalf("%+v", l)
	}
	// the vault closed: the real msgvault client has no key and answers errNichtBereit
	at.a.Beleg = &MsgvaultAPI{Key: func() string { return "" }}
	at.a.Runde(context.Background(), at.innen)
	if len(at.pf.geloescht) != 0 {
		t.Fatal("deleted with the vault closed")
	}
	if l := at.stand(t).Letzter; l == nil || l.Fehler != saysAufrZu {
		t.Fatalf("%+v", l)
	}
	// a msgvault still building its index (202 without rows) proves nothing either
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusAccepted)
		rw.Write([]byte(`{"job_id":"x"}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	at.a.Beleg = &MsgvaultAPI{Port: port, Key: func() string { return "k" }}
	at.a.Runde(context.Background(), at.innen)
	if len(at.pf.geloescht) != 0 {
		t.Fatal("deleted while msgvault builds")
	}
	// the server refuses the login: nothing, and it says so
	at.a.Beleg = at.beleg
	at.beleg.fehler = nil
	at.a.Verbinden = func(context.Context, imapZugang) (Postfach, error) { return nil, ErrMailAnmeldung }
	at.a.Runde(context.Background(), at.innen)
	if l := at.stand(t).Letzter; l == nil || l.Fehler != saysAufrAnmeldung {
		t.Fatalf("%+v", l)
	}
}

func TestMsgvaultIsAskedByMessageIDForUndeletedMessages(t *testing.T) {
	var sql string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var q map[string]string
		json.NewDecoder(r.Body).Decode(&q)
		sql = q["sql"]
		if r.URL.Path != "/api/v1/query" || r.Header.Get("X-API-Key") != "k" {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		rw.Write([]byte(`{"columns":["mid","sid"],"rows":[["a@x","maildir-1"],["a@x","maildir-2"],["b@x","maildir-3"]]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	m := &MsgvaultAPI{Port: port, Key: func() string { return "k" }}
	got, err := m.MailsImArchiv(context.Background(), []string{"a@x", "b@x"})
	if err != nil || len(got["a@x"]) != 2 || got["b@x"][0] != "maildir-3" {
		t.Fatalf("%v %v", got, err)
	}
	if !strings.Contains(sql, "deleted_at IS NULL") || !strings.Contains(sql, "rfc822_message_id IN ('a@x', 'b@x')") {
		t.Fatal(sql)
	}
	if _, err := m.MailsImArchiv(context.Background(), []string{"x'); DROP"}); err == nil {
		t.Fatal("a Message-ID that is not one went into the query")
	}
	for wert, want := range map[string]string{"<a@b>": "a@b", " <a@b> ": "a@b", "a@b": "a@b", "<a b@c>": "", "<a'@b>": "", "": "", "<>": ""} {
		if got := messageID(wert); got != want {
			t.Errorf("%q: %q", wert, got)
		}
	}
}

// The real protocol against go-imap's in-memory server (no UIDPLUS, so
// the plain EXPUNGE, only when the marked messages are exactly ours).
func TestDeletingOverIMAPTakesExactlyTheProvenMessages(t *testing.T) {
	srv := neuerIMAPServer(t)
	mb, _ := srv.user.GetMailbox("INBOX")
	alt := heute.Add(-3 * jahr)
	mb.CreateMessage(nil, alt, bytes.NewReader(mailText("weg", "Weg")))
	mb.CreateMessage([]string{imap.FlaggedFlag}, alt, bytes.NewReader(mailText("stern", "Stern")))
	mb.CreateMessage(nil, heute, bytes.NewReader(mailText("neu", "Neu")))
	sch := Schluessel{Dir: filepath.Join(t.TempDir(), "schluessel")}
	imapKontenSpeichern(sch, []imapZugang{{ID: "k1", Host: srv.adresse, Benutzer: "username", Passwort: "password"}})
	beleg := &fakeBeleg{hat: map[string][]string{
		"weg@example.org":   {inhaltID(mailText("weg", "Weg"))},
		"stern@example.org": {inhaltID(mailText("stern", "Stern"))},
		"neu@example.org":   {inhaltID(mailText("neu", "Neu"))},
	}}
	innen := Innen{Wurzel: t.TempDir()}
	a := &Aufraeumer{Schluessel: sch, Beleg: beleg, Now: func() time.Time { return heute }}
	a.Einstellen(innen, "k1", true, 24)
	a.Runde(context.Background(), innen)
	if p := a.Einstellungen(innen)["k1"].Probe; p == nil || p.Alt != 1 || p.ImArchiv != 1 || p.Fehler != "" {
		t.Fatalf("%+v", p)
	}
	a.Freigeben(innen, "k1")
	a.Runde(context.Background(), innen)
	if l := a.Einstellungen(innen)["k1"].Letzter; l == nil || l.Geloescht != 1 || l.Fehler != "" {
		t.Fatalf("%+v", l)
	}
	msgs := liste(t, mb, imap.FetchEnvelope)
	var rest []string
	for _, m := range msgs {
		rest = append(rest, m.Envelope.Subject)
	}
	sort.Strings(rest)
	if !strings.Contains(strings.Join(rest, ","), "Neu,Stern") {
		t.Fatalf("left at the provider: %v", rest)
	}
	for _, s := range rest {
		if s == "Weg" {
			t.Fatal("the proven message is still there")
		}
	}

	// someone else marked a message deleted: without UIDPLUS nothing goes, and our marks come off
	mb.CreateMessage([]string{imap.DeletedFlag}, alt, bytes.NewReader(mailText("fremd", "Fremd")))
	mb.CreateMessage(nil, alt, bytes.NewReader(mailText("weg2", "Weg2")))
	beleg.hat["weg2@example.org"] = []string{inhaltID(mailText("weg2", "Weg2"))}
	a.Now = func() time.Time { return heute.Add(31 * 24 * time.Hour) }
	a.Runde(context.Background(), innen)
	if l := a.Einstellungen(innen)["k1"].Letzter; l == nil || l.Fehler != saysAufrFremd || l.Geloescht != 0 {
		t.Fatalf("%+v", l)
	}
	msgs = liste(t, mb, imap.FetchEnvelope, imap.FetchFlags)
	for _, m := range msgs {
		if m.Envelope.Subject == "Weg2" {
			for _, f := range m.Flags {
				if f == imap.DeletedFlag {
					t.Fatal("our mark stayed")
				}
			}
		}
	}
	if len(msgs) != 5 { // memory.New()'s own message, Neu, Stern, Fremd, Weg2
		t.Fatalf("%d messages left", len(msgs))
	}
}

// liste reads what the in-memory server still holds.
func liste(t *testing.T, mb backend.Mailbox, was ...imap.FetchItem) []*imap.Message {
	t.Helper()
	s := new(imap.SeqSet)
	s.AddRange(1, 0)
	ch := make(chan *imap.Message, 64)
	if err := mb.ListMessages(true, s, was, ch); err != nil {
		t.Fatal(err)
	}
	var out []*imap.Message
	for m := range ch {
		out = append(out, m)
	}
	return out
}

func TestTheSourcesPageAsksBeforeDeletingAndSaysItCannotBeUndone(t *testing.T) {
	wt := neuesWeb(t)
	wt.oeffnen(t)
	imapKontenSpeichern(wt.w.Schluessel, []imapZugang{{ID: "k1", Name: "Privat", Host: "imap.gmail.com", Benutzer: "anna@gmail.com", Passwort: "pw"}})
	body := wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String()
	if !strings.Contains(body, "Alte Mails beim Anbieter löschen</summary>") || !strings.Contains(body, "Einschalten und zählen") || !strings.Contains(body, saysAufrNie) {
		t.Fatalf("%s", body)
	}
	if rec := wt.rufe("POST", "/tresor/mail/aufraeumen", anna, url.Values{"konto": {"k1"}, "an": {"1"}, "dauer": {"0"}, "einheit": {"jahre"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
	if rec := wt.rufe("POST", "/tresor/mail/aufraeumen", anna, url.Values{"konto": {"k1"}, "an": {"1"}, "dauer": {"2"}, "einheit": {"jahre"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	innen, _ := wt.tt.Innen()
	if e := wt.w.Aufraeumer.Einstellungen(innen)["k1"]; !e.An || e.Monate != 24 {
		t.Fatalf("%+v", e)
	}
	if _, err := os.Stat(innen.MailAufraeumen()); err != nil {
		t.Fatal("the settings are not in the vault")
	}
	body = wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String()
	if !strings.Contains(body, "zählt gleich, welche Mails älter als 2 Jahre sind") {
		t.Fatalf("%s", body)
	}
	if rec := wt.rufe("POST", "/tresor/mail/loeschen", anna, url.Values{"konto": {"k1"}}); rec.Code != http.StatusConflict {
		t.Fatalf("a tap before the count: %d", rec.Code)
	}
	// the count is there
	s, _ := aufrLaden(innen)
	s["k1"].Probe = &Durchlauf{Am: heute, Monate: 24, Alt: 1234, ImArchiv: 1234}
	aufrSpeichern(innen, s)
	body = wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String()
	for _, want := range []string{"1.234 Mails älter als 2 Jahre, alle im Archiv – jetzt beim Anbieter löschen?", "Jetzt 1.234 Mails beim Anbieter löschen", "nicht rückgängig"} {
		if !strings.Contains(body, want) {
			t.Fatalf("lacks %q:\n%s", want, body)
		}
	}
	s["k1"].Probe = &Durchlauf{Am: heute, Monate: 24, Alt: 1234, ImArchiv: 1200}
	aufrSpeichern(innen, s)
	if body = wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String(); !strings.Contains(body, "1.200 davon sind sicher im Archiv und würden gelöscht; 34 noch nicht") {
		t.Fatalf("%s", body)
	}
	if rec := wt.rufe("POST", "/tresor/mail/loeschen", anna, url.Values{"konto": {"k1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if e := wt.w.Aufraeumer.Einstellungen(innen)["k1"]; e.Freigabe == nil {
		t.Fatal("the tap was not kept")
	}
	if body = wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String(); !strings.Contains(body, "Das erste Löschen beginnt gleich") {
		t.Fatalf("%s", body)
	}
	// removing the mailbox forgets what was allowed for it
	wt.rufe("POST", "/tresor/imap/weg", anna, url.Values{"konto": {"k1"}})
	if _, da := wt.w.Aufraeumer.Einstellungen(innen)["k1"]; da {
		t.Fatal("the settings outlived the mailbox")
	}
	// closed: nothing about it can be changed
	wt.tt.Abschliessen(context.Background(), "test")
	if rec := wt.rufe("POST", "/tresor/mail/loeschen", anna, url.Values{"konto": {"k1"}}); rec.Code != http.StatusLocked {
		t.Fatalf("%d", rec.Code)
	}
}

func TestAPaperlessLinkOpensTheMailOrSaysWhy(t *testing.T) {
	wt := neuesWeb(t)
	rec := wt.rufe("GET", "/tresor/mail?id=%3Cabc%40example.org%3E", anna, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/tresor?m=mail-zu" {
		t.Fatalf("closed: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if body := wt.rufe("GET", "/tresor?m=mail-zu", anna, nil).Body.String(); !strings.Contains(body, "Link aus Paperless") {
		t.Fatal(body)
	}
}

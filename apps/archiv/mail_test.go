package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
)

func mailText(id, betreff string) []byte {
	return []byte("From: ben@example.org\r\nTo: anna@example.org\r\nSubject: " + betreff + "\r\nDate: Wed, 11 May 2016 14:31:59 +0000\r\nMessage-ID: <" + id + "@example.org>\r\nContent-Type: text/plain\r\n\r\n" + betreff + " – Inhalt\r\n")
}

// imapServer is go-imap's in-memory server behind TLS with a certificate
// the test trusts: the real client, the real protocol, no network.
type imapServer struct {
	adresse string
	be      *memory.Backend
	user    backend.User
}

func neuerIMAPServer(t *testing.T) *imapServer {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	alt := imapWurzeln
	imapWurzeln = pool
	t.Cleanup(func() { imapWurzeln = alt })

	be := memory.New()
	s := server.New(be)
	s.TLSConfig = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	l, err := tls.Listen("tcp", "127.0.0.1:0", s.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(l)
	t.Cleanup(func() { s.Close() })
	u, err := be.Login(nil, "username", "password")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return &imapServer{adresse: "localhost:" + port, be: be, user: u}
}

func (s *imapServer) legen(t *testing.T, ordner string, flags []string, roh []byte) {
	t.Helper()
	mb, err := s.user.GetMailbox(ordner)
	if err != nil {
		if err := s.user.CreateMailbox(ordner); err != nil {
			t.Fatal(err)
		}
		mb, _ = s.user.GetMailbox(ordner)
	}
	if err := mb.CreateMessage(flags, time.Now(), bytes.NewReader(roh)); err != nil {
		t.Fatal(err)
	}
}

type mhTest struct {
	h         *Mailholer
	id        *age.X25519Identity
	data      string
	verbunden int
}

func neuerMailholer(t *testing.T, host, passwort string) *mhTest {
	t.Helper()
	data := t.TempDir()
	id, _ := age.GenerateX25519Identity()
	os.WriteFile(filepath.Join(data, "briefkasten.pub"), []byte(id.Recipient().String()+"\n"), 0o644)
	sch := Schluessel{Dir: filepath.Join(data, "schluessel")}
	b, _ := imapJSON(host, "username", passwort)
	sch.Speichern(SchluesselIMAP, b)
	mt := &mhTest{id: id, data: data}
	mt.h = &Mailholer{Post: filepath.Join(data, "briefkasten"), Empfaenger: filepath.Join(data, "briefkasten.pub"),
		Gesehen: &Gesehen{Dir: filepath.Join(data, "gesehen")}, Schluessel: sch, Protokoll: LadeProtokoll(filepath.Join(data, "archiv-stand.json")),
		Now: func() time.Time { return tag }}
	mt.h.Verbinden = func(ctx context.Context, z imapZugang) (Postfach, error) {
		mt.verbunden++
		return ImapVerbinden(ctx, z)
	}
	return mt
}

// mails opens every packet and returns its Maildir entries, per packet.
func (mt *mhTest) mails(t *testing.T) (pakete []string, eintraege [][]string, inhalt map[string][]byte) {
	t.Helper()
	inhalt = map[string][]byte{}
	for _, name := range Pakete(mt.h.Post) {
		f, _ := os.Open(filepath.Join(mt.h.Post, name))
		var e []string
		m, err := PaketLesen(f, mt.id, func(n string, r io.Reader) error {
			b, _ := io.ReadAll(r)
			e = append(e, n)
			inhalt[n] = b
			return nil
		}, func(Nachricht) error { return nil })
		f.Close()
		if err != nil || m.Quelle != "mail" || m.Konto != "username" {
			t.Fatalf("%s: %v %+v", name, err, m)
		}
		sort.Strings(e)
		pakete, eintraege = append(pakete, name), append(eintraege, e)
	}
	return
}

func TestMailIsFetchedOverIMAPIntoAnEncryptedMaildirPacket(t *testing.T) {
	srv := neuerIMAPServer(t)
	srv.legen(t, "INBOX", nil, mailText("ungelesen", "Noch nicht gelesen"))
	srv.legen(t, "Arbeit.Projekte", []string{imap.FlaggedFlag}, mailText("projekt", "Projekt"))
	mt := neuerMailholer(t, srv.adresse, "password")
	mt.h.Runde(context.Background())

	pakete, e, inhalt := mt.mails(t)
	if len(pakete) != 1 || !strings.HasSuffix(pakete[0], "-mail.age") {
		t.Fatalf("%v", pakete)
	}
	// memory.New() has one seen message in INBOX already; the folder
	// "Arbeit.Projekte" has the delimiter "/" there, so it is one level.
	want := []string{"maildir/Arbeit.Projekte/cur/", "maildir/INBOX/cur/", "maildir/INBOX/cur/"}
	if len(e[0]) != 3 {
		t.Fatalf("%v", e[0])
	}
	for i, w := range want {
		if !strings.HasPrefix(e[0][i], w) {
			t.Fatalf("%v", e[0])
		}
	}
	var gelesen, ungelesen, markiert int
	for n, b := range inhalt {
		switch {
		case strings.HasSuffix(n, ":2,S"):
			gelesen++
		case strings.HasSuffix(n, ":2,") && bytes.Contains(b, []byte("Noch nicht gelesen")):
			ungelesen++
		case strings.HasSuffix(n, ":2,F") && bytes.Contains(b, []byte("Projekt")):
			markiert++
		}
	}
	if gelesen != 1 || ungelesen != 1 || markiert != 1 {
		t.Fatalf("flags: %v", e[0])
	}
	raw, _ := os.ReadFile(filepath.Join(mt.h.Post, pakete[0]))
	if bytes.Contains(raw, []byte("Projekt")) || bytes.Contains(raw, []byte("example.org")) {
		t.Fatal("plaintext in the packet")
	}
	if g := mt.h.Protokoll.Pakete()["mail"]; g.Nachrichten != 3 || g.Paket != pakete[0] {
		t.Fatalf("%+v", g)
	}
	stand, _ := os.ReadFile(filepath.Join(mt.data, "gesehen", "mail-stand.json"))
	if bytes.Contains(stand, []byte("INBOX")) || bytes.Contains(stand, []byte("Arbeit")) {
		t.Fatalf("folder names in the fetch state: %s", stand)
	}

	// the next round: nothing new, no packet; then one new mail, one packet with it alone
	mt.h.Runde(context.Background())
	if p := Pakete(mt.h.Post); len(p) != 1 {
		t.Fatalf("a round without news made a packet: %v", p)
	}
	srv.legen(t, "INBOX", []string{imap.SeenFlag}, mailText("neu", "Ganz neu"))
	mt.h.Now = func() time.Time { return tag.Add(time.Hour) }
	mt.h.Runde(context.Background())
	pakete, e, inhalt = mt.mails(t)
	if len(pakete) != 2 || len(e[1]) != 1 || !bytes.Contains(inhalt[e[1][0]], []byte("Ganz neu")) {
		t.Fatalf("%v %v", pakete, e)
	}

	// the same message copied into another folder: fetched, not packed again
	srv.legen(t, "Kopie", nil, mailText("neu", "Ganz neu"))
	mt.h.Now = func() time.Time { return tag.Add(2 * time.Hour) }
	mt.h.Runde(context.Background())
	if p := Pakete(mt.h.Post); len(p) != 2 {
		t.Fatalf("a copy of a packed mail made a packet: %v", p)
	}
	keineTmp(t, mt.h.Post)
}

func TestARefusedLoginSaysSoAndWaitsForANewPassword(t *testing.T) {
	srv := neuerIMAPServer(t)
	mt := neuerMailholer(t, srv.adresse, "falsch")
	mt.h.Runde(context.Background())
	_, f, _ := mt.h.Protokoll.Kopie()
	if f == nil || f.Satz != saysMailAnmeldung || f.Quelle != "mail" {
		t.Fatalf("%+v", f)
	}
	mt.h.Runde(context.Background())
	if mt.verbunden != 1 {
		t.Fatalf("tried the refused password again: %d", mt.verbunden)
	}
	b, _ := imapJSON(srv.adresse, "username", "password")
	mt.h.Schluessel.Speichern(SchluesselIMAP, b)
	mt.h.NeuerSchluessel()
	if _, f, _ := mt.h.Protokoll.Kopie(); f != nil {
		t.Fatalf("the error stayed after a new password: %+v", f)
	}
	mt.h.Runde(context.Background())
	if mt.verbunden != 2 || len(Pakete(mt.h.Post)) != 1 {
		t.Fatalf("%d %v", mt.verbunden, Pakete(mt.h.Post))
	}
	s := statusBauen(Lage{Stand: Stand{Zustand: Verschlossen}, Protokoll: mt.h.Protokoll, Jetzt: tag})
	if len(s.Findings) != 0 {
		t.Fatalf("%+v", s.Findings)
	}
}

func TestAServerOutOfReachIsAnErrorForTheNextRound(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	adresse := l.Addr().String()
	l.Close()
	mt := neuerMailholer(t, adresse, "password")
	mt.h.Runde(context.Background())
	_, f, _ := mt.h.Protokoll.Kopie()
	if f == nil || !strings.Contains(f.Satz, "nicht erreichbar") {
		t.Fatalf("%+v", f)
	}
	mt.h.Runde(context.Background())
	if mt.verbunden != 2 {
		t.Fatal("an unreachable server stopped the fetching for good")
	}
	if strings.Contains(f.Satz, "password") {
		t.Fatal("password in the sentence")
	}
}

// fakePostfach serves fixed folders; it can fail a FETCH after n messages.
type fakePostfach struct {
	ordner     []PostOrdner
	mails      map[string][]Mail
	aktuell    string
	fehlerNach int
	geholt     int
}

func (f *fakePostfach) Ordner() ([]PostOrdner, error)    { return f.ordner, nil }
func (f *fakePostfach) Waehlen(n string) (uint32, error) { f.aktuell = n; return 7, nil }
func (f *fakePostfach) NeueUIDs(nach uint32) ([]uint32, error) {
	var out []uint32
	for _, m := range f.mails[f.aktuell] {
		if m.UID > nach {
			out = append(out, m.UID)
		}
	}
	return out, nil
}
func (f *fakePostfach) Holen(uids []uint32, fn func(Mail) error) error {
	for _, u := range uids {
		for _, m := range f.mails[f.aktuell] {
			if m.UID == u {
				if f.fehlerNach > 0 && f.geholt >= f.fehlerNach {
					return errors.New("connection reset")
				}
				f.geholt++
				if err := fn(m); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (f *fakePostfach) Abmelden() error { return nil }

func TestBigMailboxesBecomeSeveralPacketsAndABreakLosesNothing(t *testing.T) {
	var ms []Mail
	for i := 1; i <= 10; i++ {
		ms = append(ms, Mail{UID: uint32(i), Roh: append(mailText(fmt.Sprint(i), fmt.Sprint("Mail ", i)), bytes.Repeat([]byte("x"), 1000)...)})
	}
	pf := &fakePostfach{ordner: []PostOrdner{{Name: "INBOX", Trenner: "/"}}, mails: map[string][]Mail{"INBOX": ms}, fehlerNach: 7}
	mt := neuerMailholer(t, "x", "y")
	var mu sync.Mutex
	mt.h.Verbinden = func(context.Context, imapZugang) (Postfach, error) { mu.Lock(); defer mu.Unlock(); return pf, nil }
	mt.h.Buendel, mt.h.PaketBytes = 2, 3000 // two mails per FETCH, a packet closes after the second FETCH
	mt.h.Runde(context.Background())
	_, e, _ := mt.mails(t)
	if len(e) != 1 || len(e[0]) != 4 {
		t.Fatalf("before the break: %v", e) // 1–4 in a packet; 5–6 were fetched but their packet was not finished
	}
	if _, f, _ := mt.h.Protokoll.Kopie(); f == nil || !strings.Contains(f.Satz, "brach ab") {
		t.Fatalf("%+v", f)
	}
	pf.fehlerNach = 0
	mt.h.Now = func() time.Time { return tag.Add(time.Hour) }
	mt.h.Runde(context.Background())
	_, e, inhalt := mt.mails(t)
	var alle []string
	for _, p := range e {
		alle = append(alle, p...)
	}
	if len(alle) != 10 {
		t.Fatalf("after the break: %d mails, want 10: %v", len(alle), e)
	}
	gesehen := map[string]bool{}
	for _, b := range inhalt {
		gesehen[strings.SplitN(string(b), "\r\n", 6)[4]] = true
	}
	if len(gesehen) != 10 {
		t.Fatalf("lost or doubled: %v", gesehen)
	}
	if _, f, _ := mt.h.Protokoll.Kopie(); f != nil {
		t.Fatalf("the error stayed: %+v", f)
	}
}

func TestGmailIsReadInAllMailOnly(t *testing.T) {
	alle := []PostOrdner{{Name: "INBOX"}, {Name: "[Gmail]", Attribute: []string{imap.NoSelectAttr}}, {Name: "[Gmail]/Alle Nachrichten", Trenner: "/", Attribute: []string{imap.AllAttr}},
		{Name: "[Gmail]/Spam", Attribute: []string{imap.JunkAttr}}, {Name: "Urlaub"}}
	if w := ordnerWahl(alle); len(w) != 1 || w[0].Name != "[Gmail]/Alle Nachrichten" || maildirPfad(w[0]) != "[Gmail]/Alle Nachrichten" {
		t.Fatalf("%+v", w)
	}
	anders := []PostOrdner{{Name: "Urlaub"}, {Name: "INBOX"}, {Name: "Papierkorb", Attribute: []string{imap.TrashAttr}}, {Name: "Spam", Attribute: []string{imap.JunkAttr}}, {Name: "Ordner", Attribute: []string{imap.NoSelectAttr}}}
	if w := ordnerWahl(anders); len(w) != 2 || w[0].Name != "INBOX" || w[1].Name != "Urlaub" {
		t.Fatalf("%+v", w)
	}
}

func TestFolderNamesCannotLeaveTheMaildir(t *testing.T) {
	for name, want := range map[string]string{
		"INBOX.Arbeit":    "INBOX/Arbeit",
		"../../etc":       "_/_/_/_/_etc",
		".versteckt":      "_/versteckt",
		"a..b":            "a/_/b",
		"Rechnungen/2026": "Rechnungen_2026",
	} {
		got := maildirPfad(PostOrdner{Name: name, Trenner: "."})
		if got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
		if e := "maildir/" + got + "/cur/1.1.0123456789abcdef:2,"; !maildirEintrag(e) {
			t.Errorf("%q: the packet would refuse %q", name, e)
		}
	}
	for _, schlecht := range []string{"maildir/../x/cur/1", "maildir/x/cur/.1", "maildir/.x/cur/1", "maildir/cur/1", "maildir/x/new/1", "maildir/x//cur/1", "anhaenge/x"} {
		if maildirEintrag(schlecht) {
			t.Errorf("%q accepted", schlecht)
		}
	}
}

// fakeMaildirImport records what import-maildir was handed.
type fakeMaildirImport struct {
	befehle []Befehl
	baum    []string
}

func (f *fakeMaildirImport) Run(ctx context.Context, b Befehl) (Ausgabe, error) {
	f.befehle = append(f.befehle, b)
	filepath.WalkDir(b.Args[1], func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(b.Args[1], p)
		if d.IsDir() {
			rel += "/"
		}
		f.baum = append(f.baum, rel)
		return nil
	})
	return Ausgabe{Text: "Mailboxes: 2/2\nMessages: 3 added, 1 updated, 0 skipped, 0 error(s)\n"}, nil
}

func TestPostboteHandsAMailPacketToImportMaildir(t *testing.T) {
	srv := neuerIMAPServer(t)
	srv.legen(t, "Arbeit", nil, mailText("a", "Arbeit"))
	mt := neuerMailholer(t, srv.adresse, "password")
	mt.h.Runde(context.Background())
	if len(Pakete(mt.h.Post)) != 1 {
		t.Fatal("no packet")
	}
	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(mt.id.String()), 0o600)
	mv := &fakeMaildirImport{}
	// no phone number stored: a mail packet does not need one
	pb := &Postbote{Post: mt.h.Post, Schluessel: Schluessel{Dir: filepath.Join(mt.data, "leer")}, Protokoll: mt.h.Protokoll, Run: mv, Msgvault: "msgvault"}
	pb.Runde(context.Background(), innen)
	if len(mv.befehle) != 1 {
		t.Fatalf("%d", len(mv.befehle))
	}
	a := mv.befehle[0].Args
	if a[0] != "import-maildir" || !strings.HasPrefix(a[1], innen.Arbeit()) || a[2] != "--identifier" || a[3] != "username" || len(a) != 5 || a[4] != "--no-resume" {
		t.Fatalf("%q", a)
	}
	baum := strings.Join(mv.baum, " ")
	for _, w := range []string{"Arbeit/cur/", "Arbeit/new/", "Arbeit/tmp/", "INBOX/cur/", "INBOX/new/", "INBOX/tmp/"} {
		if !strings.Contains(baum, w) {
			t.Fatalf("%s missing: %s", w, baum)
		}
	}
	if len(Pakete(mt.h.Post)) != 0 {
		t.Fatal("the packet stayed")
	}
	leer(t, innen.Arbeit(), "the vault's work folder")
	ein, _, _ := mt.h.Protokoll.Kopie()
	if e := ein["mail"]; e.Zahlen.Mails != 3 || e.Anzahl != 1 {
		t.Fatalf("%+v", e)
	}
}

func TestTheRoundSaysHowFarItIs(t *testing.T) {
	var ms []Mail
	for i := 1; i <= 10; i++ {
		ms = append(ms, Mail{UID: uint32(i), Datum: time.Date(2020+i, 3, 1, 12, 0, 0, 0, time.UTC), Roh: mailText(fmt.Sprint(i), fmt.Sprint("Mail ", i))})
	}
	pf := &fakePostfach{ordner: []PostOrdner{{Name: "INBOX", Trenner: "/"}}, mails: map[string][]Mail{"INBOX": ms}}
	mt := neuerMailholer(t, "x", "y")
	mt.h.Verbinden = func(context.Context, imapZugang) (Postfach, error) { return pf, nil }
	mt.h.Buendel = 4
	var gaenge []Gang
	mt.h.Geaendert = func() { gaenge = append(gaenge, mt.h.Gang()) }
	mt.h.Runde(context.Background())
	var fertig []int
	for _, g := range gaenge {
		if g.Gesamt == 10 {
			fertig = append(fertig, g.Fertig)
		}
	}
	if fmt.Sprint(fertig) != "[0 4 8 10]" {
		t.Fatalf("progress: %v (%+v)", fertig, gaenge)
	}
	if b := gaenge[len(gaenge)-2].Bis; b.Year() != 2030 {
		t.Fatalf("newest mail so far: %v", b)
	}
	if mt.h.Gang().Was != "" {
		t.Fatal("a finished round still shows as running")
	}
}

func TestAnotherMailboxIsFetchedFromTheStart(t *testing.T) {
	post := func(text string) *fakePostfach {
		var ms []Mail
		for i := 1; i <= 3; i++ {
			ms = append(ms, Mail{UID: uint32(i), Roh: mailText(text+fmt.Sprint(i), text)})
		}
		return &fakePostfach{ordner: []PostOrdner{{Name: "INBOX", Trenner: "/"}}, mails: map[string][]Mail{"INBOX": ms}}
	}
	mt := neuerMailholer(t, "x", "y")
	pf := post("erstes")
	mt.h.Verbinden = func(context.Context, imapZugang) (Postfach, error) { return pf, nil }
	mt.h.Runde(context.Background())
	pf = post("zweites") // the same folder, the same numbers, other mail
	mt.h.KontoGewechselt()
	mt.h.Now = func() time.Time { return tag.Add(time.Hour) }
	mt.h.Runde(context.Background())
	_, e, _ := mt.mails(t)
	n := 0
	for _, p := range e {
		n += len(p)
	}
	if n != 6 {
		t.Fatalf("the second mailbox was not fetched from the start: %v", e)
	}
}

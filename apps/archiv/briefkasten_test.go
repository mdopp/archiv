package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
)

const annaTel = "+4915112345678"

var bild = []byte("\x89PNG\r\n\x1a\nkein echtes Bild, aber Bytes")

// signalXML is what signalbackup-tools --exportxml writes, in small.
func signalXML(nachrichten ...string) string {
	return "<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\" ?>\n<smses count=\"" + fmt.Sprint(len(nachrichten)) + "\">\n" + strings.Join(nachrichten, "\n") + "\n</smses>\n"
}

func sms(zeit int64, typ int, adresse, text string) string {
	return fmt.Sprintf(`<sms protocol="0" address="%s" date="%d" type="%d" subject="null" body="%s" toa="null" sc_toa="null" service_center="null" read="1" status="-1" locked="0" date_sent="0" readable_date="" contact_name="Ben" />`, adresse, zeit, typ, text)
}

func mmsMitBild(zeit int64) string {
	return fmt.Sprintf(`<mms date="%d" msg_box="1" read="1" address="+4917011111111~+4917022222222" contact_name="Familie" m_type="132">
  <parts>
    <part seq="0" ct="text/plain" name="null" chset="106" cl="null" text="Schaut mal &amp; staunt" />
    <part seq="1" ct="image/png" name="bild.png" chset="null" cl="bild.png" text="null" data="%s" />
  </parts>
  <addrs>
    <addr address="+4917011111111" type="137" charset="106" />
    <addr address="%s" type="151" charset="106" />
    <addr address="+4917022222222" type="151" charset="106" />
  </addrs>
</mms>`, zeit, base64.StdEncoding.EncodeToString(bild), annaTel)
}

func TestPaketRoundTrip(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	var buf bytes.Buffer
	p, err := NeuesPaket(&buf, id.Recipient(), Manifest{Quelle: "signal", Herkunft: "telefon"}, tag)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := p.Anhang("image/png", "bild.png", bild)
	p.Nachricht(Nachricht{ID: "x:1", Zeit: 1, Richtung: "ein", Absender: "+491", Teilnehmer: []string{"+491"}, Text: "hallo", Anhaenge: []Anhang{a}})
	p.Nachricht(Nachricht{ID: "x:2", Zeit: 2, Richtung: "aus", Teilnehmer: []string{"+491"}, Text: "zurück"})
	if err := p.Schliessen(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("hallo")) || bytes.Contains(buf.Bytes(), bild[8:]) {
		t.Fatal("plaintext in the packet")
	}
	var ns []Nachricht
	anh := map[string][]byte{}
	m, err := PaketLesen(bytes.NewReader(buf.Bytes()), id, func(name string, r io.Reader) error {
		b, _ := io.ReadAll(r)
		anh[name] = b
		return nil
	}, func(n Nachricht) error { ns = append(ns, n); return nil })
	if err != nil || m.Format != "archiv-paket" || m.Version != 1 || m.Quelle != "signal" || m.Herkunft != "telefon" || m.Erstellt != "2026-10-01T03:00:00Z" {
		t.Fatalf("%v %+v", err, m)
	}
	if len(ns) != 2 || ns[0].Text != "hallo" || ns[1].Richtung != "aus" || !bytes.Equal(anh["000001"], bild) || ns[0].Anhaenge[0].Datei != "anhaenge/000001" {
		t.Fatalf("%+v %v", ns, anh)
	}
	anderer, _ := age.GenerateX25519Identity()
	if _, err := PaketLesen(bytes.NewReader(buf.Bytes()), anderer, nil, nil); err == nil {
		t.Fatal("another key opened the packet")
	}
}

// The XML from signalbackup-tools goes through the packet and back into
// an XML that carries the same messages.
func TestXMLThroughThePacketAndBack(t *testing.T) {
	in := signalXML(sms(1700000000000, 1, "+4917011111111", "Hallo &quot;du&quot;\nzweite Zeile"), sms(1700000100000, 2, "+4917011111111", "Tschüss"), mmsMitBild(1700000200000))
	var erst []xmlNachricht
	if err := XMLLesen(strings.NewReader(in), annaTel, func(x xmlNachricht) error { erst = append(erst, x); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(erst) != 3 || erst[0].N.Text != "Hallo \"du\"\nzweite Zeile" || erst[0].N.Richtung != "ein" || erst[1].N.Richtung != "aus" {
		t.Fatalf("%+v", erst)
	}
	m := erst[2].N
	if m.Absender != "+4917011111111" || strings.Join(m.Teilnehmer, ",") != "+4917011111111,+4917022222222" || m.Text != "Schaut mal & staunt" || len(m.Anhaenge) != 1 || !bytes.Equal(erst[2].Daten[0], bild) {
		t.Fatalf("mms: %+v", m)
	}
	var out bytes.Buffer
	x, _ := NeuesXML(&out, annaTel)
	for _, e := range erst {
		daten := e.Daten
		x.Nachricht(e.N, func(a Anhang) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(daten[0])), nil })
	}
	x.Schliessen()
	var zweit []xmlNachricht
	if err := XMLLesen(&out, annaTel, func(x xmlNachricht) error { zweit = append(zweit, x); return nil }); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for i := range erst {
		a, b := erst[i], zweit[i]
		if a.N.identitaet(summen(a.Daten)) != b.N.identitaet(summen(b.Daten)) {
			t.Fatalf("message %d changed on the way:\n%+v\n%+v", i, a.N, b.N)
		}
	}
}

func TestACutXMLIsNoPacket(t *testing.T) {
	in := signalXML(sms(1, 1, "+491", "a"))
	if err := XMLLesen(strings.NewReader(in[:len(in)-12]), annaTel, func(xmlNachricht) error { return nil }); err == nil {
		t.Fatal("an XML without </smses> was read as whole")
	}
}

// --- the Briefkasten, vault closed

// fakeSignal plays signalbackup-tools: it writes an XML into the pipe.
type fakeSignal struct {
	mu      sync.Mutex
	befehle []Befehl
	xml     map[string]string // backup name → XML; missing: exit 1 without touching the pipe
	halb    bool              // write half the XML, then fail
	falsch  bool
}

func (f *fakeSignal) Run(ctx context.Context, b Befehl) (Ausgabe, error) {
	f.mu.Lock()
	f.befehle = append(f.befehle, b)
	f.mu.Unlock()
	if f.falsch {
		return Ausgabe{Code: 1, Text: " *** NOTE : IT IS LIKELY AN INCORRECT PASSPHRASE WAS PROVIDED ***"}, nil
	}
	x, ok := f.xml[filepath.Base(b.Args[0])]
	if !ok {
		return Ausgabe{Code: 1, Text: "[Error]: Failed to open input file"}, nil
	}
	rohr := b.Args[2]
	if fi, err := os.Stat(rohr); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		return Ausgabe{Code: 2, Text: "the XML target is not a named pipe"}, nil
	}
	w, err := os.OpenFile(rohr, os.O_WRONLY, 0) // blocks until the reader is there, like the real one
	if err != nil {
		return Ausgabe{Code: 1}, nil
	}
	if f.halb {
		w.Write([]byte(x[:len(x)/2]))
		w.Close()
		return Ausgabe{Code: 1, Text: "[Error]: write failed"}, nil
	}
	w.Write([]byte(x))
	w.Close()
	return Ausgabe{Text: "Exporting backup to '" + rohr + "'"}, nil
}

func (f *fakeSignal) anzahl() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.befehle) }

type bkTest struct {
	b     *Briefkasten
	run   *fakeSignal
	id    *age.X25519Identity
	inbox string
	data  string
	klar  string
}

func neuerBriefkasten(t *testing.T) *bkTest {
	t.Helper()
	inbox, data, klar := t.TempDir(), t.TempDir(), t.TempDir()
	id, _ := age.GenerateX25519Identity()
	os.WriteFile(filepath.Join(data, "briefkasten.pub"), []byte(id.Recipient().String()+"\n"), 0o644)
	sch := Schluessel{Dir: filepath.Join(data, "schluessel")}
	sch.Speichern(SchluesselSignal, []byte(passphrase))
	sch.Speichern(SchluesselTelefon, []byte(annaTel))
	run := &fakeSignal{xml: map[string]string{}}
	b := &Briefkasten{Eingang: &Eingang{Dir: inbox}, Letzte: filepath.Join(inbox, "letzte"), Post: filepath.Join(data, "briefkasten"),
		Empfaenger: filepath.Join(data, "briefkasten.pub"), Gesehen: &Gesehen{Dir: filepath.Join(data, "gesehen")}, Schluessel: sch,
		Klartext: klar, Run: run, SignalBin: "signalbackup-tools", Protokoll: LadeProtokoll(filepath.Join(data, "archiv-stand.json")),
		Now: func() time.Time { return tag.Add(240 * time.Hour) }}
	return &bkTest{b: b, run: run, id: id, inbox: inbox, data: data, klar: klar}
}

func (bt *bkTest) runde() {
	bt.b.Eingang.Stabil("signal")
	bt.b.Eingang.StabilMedien()
	bt.b.Eingang.Stabil("whatsapp")
	bt.b.Runde(context.Background())
}

// lies opens every packet with the private key.
func (bt *bkTest) lies(t *testing.T) (pakete []string, ns []Nachricht) {
	t.Helper()
	for _, name := range Pakete(bt.b.Post) {
		f, _ := os.Open(filepath.Join(bt.b.Post, name))
		_, err := PaketLesen(f, bt.id, func(string, io.Reader) error { return nil }, func(n Nachricht) error { ns = append(ns, n); return nil })
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		pakete = append(pakete, name)
	}
	return
}

func leer(t *testing.T, dir, was string) {
	t.Helper()
	e, _ := os.ReadDir(dir)
	if len(e) != 0 {
		t.Fatalf("%s not empty: %s", was, e[0].Name())
	}
}

func keineTmp(t *testing.T, post string) {
	t.Helper()
	e, _ := os.ReadDir(post)
	for _, x := range e {
		if strings.HasPrefix(x.Name(), ".") {
			t.Fatalf("an unfinished packet stayed: %s", x.Name())
		}
	}
}

func TestBriefkastenPacksWhileClosedAndKeepsTheNewestBackup(t *testing.T) {
	bt := neuerBriefkasten(t)
	tt := neuerTresor(t) // a vault that is never opened: the Briefkasten has no way to it
	tt.Anlegen(context.Background(), []byte("richtig-langes-pw"))
	tt.bereit(t)
	<-tt.lauf
	tt.Abschliessen(context.Background(), "test")

	legen(t, bt.inbox, "signal/signal-2026-10-01.backup", 5, tag)
	bt.run.xml["signal-2026-10-01.backup"] = signalXML(sms(1700000000000, 1, "+4917011111111", "eins"), mmsMitBild(1700000200000))
	bt.runde()

	pakete, ns := bt.lies(t)
	if len(pakete) != 1 || !strings.HasSuffix(pakete[0], "-signal.age") || len(ns) != 2 || ns[0].Text != "eins" || ns[1].Anhaenge[0].Datei != "anhaenge/000001" {
		t.Fatalf("%v %+v", pakete, ns)
	}
	if !strings.HasPrefix(ns[0].ID, "signal:") {
		t.Fatalf("id %q", ns[0].ID)
	}
	if _, err := os.Stat(filepath.Join(bt.inbox, "signal", "signal-2026-10-01.backup")); !os.IsNotExist(err) {
		t.Fatal("the packed backup is still in the inbox")
	}
	if _, err := os.Stat(filepath.Join(bt.inbox, "letzte", "signal", "signal-2026-10-01.backup")); err != nil {
		t.Fatalf("the newest backup is not kept for a new phone: %v", err)
	}
	leer(t, bt.klar, "the plaintext folder")
	keineTmp(t, bt.b.Post)
	if e, _ := os.ReadDir(tt.Mnt); len(e) != 0 {
		t.Fatal("the closed vault's mount point got files")
	}
	if g := bt.b.Protokoll.Pakete()["signal"]; g.Nachrichten != 2 || g.Anhaenge != 1 || g.Paket != pakete[0] {
		t.Fatalf("protokoll: %+v", g)
	}
	// the pipe was a pipe, the passphrase never on the command line
	b := bt.run.befehle[0]
	if strings.Contains(strings.Join(b.Args, " "), passphrase) || string(b.Terminal) != passphrase+"\n" || !strings.HasPrefix(b.Args[2], bt.klar) {
		t.Fatalf("%q", b.Args)
	}
}

func TestBriefkastenPacksOnlyWhatItHasNotSeen(t *testing.T) {
	bt := neuerBriefkasten(t)
	alt := []string{sms(1700000000000, 1, "+4917011111111", "eins"), sms(1700000100000, 2, "+4917011111111", "zwei"), mmsMitBild(1700000200000)}
	legen(t, bt.inbox, "signal/signal-1.backup", 5, tag)
	bt.run.xml["signal-1.backup"] = signalXML(alt...)
	bt.runde()
	// the next night: the same history, one new message, one deleted on the phone
	legen(t, bt.inbox, "signal/signal-2.backup", 5, tag.Add(24*time.Hour))
	bt.run.xml["signal-2.backup"] = signalXML(alt[0], alt[2], sms(1700000300000, 1, "+4917011111111", "drei"))
	bt.runde()
	pakete, ns := bt.lies(t)
	if len(pakete) != 1 && len(pakete) != 2 {
		t.Fatalf("pakete %v", pakete)
	}
	var texte []string
	for _, n := range ns {
		texte = append(texte, n.Text)
	}
	if strings.Join(texte, "|") != "eins|zwei|Schaut mal & staunt|drei" {
		t.Fatalf("messages over both packets: %q", texte)
	}
	// a third night with nothing new: no packet, the backup still becomes the newest kept
	legen(t, bt.inbox, "signal/signal-3.backup", 5, tag.Add(48*time.Hour))
	bt.run.xml["signal-3.backup"] = signalXML(alt[0])
	vorher := len(Pakete(bt.b.Post))
	bt.runde()
	if len(Pakete(bt.b.Post)) != vorher {
		t.Fatal("a packet without new messages")
	}
	if e, _ := os.ReadDir(filepath.Join(bt.inbox, "letzte", "signal")); len(e) != 1 || e[0].Name() != "signal-3.backup" {
		t.Fatalf("letzte/signal: %v", e)
	}
	// the set says nothing about the messages
	set, _ := os.ReadFile(filepath.Join(bt.data, "gesehen", "signal"))
	if len(set) != 4*32 || bytes.Contains(set, []byte("eins")) || bytes.Contains(set, []byte("4917011111111")) {
		t.Fatalf("gesehen: %d bytes", len(set))
	}
}

func TestAnOlderBackupDoesNotReplaceTheNewestKept(t *testing.T) {
	bt := neuerBriefkasten(t)
	legen(t, bt.inbox, "signal/signal-neu.backup", 5, tag.Add(48*time.Hour))
	bt.run.xml["signal-neu.backup"] = signalXML(sms(2, 1, "+491", "neu"))
	bt.runde()
	legen(t, bt.inbox, "signal/signal-alt.backup", 5, tag) // arrives late
	bt.run.xml["signal-alt.backup"] = signalXML(sms(1, 1, "+491", "alt"))
	bt.runde()
	if e, _ := os.ReadDir(filepath.Join(bt.inbox, "letzte", "signal")); len(e) != 1 || e[0].Name() != "signal-neu.backup" {
		t.Fatalf("letzte/signal: %v", e)
	}
	if l, _ := bt.b.Eingang.Liste("signal"); len(l) != 0 {
		t.Fatal("the older backup stayed in the inbox after its packet")
	}
}

func TestBriefkastenFailureLeavesNoPlaintextAndKeepsTheBackup(t *testing.T) {
	for _, fall := range []string{"halb", "nie"} {
		t.Run(fall, func(t *testing.T) {
			bt := neuerBriefkasten(t)
			p := legen(t, bt.inbox, "signal/signal-1.backup", 5, tag)
			if fall == "halb" {
				bt.run.halb = true
				bt.run.xml["signal-1.backup"] = signalXML(sms(1, 1, "+491", "eins"), sms(2, 1, "+491", "zwei"))
			} // "nie": the program fails before it opens the pipe
			fertig := make(chan struct{})
			go func() { bt.runde(); close(fertig) }()
			select {
			case <-fertig:
			case <-time.After(10 * time.Second):
				t.Fatal("hangs on a pipe nobody opened")
			}
			if _, err := os.Stat(p); err != nil {
				t.Fatal("the failed backup left the inbox")
			}
			if len(Pakete(bt.b.Post)) != 0 {
				t.Fatal("a packet from a failed export")
			}
			keineTmp(t, bt.b.Post)
			leer(t, bt.klar, "the plaintext folder")
			if _, f, _ := bt.b.Protokoll.Kopie(); f == nil || !strings.Contains(f.Satz, "signal-1.backup") {
				t.Fatalf("fehler %+v", f)
			}
			if set, _ := os.ReadFile(filepath.Join(bt.data, "gesehen", "signal")); len(set) != 0 {
				t.Fatal("a failed packet marked messages as seen")
			}
		})
	}
}

func TestBriefkastenWrongPassphrase(t *testing.T) {
	bt := neuerBriefkasten(t)
	legen(t, bt.inbox, "signal/signal-1.backup", 5, tag)
	legen(t, bt.inbox, "signal/signal-2.backup", 5, tag.Add(time.Hour))
	bt.run.falsch = true
	bt.runde()
	bt.runde()
	if bt.run.anzahl() != 1 {
		t.Fatalf("the wrong passphrase was tried %d times", bt.run.anzahl())
	}
	if _, f, _ := bt.b.Protokoll.Kopie(); f == nil || f.Satz != "Die Signal-Passphrase passt nicht zu dieser Sicherung. Gib sie neu ein." {
		t.Fatalf("%+v", f)
	}
	bt.run.falsch = false
	bt.run.xml["signal-1.backup"] = signalXML(sms(1, 1, "+491", "eins"))
	bt.run.xml["signal-2.backup"] = signalXML(sms(2, 1, "+491", "zwei"))
	bt.b.NeuerSchluessel()
	bt.runde()
	if _, f, _ := bt.b.Protokoll.Kopie(); f != nil || len(Pakete(bt.b.Post)) == 0 {
		t.Fatalf("after the new key: %+v", f)
	}
}

func TestBriefkastenWaitsForTheKeys(t *testing.T) {
	bt := neuerBriefkasten(t)
	os.Remove(filepath.Join(bt.data, "schluessel", SchluesselSignal))
	legen(t, bt.inbox, "signal/signal-1.backup", 5, tag)
	bt.runde()
	if bt.run.anzahl() != 0 || !bt.b.FehlenSchluessel() {
		t.Fatal("ran without the passphrase")
	}
	os.Remove(filepath.Join(bt.data, "briefkasten.pub"))
	bt.b.Schluessel.Speichern(SchluesselSignal, []byte(passphrase))
	bt.runde()
	if bt.run.anzahl() != 0 {
		t.Fatal("packed without a public key: the vault was never made")
	}
}

func TestWhatsappNewestDatabaseAndEncryptedMedia(t *testing.T) {
	bt := neuerBriefkasten(t)
	legen(t, bt.inbox, "whatsapp/Databases/msgstore.db.crypt15.20261001-030000", 3, tag)
	neu := legen(t, bt.inbox, "whatsapp/Databases/msgstore.db.crypt15", 7, tag.Add(24*time.Hour))
	m := legen(t, bt.inbox, "whatsapp/Media/WhatsApp Images/IMG-1.jpg", 10, tag)
	os.WriteFile(m, []byte("KLARTEXT-BILD"), 0o644)
	legen(t, bt.inbox, "whatsapp/Media/.Statuses/x.jpg", 3, tag) // a dot-entry is not touched
	bt.runde()

	db := filepath.Join(bt.inbox, "letzte", "whatsapp", "Databases", "msgstore.db.crypt15")
	a, _ := os.Stat(db)
	b, _ := os.Stat(neu)
	if a == nil || b == nil || !os.SameFile(a, b) {
		t.Fatal("the newest database is not kept in letzte/ (or the inbox lost it)")
	}
	if n := wartende(bt.b.Eingang)["whatsapp"].Wartend; n != 2 {
		t.Fatalf("the inbox databases wait for the WhatsApp import: %d", n)
	}
	enc := filepath.Join(bt.inbox, "letzte", "whatsapp", "Media", "WhatsApp Images", "IMG-1.jpg.age")
	c, err := os.ReadFile(enc)
	if err != nil || bytes.Contains(c, []byte("KLARTEXT")) {
		t.Fatalf("media not encrypted: %v", err)
	}
	r, err := age.Decrypt(bytes.NewReader(c), bt.id)
	if err != nil {
		t.Fatal(err)
	}
	if klar, _ := io.ReadAll(r); string(klar) != "KLARTEXT-BILD" {
		t.Fatalf("%q", klar)
	}
	if _, err := os.Stat(m); !os.IsNotExist(err) {
		t.Fatal("the plaintext media stayed in the inbox")
	}
	if _, err := os.Stat(filepath.Join(bt.inbox, "whatsapp", "Media", ".Statuses", "x.jpg")); err != nil {
		t.Fatal("a dot-entry was touched")
	}
}

// --- the Postbote, vault open

type fakeMsgvault struct {
	mu      sync.Mutex
	befehle []Befehl
	xml     []string // what each import saw
	fehler  bool
}

func (f *fakeMsgvault) Run(ctx context.Context, b Befehl) (Ausgabe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.befehle = append(f.befehle, b)
	x, _ := os.ReadFile(b.Args[1])
	f.xml = append(f.xml, string(x))
	if f.fehler {
		return Ausgabe{Code: 1, Text: "Error: database is locked"}, nil
	}
	return Ausgabe{Text: "Imported 2 SMS, 1 MMS, 0 calls, 1 attachments from 1 files"}, nil
}

func TestPostboteBringsPacketsInOldestFirstAndDeletesThem(t *testing.T) {
	bt := neuerBriefkasten(t)
	legen(t, bt.inbox, "signal/signal-1.backup", 5, tag)
	bt.run.xml["signal-1.backup"] = signalXML(sms(1700000000000, 1, "+4917011111111", "eins"), mmsMitBild(1700000200000))
	bt.runde()
	bt.b.Now = func() time.Time { return tag.Add(241 * time.Hour) }
	legen(t, bt.inbox, "signal/signal-2.backup", 5, tag.Add(time.Hour))
	bt.run.xml["signal-2.backup"] = signalXML(sms(1700000300000, 2, "+4917011111111", "zwei"))
	bt.runde()
	pakete := Pakete(bt.b.Post)
	if len(pakete) != 2 {
		t.Fatalf("%v", pakete)
	}

	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(bt.id.String()), 0o600)
	mv := &fakeMsgvault{}
	pb := &Postbote{Post: bt.b.Post, Schluessel: bt.b.Schluessel, Protokoll: bt.b.Protokoll, Run: mv, Msgvault: "msgvault"}
	pb.Runde(context.Background(), innen)

	if len(mv.xml) != 2 || !strings.Contains(mv.xml[0], `body="eins"`) || !strings.Contains(mv.xml[1], `body="zwei"`) {
		t.Fatalf("order or content: %q", mv.xml)
	}
	if !strings.Contains(mv.xml[0], base64.StdEncoding.EncodeToString(bild)) || !strings.Contains(mv.xml[0], `<addr address="`+annaTel+`" type="151"`) {
		t.Fatalf("the MMS lost its picture or the person:\n%s", mv.xml[0])
	}
	for _, b := range mv.befehle {
		if strings.Join(b.Args[:1], "") != "import-synctech-sms" || b.Args[3] != annaTel || !strings.HasPrefix(b.Args[1], innen.Arbeit()) {
			t.Fatalf("%q", b.Args)
		}
	}
	if len(Pakete(bt.b.Post)) != 0 {
		t.Fatal("imported packets stayed in the Briefkasten")
	}
	leer(t, innen.Arbeit(), "the vault's work folder")
	ein, _, _ := bt.b.Protokoll.Kopie()
	if e := ein["signal"]; e.Datei != pakete[1] || e.Anzahl != 2 || e.Zahlen.Nachrichten() != 3 {
		t.Fatalf("%+v", e)
	}
}

func TestPostboteKeepsAPacketItCouldNotImport(t *testing.T) {
	bt := neuerBriefkasten(t)
	legen(t, bt.inbox, "signal/signal-1.backup", 5, tag)
	bt.run.xml["signal-1.backup"] = signalXML(mmsMitBild(1700000200000))
	bt.runde()
	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(bt.id.String()), 0o600)
	mv := &fakeMsgvault{fehler: true}
	pb := &Postbote{Post: bt.b.Post, Schluessel: bt.b.Schluessel, Protokoll: bt.b.Protokoll, Run: mv, Msgvault: "msgvault"}
	pb.Runde(context.Background(), innen)
	pb.Runde(context.Background(), innen)
	if len(Pakete(bt.b.Post)) != 1 || len(mv.befehle) != 1 {
		t.Fatalf("packet gone or tried twice in one opening: %d", len(mv.befehle))
	}
	leer(t, innen.Arbeit(), "the vault's work folder")
	if _, f, _ := bt.b.Protokoll.Kopie(); f == nil || !strings.Contains(f.Satz, "nicht übernehmen") {
		t.Fatalf("%+v", f)
	}
}

func TestTheVaultMakesTheKeyPairOnce(t *testing.T) {
	tt := neuerTresor(t)
	tt.Empfaenger = filepath.Join(tt.dir, "briefkasten.pub")
	tt.Anlegen(context.Background(), []byte("richtig-langes-pw"))
	tt.bereit(t)
	innen := <-tt.lauf
	id, err := identitaetLesen(innen.Identitaet())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := empfaengerLesen(tt.Empfaenger)
	if err != nil || pub.String() != id.Recipient().String() {
		t.Fatalf("public key outside %v does not match the private one inside", err)
	}
	if fi, _ := os.Stat(innen.Identitaet()); fi.Mode().Perm() != 0o600 {
		t.Fatal("private key mode")
	}
	tt.Abschliessen(context.Background(), "t")
	tt.Oeffnen(context.Background(), []byte("richtig-langes-pw"))
	tt.bereit(t)
	innen = <-tt.lauf
	if id2, _ := identitaetLesen(innen.Identitaet()); id2.String() != id.String() {
		t.Fatal("reopening made a new key pair: old packets would be unreadable")
	}
}

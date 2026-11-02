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
		x.Nachricht(e.N, func(a Anhang) (io.ReadCloser, string, error) { return io.NopCloser(bytes.NewReader(daten[0])), "", nil })
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

// --- the Briefkasten

type bkTest struct {
	b     *Briefkasten
	sch   Schluessel
	id    *age.X25519Identity
	inbox string
	data  string
}

func neuerBriefkasten(t *testing.T) *bkTest {
	t.Helper()
	inbox, data := t.TempDir(), t.TempDir()
	id, _ := age.GenerateX25519Identity()
	os.WriteFile(filepath.Join(data, "briefkasten.pub"), []byte(id.Recipient().String()+"\n"), 0o644)
	sch := Schluessel{Dir: filepath.Join(data, "schluessel")}
	sch.Speichern(SchluesselTelefon, []byte(annaTel))
	b := &Briefkasten{Eingang: &Eingang{Dir: inbox}, Letzte: filepath.Join(inbox, "letzte"), Post: filepath.Join(data, "briefkasten"),
		Empfaenger: filepath.Join(data, "briefkasten.pub"), Gesehen: &Gesehen{Dir: filepath.Join(data, "gesehen")},
		Protokoll: LadeProtokoll(filepath.Join(data, "archiv-stand.json")), Now: func() time.Time { return tag.Add(240 * time.Hour) }}
	return &bkTest{b: b, sch: sch, id: id, inbox: inbox, data: data}
}

func (bt *bkTest) runde() {
	bt.b.Eingang.StabilMedien()
	bt.b.Eingang.Stabil("whatsapp")
	bt.b.Runde(context.Background())
}

// paket writes the messages of an XML as one packet into the Briefkasten,
// as the box did with a Signal backup before 2026-11-02 (such packets may
// still wait) and as `archiv paket-aus-xml` does.
func (bt *bkTest) paket(t *testing.T, xml string, jetzt time.Time) string {
	t.Helper()
	os.MkdirAll(bt.b.Post, 0o700)
	tmp := filepath.Join(bt.b.Post, ".neu-signal-"+zufall()+".age")
	out, _ := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	_, n, _, err := PackeXML(strings.NewReader(xml), out, bt.id.Recipient(), Manifest{Quelle: "signal", Herkunft: "box"}, annaTel, bt.b.Gesehen, map[[32]byte]bool{}, jetzt)
	out.Close()
	if err != nil || n == 0 {
		t.Fatalf("paket: %v (%d)", err, n)
	}
	name, err := einwerfen(tmp, bt.b.Post, "signal", jetzt)
	if err != nil {
		t.Fatal(err)
	}
	return name
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

func leer(t *testing.T, dir, was string) {
	t.Helper()
	e, _ := os.ReadDir(dir)
	if len(e) != 0 {
		t.Fatalf("%s not empty: %s", was, e[0].Name())
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
	bt.paket(t, signalXML(sms(1700000000000, 1, "+4917011111111", "eins"), mmsMitBild(1700000200000)), tag)
	bt.paket(t, signalXML(sms(1700000300000, 2, "+4917011111111", "zwei")), tag.Add(time.Hour))
	pakete := Pakete(bt.b.Post)
	if len(pakete) != 2 {
		t.Fatalf("%v", pakete)
	}

	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(bt.id.String()), 0o600)
	mv := &fakeMsgvault{}
	pb := &Postbote{Post: bt.b.Post, Schluessel: bt.sch, Protokoll: bt.b.Protokoll, Run: mv, Msgvault: "msgvault"}
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
	bt.paket(t, signalXML(mmsMitBild(1700000200000)), tag)
	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(bt.id.String()), 0o600)
	mv := &fakeMsgvault{fehler: true}
	pb := &Postbote{Post: bt.b.Post, Schluessel: bt.sch, Protokoll: bt.b.Protokoll, Run: mv, Msgvault: "msgvault"}
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

// The first real backup on the box (2026-11-02): an attachment the backup
// does not hold leaves a lone "></part>" in signalbackup-tools' XML. The
// message stays (without that attachment), and nothing else is lost.
func TestALoneClosingPartFromSignalbackupToolsIsSkipped(t *testing.T) {
	x := `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<smses count="2">
  <mms date="1700000300000" msg_box="1" read="1" address="+4915199999999" contact_name="Ben" m_type="132" text_only="0">
    <parts>
      <part seq="0" ct="text/plain" name="null" chset="106" cd="null" fn="null" cl="text000001.txt" ctt_s="null" ctt_t="null" text="Ein Bild, das fehlt" ></part>
      ></part>
      <part seq="1" ct="image/png" name="p.png" chset="null" cd="null" fn="null" cid="null" cl="p.png" ctt_s="null" ctt_t="null" text="null" data="` + strings.Repeat("QUFB", 3000) + `" ></part>
    </parts>
    <addrs>
      <addr address="+4915199999999" type="137" charset="106" />
    </addrs>
  </mms>
  <sms protocol="0" address="+4915199999999" date="1700000400000" type="1" body="danach" read="1" contact_name="Ben" />
</smses>
`
	var ns []xmlNachricht
	if err := XMLLesen(strings.NewReader(x), annaTel, func(n xmlNachricht) error { ns = append(ns, n); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 || ns[0].N.Text != "Ein Bild, das fehlt" || len(ns[0].N.Anhaenge) != 1 || len(ns[0].Daten[0]) != 9000 || ns[1].N.Text != "danach" {
		t.Fatalf("%+v", ns)
	}
	// a cut export still fails
	if err := XMLLesen(strings.NewReader(x[:len(x)-10]), annaTel, func(xmlNachricht) error { return nil }); err == nil {
		t.Fatal("a cut export was read as whole")
	}
}

func TestHalfPacketsOfAStoppedRunAreRemovedAtStart(t *testing.T) {
	post := t.TempDir()
	os.WriteFile(filepath.Join(post, ".neu-mail-1.age"), []byte("halb"), 0o600)
	os.WriteFile(filepath.Join(post, ".neu-signal-2.age"), []byte("halb"), 0o600)
	os.WriteFile(filepath.Join(post, "20261102T080000Z-mail.age"), []byte("ganz"), 0o600)
	HalbeWeg(post)
	e, _ := os.ReadDir(post)
	if len(e) != 1 || e[0].Name() != "20261102T080000Z-mail.age" {
		t.Fatalf("%v", e)
	}
}

// msgvault's SMS import refuses a part over 25 MiB and then fails the whole
// import (the owner's first Signal backup, 2026-11-02, held one video).
func TestAnAttachmentTooBigForMsgvaultIsKeptInTheVaultAndNamedInTheMessage(t *testing.T) {
	bt := neuerBriefkasten(t)
	video := bytes.Repeat([]byte("v"), MaxAnhang+1)
	gross := strings.Replace(strings.Replace(mmsMitBild(1700000200000), base64.StdEncoding.EncodeToString(bild), base64.StdEncoding.EncodeToString(video), 1), `"bild.png"`, `"urlaub.mp4"`, 2)
	bt.paket(t, signalXML(sms(1700000000000, 1, "+4917011111111", "eins"), gross, mmsMitBild(1700000300000)), tag)
	pakete := Pakete(bt.b.Post)
	if len(pakete) != 1 {
		t.Fatalf("%v", pakete)
	}

	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(bt.id.String()), 0o600)
	mv := &fakeMsgvault{}
	pb := &Postbote{Post: bt.b.Post, Schluessel: bt.sch, Protokoll: bt.b.Protokoll, Run: mv, Msgvault: "msgvault"}
	pb.Runde(context.Background(), innen)

	if len(mv.xml) != 1 {
		t.Fatalf("imports: %d", len(mv.xml))
	}
	x := mv.xml[0]
	if strings.Contains(x, base64.StdEncoding.EncodeToString(video[:3000])) {
		t.Fatal("the too-big video went to msgvault, which fails the whole import")
	}
	if !strings.Contains(x, base64.StdEncoding.EncodeToString(bild)) || !strings.Contains(x, `body="eins"`) {
		t.Fatal("the rest of the packet did not go with it")
	}
	abgelegt, _ := os.ReadDir(innen.GrosseAnhaenge())
	if len(abgelegt) != 1 || !strings.HasSuffix(abgelegt[0].Name(), "-urlaub.mp4") || !strings.HasPrefix(abgelegt[0].Name(), strings.TrimSuffix(pakete[0], ".age")) {
		t.Fatalf("kept beside the archive: %v", abgelegt)
	}
	if got, _ := os.ReadFile(filepath.Join(innen.GrosseAnhaenge(), abgelegt[0].Name())); !bytes.Equal(got, video) {
		t.Fatal("the kept video is not the video")
	}
	if !strings.Contains(x, "Anhang urlaub.mp4, 26 MB: zu groß für die Suche im Archiv. Er liegt im Tresor unter grosse-anhaenge/"+abgelegt[0].Name()) {
		t.Fatalf("the message does not say where the video is:\n%s", x[:min(len(x), 3000)])
	}
	if len(Pakete(bt.b.Post)) != 0 {
		t.Fatal("the packet stayed")
	}
	leer(t, innen.Arbeit(), "the vault's work folder")
}

// smsVomHandy is what the Haus app uploads: an SMS Backup & Restore XML,
// encrypted on the phone to the Briefkasten's public key.
func smsVomHandy(t *testing.T, dir, name string, an age.Recipient, xml string) string {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "sms"), 0o700)
	p := filepath.Join(dir, "sms", name)
	f, _ := os.Create(p)
	w, err := age.Encrypt(f, an)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, xml)
	w.Close()
	f.Close()
	return p
}

func TestThePhonesSMSAndCallsComeInEncryptedAndAreImportedWhenOpen(t *testing.T) {
	bt := neuerBriefkasten(t)
	bt.runde()
	pub, err := os.ReadFile(filepath.Join(bt.inbox, ".briefkasten.pub"))
	if err != nil || strings.TrimSpace(string(pub)) != bt.id.Recipient().String() {
		t.Fatalf("the public key is not in the inbox for the Haus app: %q %v", pub, err)
	}
	smsDatei := smsVomHandy(t, bt.inbox, "sms-20261102T120000Z.xml.age", bt.id.Recipient(), signalXML(sms(1700000000000, 1, "+4917011111111", "vom Handy")))
	anrufe := smsVomHandy(t, bt.inbox, "anrufe-20261102T120000Z.xml.age", bt.id.Recipient(), `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><calls count="1"><call number="+4917011111111" duration="61" date="1700000000000" type="1" presentation="1" contact_name="Ben" /></calls>`)
	fremd, _ := age.GenerateX25519Identity()
	kaputt := smsVomHandy(t, bt.inbox, "sms-alt.xml.age", fremd.Recipient(), signalXML())
	if roh, _ := os.ReadFile(smsDatei); bytes.Contains(roh, []byte("vom Handy")) {
		t.Fatal("test setup: the file is not encrypted")
	}

	bt.b.Eingang.Ruhe = 0
	bt.b.Eingang.Stabil("sms")
	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(bt.id.String()), 0o600)
	mv := &fakeMsgvault{}
	pb := &Postbote{Post: bt.b.Post, Eingang: bt.b.Eingang, Schluessel: bt.sch, Protokoll: bt.b.Protokoll, Run: mv, Msgvault: "msgvault"}
	pb.Runde(context.Background(), innen)

	alle := strings.Join(mv.xml, "\n")
	if len(mv.xml) != 2 || !strings.Contains(alle, `body="vom Handy"`) || !strings.Contains(alle, `<call number="+4917011111111"`) {
		t.Fatalf("msgvault got: %q", mv.xml)
	}
	for _, b := range mv.befehle {
		if b.Args[0] != "import-synctech-sms" || b.Args[3] != annaTel || !strings.HasPrefix(b.Args[1], innen.Arbeit()) {
			t.Fatalf("%q", b.Args)
		}
	}
	for _, p := range []string{smsDatei, anrufe} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s stayed in the inbox after the import", p)
		}
	}
	if _, err := os.Stat(kaputt); err != nil {
		t.Fatal("a file that cannot be decrypted must stay")
	}
	if _, f, _ := bt.b.Protokoll.Kopie(); f == nil || f.Quelle != "sms" || !strings.Contains(f.Satz, "sms-alt.xml.age") {
		t.Fatalf("the person is not told about the unreadable file: %+v", f)
	}
	leer(t, innen.Arbeit(), "the vault's work folder")
	ein, _, _ := bt.b.Protokoll.Kopie()
	if _, ok := ein["sms"]; !ok {
		t.Fatal("the import is not recorded")
	}
}

// fakeWA plays wadecrypt and msgvault import-whatsapp.
type fakeWA struct {
	befehle []Befehl
	falsch  bool
}

func (f *fakeWA) Run(ctx context.Context, b Befehl) (Ausgabe, error) {
	f.befehle = append(f.befehle, b)
	if b.Pfad == "wadecrypt" {
		if f.falsch {
			return Ausgabe{Code: 1, Text: "Error: wrong key " + b.Args[0]}, nil
		}
		os.WriteFile(b.Args[2], []byte("SQLite format 3"), 0o600)
		return Ausgabe{}, nil
	}
	return Ausgabe{Text: "Import complete!\n  Messages:       5000 processed, 4711 added, 289 skipped\n"}, nil
}

func TestWhatsAppIsReadOnlyWhileOpenWithTheKeyInsideTheVault(t *testing.T) {
	inbox := t.TempDir()
	os.MkdirAll(filepath.Join(inbox, "whatsapp", "Databases"), 0o700)
	db := filepath.Join(inbox, "whatsapp", "Databases", "msgstore.db.crypt15")
	os.WriteFile(db, []byte("verschluesselt"), 0o600)
	e := &Eingang{Dir: inbox}
	e.Stabil("whatsapp")
	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	data := t.TempDir()
	sch := Schluessel{Dir: filepath.Join(data, "schluessel")}
	sch.Speichern(SchluesselTelefon, []byte(annaTel))
	run := &fakeWA{}
	pb := &Postbote{Post: filepath.Join(data, "briefkasten"), Eingang: e, Schluessel: sch, Protokoll: LadeProtokoll(filepath.Join(data, "stand.json")), Run: run, Msgvault: "msgvault"}

	pb.Runde(context.Background(), innen)
	if len(run.befehle) != 0 {
		t.Fatal("read without a key")
	}
	key := strings.Repeat("0123456789abcdef", 4)
	os.WriteFile(innen.WhatsAppSchluessel(), []byte(key), 0o600)
	run.falsch = true
	pb.Runde(context.Background(), innen)
	if _, f, _ := pb.Protokoll.Kopie(); f == nil || f.Quelle != "whatsapp" || strings.Contains(f.Satz, key) || !strings.Contains(f.Satz, "64-stelligen") {
		t.Fatalf("a wrong key is not said, or the key is shown: %+v", f)
	}
	n := len(run.befehle)
	pb.Runde(context.Background(), innen)
	if len(run.befehle) != n {
		t.Fatal("a failed database is tried again in a loop")
	}
	run.falsch = false
	pb.NeuerWASchluessel()
	pb.Runde(context.Background(), innen)
	letzte := run.befehle[len(run.befehle)-1]
	if letzte.Args[0] != "import-whatsapp" || letzte.Args[3] != annaTel || !strings.HasPrefix(letzte.Args[1], innen.Arbeit()) {
		t.Fatalf("%q", letzte.Args)
	}
	ein, f, _ := pb.Protokoll.Kopie()
	if ein["whatsapp"].Zahlen.WhatsApp != 4711 || f != nil {
		t.Fatalf("%+v %+v", ein["whatsapp"], f)
	}
	leer(t, innen.Arbeit(), "the vault's work folder")
	if _, err := os.Stat(db); err != nil {
		t.Fatal("the database left the inbox; it is also the newest backup for a new phone")
	}
	n = len(run.befehle)
	pb.Runde(context.Background(), innen)
	if len(run.befehle) != n {
		t.Fatal("the same database is imported twice")
	}
	later := time.Now().Add(time.Hour)
	os.WriteFile(db, []byte("der naechste Tag"), 0o600)
	os.Chtimes(db, later, later)
	e.Ruhe = 0
	e.Stabil("whatsapp")
	pb.Runde(context.Background(), innen)
	if len(run.befehle) == n {
		t.Fatal("the next day's database is not read")
	}
}

// 2026-11-02: a restart cut a mail import off and msgvault refused every
// later packet; the page said „beim nächsten Öffnen“ and nothing happened
// by itself. Now a failed packet is tried again while open, and the same
// failure three times in a row turns the status red (that wakes the
// Operator).
func TestAFailedPacketIsTriedAgainAndTurnsRedWhenItKeepsFailing(t *testing.T) {
	bt := neuerBriefkasten(t)
	bt.paket(t, signalXML(sms(1700000000000, 1, "+4917011111111", "eins")), tag)
	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(bt.id.String()), 0o600)
	mv := &fakeMsgvault{fehler: true}
	jetzt := tag
	pb := &Postbote{Post: bt.b.Post, Schluessel: bt.sch, Protokoll: bt.b.Protokoll, Run: mv, Msgvault: "msgvault", Now: func() time.Time { return jetzt }}
	rot := func() bool {
		s := statusBauen(Lage{Person: "anna", Stand: Stand{Zustand: Offen, Bereit: true}, Protokoll: bt.b.Protokoll, Jetzt: jetzt})
		for _, f := range s.Findings {
			if f.Severity == "rot" {
				return true
			}
		}
		return false
	}
	pb.Runde(context.Background(), innen)
	pb.Runde(context.Background(), innen)
	if len(mv.xml) != 1 {
		t.Fatalf("tried %d times at once; want 1, then a wait", len(mv.xml))
	}
	if rot() {
		t.Fatal("red after one failure: a passing cause must heal quietly")
	}
	for i := 0; i < 2; i++ {
		jetzt = jetzt.Add(Erneut + time.Minute)
		pb.Runde(context.Background(), innen)
	}
	if len(mv.xml) != 3 {
		t.Fatalf("not tried again while open: %d tries", len(mv.xml))
	}
	if !rot() {
		t.Fatal("three failures in a row and the status is not red")
	}
	mv.fehler = false
	jetzt = jetzt.Add(Erneut + time.Minute)
	pb.Runde(context.Background(), innen)
	if len(Pakete(bt.b.Post)) != 0 {
		t.Fatal("the packet did not go in once the cause was gone")
	}
	if rot() {
		t.Fatal("still red after the packet went in")
	}
}

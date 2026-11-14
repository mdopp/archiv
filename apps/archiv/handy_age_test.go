package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

// archiv#64: the Homes app encrypts WhatsApp's and Signal's backups on the
// phone to the archive's key, so Homes relays only bytes it cannot read.
// The archive unwraps them inside the vault, keeps them so in letzte/, and
// reads the backups Homes took before as they are.

// vomHandy writes inhalt into the inbox at rel, age-encrypted to an, as
// the Homes app uploads it.
func vomHandy(t *testing.T, inbox, rel string, an age.Recipient, inhalt []byte, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(inbox, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w, err := age.Encrypt(f, an)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(inhalt)
	w.Close()
	f.Close()
	os.Chtimes(p, mtime, mtime)
	return p
}

func TestAgeBackupsAreBackupsOfTheirSource(t *testing.T) {
	for _, c := range []struct {
		quelle, rel string
		ja          bool
	}{
		{"signal", "signal-2026-11-01-02-00-00.backup.age", true},
		{"signal", "signal-2026-11-01-02-00-00.backup", true},
		{"signal", "signal-2026-11-01-02-00-00.backup.age.20261101-020000", false},
		{"whatsapp", "Databases/msgstore.db.crypt15.age", true},
		{"whatsapp", "Databases/msgstore.db.crypt15.age.20261101-020000", true},
		{"whatsapp", "Media/WhatsApp Images/IMG-1.jpg.age", false},
	} {
		if got := istSicherung(c.quelle, c.rel); got != c.ja {
			t.Errorf("%s %s: %v", c.quelle, c.rel, got)
		}
	}
	for rel, ja := range map[string]bool{"Databases/msgstore.db.crypt15.age": true, "Databases/msgstore.db.crypt15.age.20261101-020000": true, "Databases/msgstore.db.crypt15": false, "signal-x.backup": false} {
		if (Datei{Rel: rel}).alsAge() != ja {
			t.Errorf("alsAge %s", rel)
		}
	}
}

func TestTheBriefkastenKeepsAgeBackupsAsTheyCame(t *testing.T) {
	bt := neuerBriefkasten(t)
	an := bt.id.Recipient()
	// before archiv#64 Homes took the database as the phone wrote it
	alt := filepath.Join(bt.inbox, "letzte", "whatsapp", "Databases", "msgstore.db.crypt15")
	os.MkdirAll(filepath.Dir(alt), 0o755)
	os.WriteFile(alt, []byte("gestern"), 0o644)
	os.Chtimes(alt, tag, tag)
	db := vomHandy(t, bt.inbox, "whatsapp/Databases/msgstore.db.crypt15.age", an, []byte("CRYPT15"), tag.Add(24*time.Hour))
	m := vomHandy(t, bt.inbox, "whatsapp/Media/WhatsApp Images/IMG-1.jpg.age", an, []byte("BILD"), tag)
	vorher, _ := os.ReadFile(m)
	bt.runde()

	a, _ := os.Stat(filepath.Join(bt.inbox, "letzte", "whatsapp", "Databases", "msgstore.db.crypt15.age"))
	b, _ := os.Stat(db)
	if a == nil || b == nil || !os.SameFile(a, b) {
		t.Fatal("the age database is not kept in letzte/ as it came")
	}
	if _, err := os.Stat(alt); !os.IsNotExist(err) {
		t.Fatal("letzte/ keeps two databases: a new phone would get the old one too")
	}
	ziel := filepath.Join(bt.inbox, "letzte", "whatsapp", "Media", "WhatsApp Images", "IMG-1.jpg.age")
	if c, err := os.ReadFile(ziel); err != nil || !bytes.Equal(c, vorher) {
		t.Fatalf("the medium was not moved as it came (encrypted twice?): %v", err)
	}
	if _, err := os.Stat(m); !os.IsNotExist(err) {
		t.Fatal("the medium stayed in the inbox")
	}
	r, err := age.Decrypt(bytes.NewReader(vorher), bt.id)
	if err != nil {
		t.Fatal(err)
	}
	if klar, _ := io.ReadAll(r); string(klar) != "BILD" {
		t.Fatalf("%q", klar)
	}
}

func TestAnAgeSignalBackupIsUnwrappedInsideTheVault(t *testing.T) {
	st := neuerSignalTest(t)
	an, _ := empfaengerLesen(filepath.Join(filepath.Dir(st.pb.Post), "briefkasten.pub"))
	vomHandy(t, st.inbox, "signal/signal-2026-10-01.backup.age", an, []byte("SIGNAL-EIGENE-VERSCHLUESSELUNG"), tag)
	var sah string
	innen := st.innen
	st.run.vorher = func(b Befehl) {
		if b.Pfad == "signalbackup-tools" {
			x, _ := os.ReadFile(b.Args[0])
			sah = string(x)
			if filepath.Dir(b.Args[0]) != innen.Arbeit() {
				t.Errorf("the unwrapped backup is outside the vault's work folder: %s", b.Args[0])
			}
		}
	}
	st.runde()
	if sah != "SIGNAL-EIGENE-VERSCHLUESSELUNG" {
		t.Fatalf("signalbackup-tools saw %q", sah)
	}
	leer(t, st.innen.Arbeit(), "the vault's work folder")
	if _, err := os.Stat(filepath.Join(st.inbox, "letzte", "signal", "signal-2026-10-01.backup.age")); err != nil {
		t.Fatalf("letzte/ does not keep the backup as it came: %v", err)
	}
	if ein, _, _ := st.pb.Protokoll.Kopie(); ein["signal"].Zahlen.Signal != 9001 {
		t.Fatalf("%+v", ein["signal"])
	}
}

func TestAnAgeBackupForAnotherKeyStaysAndIsSaid(t *testing.T) {
	st := neuerSignalTest(t)
	fremd, _ := age.GenerateX25519Identity()
	p := vomHandy(t, st.inbox, "signal/signal-2026-10-01.backup.age", fremd.Recipient(), []byte("x"), tag)
	st.runde()
	if st.run.anzahl() != 0 {
		t.Fatal("signalbackup-tools ran on a backup the archive could not unwrap")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("the backup left the inbox")
	}
	leer(t, st.innen.Arbeit(), "the vault's work folder")
	if _, f, _ := st.pb.Protokoll.Kopie(); f == nil || f.Quelle != "signal" || !strings.Contains(f.Satz, "mit dem Schlüssel deines Archivs nicht öffnen") {
		t.Fatalf("not said: %+v", f)
	}
}

func TestAnAgeWhatsAppDatabaseIsUnwrappedInsideTheVault(t *testing.T) {
	bt := neuerBriefkasten(t)
	inbox := bt.inbox
	vomHandy(t, inbox, "whatsapp/Databases/msgstore.db.crypt15.age", bt.id.Recipient(), []byte("CRYPT15-BYTES"), time.Now().Add(-time.Hour))
	e := &Eingang{Dir: inbox}
	e.Stabil("whatsapp")
	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(bt.id.String()), 0o600)
	os.WriteFile(innen.WhatsAppSchluessel(), []byte(strings.Repeat("0123456789abcdef", 4)), 0o600)
	var sah string
	run := &fakeWA{vorher: func(b Befehl) {
		if b.Pfad == "wadecrypt" {
			x, _ := os.ReadFile(b.Args[1])
			sah = string(x)
			if filepath.Dir(b.Args[1]) != innen.Arbeit() {
				t.Errorf("the unwrapped database is outside the vault's work folder: %s", b.Args[1])
			}
		}
	}}
	pb := &Postbote{Post: bt.b.Post, Eingang: e, Schluessel: bt.sch, Protokoll: bt.b.Protokoll, Run: run, Msgvault: "msgvault"}
	pb.Runde(context.Background(), innen)
	if sah != "CRYPT15-BYTES" {
		t.Fatalf("wadecrypt saw %q", sah)
	}
	leer(t, innen.Arbeit(), "the vault's work folder")
	if ein, f, _ := pb.Protokoll.Kopie(); ein["whatsapp"].Zahlen.WhatsApp != 4711 || f != nil {
		t.Fatalf("%+v %+v", ein["whatsapp"], f)
	}
}

func TestTheSourcesPageSaysTheBackupsComeEncryptedForTheArchive(t *testing.T) {
	wt := neuesWeb(t)
	wt.oeffnen(t)
	an, _ := empfaengerLesen(wt.tt.Empfaenger)
	vomHandy(t, wt.inbox, "signal/signal-2026-11-01.backup.age", an, []byte("x"), tag)
	vomHandy(t, wt.inbox, "whatsapp/Databases/msgstore.db.crypt15.age", an, []byte("x"), tag)
	body := wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String()
	for _, want := range []string{"1 Sicherung wartet im Eingang, verschlüsselt für dein Archiv.", "1 Sicherung liegt im Eingang, verschlüsselt für dein Archiv.", "kann sie nicht lesen"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Quellen lacks %q:\n%s", want, body)
		}
	}
}

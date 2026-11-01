package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- the inbox

func legen(t *testing.T, dir, rel string, groesse int, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(strings.Repeat("x", groesse)), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mtime, mtime)
	return p
}

var tag = time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)

func TestEingangIgnoresDotEntriesAndOtherFiles(t *testing.T) {
	dir := t.TempDir()
	legen(t, dir, "signal/signal-2026-10-01-03-00-00.backup", 10, tag)
	legen(t, dir, ".teil/0123", 10, tag)                                // Haus' partials
	legen(t, dir, "signal/.signal-2026-10-02-03-00-00.backup", 10, tag) // hidden
	legen(t, dir, "signal/.zwischen/signal-2026-10-03-03-00-00.backup", 10, tag)
	legen(t, dir, "signal/notizen.txt", 10, tag)
	legen(t, dir, "whatsapp/Databases/msgstore.db.crypt15", 10, tag)
	legen(t, dir, "whatsapp/Databases/msgstore.db.crypt15.20261001-030000", 10, tag)
	legen(t, dir, "whatsapp/Media/WhatsApp Images/IMG-1.jpg", 10, tag)
	os.Symlink("/etc/passwd", filepath.Join(dir, "signal", "signal-link.backup"))
	e := &Eingang{Dir: dir}
	l, err := e.Liste("signal")
	if err != nil || len(l) != 1 || l[0].Rel != "signal-2026-10-01-03-00-00.backup" {
		t.Fatalf("signal: %v %+v", err, l)
	}
	w := wartende(e)
	if w["signal"].Wartend != 1 || w["discord"].Wartend != 0 {
		t.Fatalf("wartend: %+v", w)
	}
}

func TestWhatsappBackupsCountedInDatabases(t *testing.T) {
	dir := t.TempDir()
	legen(t, dir, "whatsapp/Databases/msgstore.db.crypt15", 10, tag)
	legen(t, dir, "whatsapp/Databases/msgstore.db.crypt15.20261001-030000", 10, tag)
	legen(t, dir, "whatsapp/Media/WhatsApp Images/IMG-1.jpg", 10, tag)
	if n := wartende(&Eingang{Dir: dir})["whatsapp"].Wartend; n != 2 {
		t.Fatalf("whatsapp backups: %d, want the database and its kept copy", n)
	}
}

func TestEingangOldestFirst(t *testing.T) {
	dir := t.TempDir()
	legen(t, dir, "signal/signal-b.backup", 1, tag.Add(48*time.Hour))
	legen(t, dir, "signal/signal-a.backup", 1, tag.Add(72*time.Hour)) // newer, though its name sorts first
	legen(t, dir, "signal/alt/signal-c.backup", 1, tag)
	l, _ := (&Eingang{Dir: dir}).Liste("signal")
	var got []string
	for _, d := range l {
		got = append(got, d.Rel)
	}
	if strings.Join(got, " ") != "alt/signal-c.backup signal-b.backup signal-a.backup" {
		t.Fatalf("order: %v", got)
	}
}

func TestEingangWaitsUntilAFileIsStable(t *testing.T) {
	dir := t.TempDir()
	u := &uhr{t: tag}
	e := &Eingang{Dir: dir, Ruhe: time.Minute, Now: u.Now}
	p := legen(t, dir, "signal/signal-1.backup", 10, tag)
	if s, _ := e.Stabil("signal"); len(s) != 0 {
		t.Fatal("stable at first sight")
	}
	u.vor(59 * time.Second)
	if s, _ := e.Stabil("signal"); len(s) != 0 {
		t.Fatal("stable before a minute")
	}
	os.WriteFile(p, []byte(strings.Repeat("x", 20)), 0o644) // still growing
	os.Chtimes(p, tag, tag)
	u.vor(30 * time.Second)
	if s, _ := e.Stabil("signal"); len(s) != 0 {
		t.Fatal("a file that changed counted as stable")
	}
	u.vor(59 * time.Second)
	if s, _ := e.Stabil("signal"); len(s) != 0 {
		t.Fatal("stable 59 s after its last change")
	}
	u.vor(time.Second)
	if s, _ := e.Stabil("signal"); len(s) != 1 {
		t.Fatal("not stable a minute after its last change")
	}
}

// --- the import

// fakeLauf plays signalbackup-tools and msgvault.
type fakeLauf struct {
	mu        sync.Mutex
	befehle   []Befehl
	export    func(b Befehl) (Ausgabe, error) // default: writes the XML
	importErr bool
	waehrend  func() // called inside the export, e.g. to cancel
}

func (f *fakeLauf) Run(ctx context.Context, b Befehl) (Ausgabe, error) {
	f.mu.Lock()
	f.befehle = append(f.befehle, b)
	f.mu.Unlock()
	if b.Pfad == "signalbackup-tools" {
		if f.waehrend != nil {
			f.waehrend()
			if ctx.Err() != nil {
				os.WriteFile(b.Args[2], []byte("<smses>halb"), 0o600)
				return Ausgabe{Code: -1}, ctx.Err()
			}
		}
		if f.export != nil {
			return f.export(b)
		}
		os.WriteFile(b.Args[2], []byte("<smses>klartext</smses>"), 0o600)
		return Ausgabe{Text: "Exporting backup to '" + b.Args[2] + "'"}, nil
	}
	if f.importErr {
		return Ausgabe{Code: 1, Text: "Error: database is locked"}, nil
	}
	return Ausgabe{Text: "Imported 3 SMS, 1 MMS, 2 calls, 4 attachments from 1 files"}, nil
}

func (f *fakeLauf) exporte() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, b := range f.befehle {
		if b.Pfad == "signalbackup-tools" {
			out = append(out, filepath.Base(b.Args[0]))
		}
	}
	return out
}

type importTest struct {
	im     *Importeur
	lauf   *fakeLauf
	innen  Innen
	inbox  string
	prot   *Protokoll
	geheim []byte
}

func neuerImport(t *testing.T, mitSchluessel bool) *importTest {
	t.Helper()
	inbox, data := t.TempDir(), t.TempDir()
	innen := Innen{Wurzel: t.TempDir()}
	if _, err := einrichten(innen, 18890); err != nil {
		t.Fatal(err)
	}
	if mitSchluessel {
		sch := Schluessel{innen.Schluessel()}
		sch.Speichern(SchluesselSignal, []byte(passphrase))
		sch.Speichern(SchluesselTelefon, []byte("+4915112345678"))
	}
	f := &fakeLauf{}
	prot := LadeProtokoll(filepath.Join(data, "archiv-stand.json"))
	im := &Importeur{Eingang: &Eingang{Dir: inbox}, Protokoll: prot, Run: f, SignalBin: "signalbackup-tools", Msgvault: "msgvault",
		Now: func() time.Time { return tag.Add(240 * time.Hour) }}
	return &importTest{im: im, lauf: f, innen: innen, inbox: inbox, prot: prot}
}

// runde scans twice (a file is stable from its second sight with Ruhe 0).
func (it *importTest) runde(ctx context.Context) {
	it.im.Eingang.Stabil("signal")
	it.im.Runde(ctx, it.innen)
}

func arbeitLeer(t *testing.T, innen Innen) {
	t.Helper()
	if e, _ := os.ReadDir(innen.Arbeit()); len(e) != 0 {
		t.Fatalf("plaintext left in the vault's work folder: %v", e[0].Name())
	}
}

func TestImportOldestFirstThenRemovedFromTheInbox(t *testing.T) {
	it := neuerImport(t, true)
	neu := legen(t, it.inbox, "signal/signal-2026-10-05.backup", 5, tag.Add(96*time.Hour))
	alt := legen(t, it.inbox, "signal/signal-2026-10-01.backup", 5, tag)
	it.runde(context.Background())
	if got := strings.Join(it.lauf.exporte(), " "); got != "signal-2026-10-01.backup signal-2026-10-05.backup" {
		t.Fatalf("order: %s", got)
	}
	for _, p := range []string{neu, alt} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still in the inbox", p)
		}
	}
	arbeitLeer(t, it.innen)
	ein, fehler, _ := it.prot.Kopie()
	s := ein["signal"]
	if fehler != nil || s.Datei != "signal-2026-10-05.backup" || s.Zahlen != (Zahlen{SMS: 3, MMS: 1, Anrufe: 2, Anhaenge: 4}) || s.Anzahl != 2 {
		t.Fatalf("protokoll: %+v %+v", s, fehler)
	}
	// the import went to the XML in the vault's work folder, with the person's number
	b := it.lauf.befehle[1]
	if b.Args[0] != "import-synctech-sms" || !strings.HasPrefix(b.Args[1], it.innen.Arbeit()) || b.Args[3] != "+4915112345678" {
		t.Fatalf("import: %q", b.Args)
	}
}

// Additive only: nothing the importer runs can delete from the archive.
func TestImportRunsNothingThatDeletes(t *testing.T) {
	it := neuerImport(t, true)
	legen(t, it.inbox, "signal/signal-1.backup", 5, tag)
	legen(t, it.inbox, "signal/signal-2.backup", 5, tag.Add(time.Hour))
	it.runde(context.Background())
	if len(it.lauf.befehle) != 4 {
		t.Fatalf("%d commands", len(it.lauf.befehle))
	}
	for _, b := range it.lauf.befehle {
		if b.Pfad == "msgvault" && b.Args[0] != "import-synctech-sms" {
			t.Fatalf("msgvault %s: the importer runs only import-synctech-sms", b.Args[0])
		}
		for _, a := range b.Args {
			l := strings.ToLower(a)
			for _, boese := range []string{"delete", "gc", "prune", "purge", "remove", "--full", "mirror"} {
				if l == boese || strings.HasPrefix(l, boese) || strings.HasPrefix(l, "--"+boese) {
					t.Fatalf("%s %q carries %q", b.Pfad, b.Args, boese)
				}
			}
		}
	}
}

func TestFailedImportKeepsTheBackupAndDeletesThePlaintext(t *testing.T) {
	it := neuerImport(t, true)
	p := legen(t, it.inbox, "signal/signal-1.backup", 5, tag)
	it.lauf.importErr = true
	it.runde(context.Background())
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the backup left the inbox after a failed import: %v", err)
	}
	arbeitLeer(t, it.innen)
	_, fehler, _ := it.prot.Kopie()
	if fehler == nil || !strings.Contains(fehler.Satz, "nicht übernehmen") || fehler.Datei != "signal-1.backup" {
		t.Fatalf("fehler: %+v", fehler)
	}
	n := len(it.lauf.befehle)
	it.runde(context.Background())
	if len(it.lauf.befehle) != n {
		t.Fatal("a failed backup was tried again in the same opening")
	}
}

func TestFailedExportDeletesItsPartialPlaintext(t *testing.T) {
	it := neuerImport(t, true)
	legen(t, it.inbox, "signal/signal-1.backup", 5, tag)
	it.lauf.export = func(b Befehl) (Ausgabe, error) {
		os.WriteFile(b.Args[2], []byte("<smses>halb fertig"), 0o600)
		return Ausgabe{Code: 1, Text: "[Error]: Failed to get phone number for recipient: 12"}, nil
	}
	it.runde(context.Background())
	arbeitLeer(t, it.innen)
	_, fehler, _ := it.prot.Kopie()
	if fehler == nil || !strings.Contains(fehler.Satz, "ließ sich nicht lesen") {
		t.Fatalf("fehler: %+v", fehler)
	}
}

func TestWrongSignalPassphraseIsSaidInGerman(t *testing.T) {
	it := neuerImport(t, true)
	p := legen(t, it.inbox, "signal/signal-1.backup", 5, tag)
	legen(t, it.inbox, "signal/signal-2.backup", 5, tag.Add(time.Hour))
	falsch := true
	it.lauf.export = func(b Befehl) (Ausgabe, error) {
		if falsch {
			return Ausgabe{Code: 1, Text: "[Warning]: Bad MAC in frame: theirMac: 00\n *** NOTE : IT IS LIKELY AN INCORRECT PASSPHRASE WAS PROVIDED ***\n[Error]: Failed to get valid frame from decoded data..."}, nil
		}
		os.WriteFile(b.Args[2], []byte("<smses/>"), 0o600)
		return Ausgabe{}, nil
	}
	it.runde(context.Background())
	_, fehler, _ := it.prot.Kopie()
	if fehler == nil || fehler.Satz != "Die Signal-Passphrase passt nicht zu dieser Sicherung. Gib sie neu ein." {
		t.Fatalf("fehler: %+v", fehler)
	}
	if got := it.lauf.exporte(); len(got) != 1 {
		t.Fatalf("after a wrong passphrase the older backups were tried with it too: %v", got)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("the backup left the inbox")
	}
	it.runde(context.Background())
	if got := it.lauf.exporte(); len(got) != 1 {
		t.Fatal("the same wrong passphrase was tried again")
	}
	// the person enters the new passphrase: both go in, the error is gone
	falsch = false
	it.im.NeuerSchluessel()
	it.runde(context.Background())
	if got := it.lauf.exporte(); len(got) != 3 {
		t.Fatalf("after the new key: %v", got)
	}
	if _, fehler, _ := it.prot.Kopie(); fehler != nil {
		t.Fatalf("error kept after a good import: %+v", fehler)
	}
}

func TestNoImportWithoutTheKeys(t *testing.T) {
	it := neuerImport(t, false)
	legen(t, it.inbox, "signal/signal-1.backup", 5, tag)
	it.runde(context.Background())
	if len(it.lauf.befehle) != 0 || !it.im.FehlenSchluessel() {
		t.Fatalf("ran %d commands; fehlt=%v", len(it.lauf.befehle), it.im.FehlenSchluessel())
	}
}

func TestClosingDuringAnImportLeavesNoPlaintextAndNoError(t *testing.T) {
	it := neuerImport(t, true)
	p := legen(t, it.inbox, "signal/signal-1.backup", 5, tag)
	ctx, cancel := context.WithCancel(context.Background())
	it.lauf.waehrend = cancel
	it.runde(ctx)
	arbeitLeer(t, it.innen)
	if _, err := os.Stat(p); err != nil {
		t.Fatal("the backup left the inbox though the import was cut off")
	}
	if _, fehler, _ := it.prot.Kopie(); fehler != nil {
		t.Fatalf("closing is not an error: %+v", fehler)
	}
}

func TestFalschePassphraseErkennung(t *testing.T) {
	for text, want := range map[string]bool{
		" *** NOTE : IT IS LIKELY AN INCORRECT PASSPHRASE WAS PROVIDED ***": true,
		"[Error]: Failed to get backupkey from passphrase":                  true,
		"[Error]: Failed to open file":                                      false,
	} {
		if falschePassphrase(text) != want {
			t.Errorf("%q: want %v", text, want)
		}
	}
}

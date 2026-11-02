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

// --- Signal, vault open: signalbackup-tools into the vault's work folder,
// then msgvault's import-signal

// fakeSignalImport plays signalbackup-tools --output and msgvault
// import-signal, and notes what each saw of the folder between them.
type fakeSignalImport struct {
	mu        sync.Mutex
	befehle   []Befehl
	falsch    bool   // the passphrase does not fit
	entpacken string // "" or "kaputt": decrypting fails after writing half
	importErr string // import-signal fails with this output
	ordner    []string
	gang      []Gang // what the page said while import-signal reported progress
	pb        *Postbote
	cancel    func() // called during the decrypt (the vault closes)
	gross     int64  // the size of one attachment file
}

func (f *fakeSignalImport) Run(ctx context.Context, b Befehl) (Ausgabe, error) {
	f.mu.Lock()
	f.befehle = append(f.befehle, b)
	f.mu.Unlock()
	switch b.Args[0] {
	case "import-signal":
		dir := b.Args[1]
		e, _ := os.ReadDir(dir)
		var namen []string
		for _, x := range e {
			namen = append(namen, x.Name())
		}
		f.ordner = append(f.ordner, strings.Join(namen, " "))
		if b.Unterwegs != nil {
			b.Unterwegs("Processed: 5000 | Added: 4000 | Skipped: 0 | Rate: 900/s | Elapsed: 5s")
			f.gang = append(f.gang, f.pb.Gang())
		}
		if f.importErr != "" {
			return Ausgabe{Code: 1, Text: f.importErr}, nil
		}
		return Ausgabe{Text: "Importing Signal messages from " + dir + "/database.sqlite\n\r  Processed: 12000 | Added: 9000    \n\nImport complete!\n  Duration:       9s\n  Chats:          40 imported, 2 skipped\n  Messages:       12000 processed, 9001 added, 3 updated, 996 skipped\n"}, nil
	default: // signalbackup-tools <backup> --output <dir>/ --no-showprogress
		if f.falsch {
			return Ausgabe{Code: 1, Text: "[Error]: Failed to get backupkey from passphrase\n *** NOTE : IT IS LIKELY AN INCORRECT PASSPHRASE WAS PROVIDED ***"}, nil
		}
		dir := b.Args[2]
		if !strings.HasSuffix(dir, "/") {
			return Ausgabe{Code: 1, Text: "would ask for an output passphrase"}, nil
		}
		if e, err := os.ReadDir(dir); err != nil || len(e) != 0 {
			return Ausgabe{Code: 1, Text: "[Error]: Output directory not empty or missing"}, nil
		}
		os.WriteFile(filepath.Join(dir, "database.sqlite"), []byte("SQLite format 3 KLARTEXT"), 0o600)
		os.WriteFile(filepath.Join(dir, "Attachment_7_1.bin"), []byte("BILD"), 0o600)
		if f.gross > 0 {
			os.WriteFile(filepath.Join(dir, "Attachment_8_2.bin"), make([]byte, f.gross), 0o600)
		}
		if f.cancel != nil {
			f.cancel()
			return Ausgabe{Code: -1}, context.Canceled
		}
		if f.entpacken == "kaputt" {
			return Ausgabe{Code: 1, Text: "[Error]: Failed to read frame (bad MAC)"}, nil
		}
		return Ausgabe{Text: "Exporting backup into '" + dir + "'"}, nil
	}
}

func (f *fakeSignalImport) anzahl() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.befehle) }

type sigTest struct {
	pb    *Postbote
	run   *fakeSignalImport
	innen Innen
	inbox string
	jetzt time.Time
}

func neuerSignalTest(t *testing.T) *sigTest {
	t.Helper()
	bt := neuerBriefkasten(t)
	bt.b.Eingang.Ruhe = 0
	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(bt.id.String()), 0o600)
	st := &sigTest{run: &fakeSignalImport{}, innen: innen, inbox: bt.inbox, jetzt: tag.Add(240 * time.Hour)}
	st.pb = &Postbote{Post: bt.b.Post, Eingang: bt.b.Eingang, Letzte: bt.b.Letzte, Schluessel: bt.sch, Protokoll: bt.b.Protokoll,
		Run: st.run, Msgvault: "msgvault", SignalBin: "signalbackup-tools", Now: func() time.Time { return st.jetzt }}
	st.run.pb = st.pb
	return st
}

func (st *sigTest) runde() {
	st.pb.Eingang.Stabil("signal")
	st.pb.Runde(context.Background(), st.innen)
}

func TestSignalIsDecryptedInsideTheVaultAndImported(t *testing.T) {
	st := neuerSignalTest(t)
	st.pb.Eingang.Stabil("signal")
	legen(t, st.inbox, "signal/signal-2026-10-01.backup", 5, tag)
	st.runde()

	if st.run.anzahl() != 2 {
		t.Fatalf("want decrypt then import-signal, got %d commands", st.run.anzahl())
	}
	ent, imp := st.run.befehle[0], st.run.befehle[1]
	dir := strings.TrimSuffix(ent.Args[2], "/")
	if ent.Pfad != "signalbackup-tools" || ent.Args[0] != filepath.Join(st.inbox, "signal", "signal-2026-10-01.backup") || ent.Args[1] != "--output" ||
		filepath.Dir(dir) != st.innen.Arbeit() || !strings.HasPrefix(filepath.Base(dir), "signal-") {
		t.Fatalf("decrypt: %q", ent.Args)
	}
	if imp.Args[0] != "import-signal" || imp.Args[1] != dir || imp.Args[2] != "--phone" || imp.Args[3] != annaTel || imp.Pfad != "msgvault" {
		t.Fatalf("import: %q", imp.Args)
	}
	// the passphrase only on the terminal, never on argv or in the environment
	if string(ent.Terminal) != passphrase+"\n" || ent.Eingabe != nil {
		t.Fatalf("terminal %q", ent.Terminal)
	}
	for _, b := range st.run.befehle {
		if strings.Contains(strings.Join(append(append([]string{b.Pfad}, b.Args...), b.Env...), " "), passphrase) {
			t.Fatalf("the passphrase on argv/env: %q", b.Args)
		}
	}
	if st.run.ordner[0] != "Attachment_7_1.bin database.sqlite" {
		t.Fatalf("import-signal did not see the decrypted folder: %q", st.run.ordner)
	}
	leer(t, st.innen.Arbeit(), "the vault's work folder")
	if _, err := os.Stat(filepath.Join(st.inbox, "signal", "signal-2026-10-01.backup")); !os.IsNotExist(err) {
		t.Fatal("the imported backup is still in the inbox")
	}
	if _, err := os.Stat(filepath.Join(st.inbox, "letzte", "signal", "signal-2026-10-01.backup")); err != nil {
		t.Fatalf("the newest backup is not kept for a new phone: %v", err)
	}
	ein, f, _ := st.pb.Protokoll.Kopie()
	if e := ein["signal"]; e.Zahlen.Signal != 9001 || e.Zahlen.Nachrichten() != 9001 || e.Datei != "signal-2026-10-01.backup" || e.Anzahl != 1 || f != nil {
		t.Fatalf("protokoll %+v %+v", e, f)
	}
	// the page counted along while import-signal ran, and is empty after
	if g := st.run.gang; len(g) != 1 || g[0].Quelle != "signal" || g[0].Fertig != 5000 || g[0].Neu != 4000 || g[0].Was != "signal-2026-10-01.backup" {
		t.Fatalf("progress: %+v", g)
	}
	if st.pb.Gang().Was != "" || st.pb.Laeuft() != "" {
		t.Fatal("still shown as running")
	}

	// the next night: oldest first, the older kept one goes, nothing in the work folder
	legen(t, st.inbox, "signal/signal-2026-10-03.backup", 5, tag.Add(48*time.Hour))
	legen(t, st.inbox, "signal/signal-2026-10-02.backup", 5, tag.Add(24*time.Hour))
	st.runde()
	st.runde()
	var reihe []string
	for _, b := range st.run.befehle[2:] {
		if b.Args[0] != "import-signal" {
			reihe = append(reihe, filepath.Base(b.Args[0]))
		}
	}
	if strings.Join(reihe, " ") != "signal-2026-10-02.backup signal-2026-10-03.backup" {
		t.Fatalf("order %q", reihe)
	}
	if e, _ := os.ReadDir(filepath.Join(st.inbox, "letzte", "signal")); len(e) != 1 || e[0].Name() != "signal-2026-10-03.backup" {
		t.Fatalf("letzte/signal: %v", e)
	}
	leer(t, st.innen.Arbeit(), "the vault's work folder")
}

// Every way it can fail leaves no plaintext in the vault's work folder
// and the backup in the inbox; it is tried again after Erneut.
func TestSignalFailuresLeaveNoPlaintextAndKeepTheBackup(t *testing.T) {
	for _, fall := range []string{"entpacken", "import", "nummer", "abbruch"} {
		t.Run(fall, func(t *testing.T) {
			st := neuerSignalTest(t)
			st.pb.Eingang.Stabil("signal")
			p := legen(t, st.inbox, "signal/signal-1.backup", 5, tag)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			satz := ""
			switch fall {
			case "entpacken":
				st.run.entpacken = "kaputt"
				satz = "Die Signal-Sicherung signal-1.backup ließ sich nicht lesen. Sie bleibt im Eingang; sag dem Admin Bescheid."
			case "import":
				st.run.importErr = "Error: import failed: database is locked"
				satz = "Das Archiv konnte die Nachrichten aus signal-1.backup nicht übernehmen. Verloren geht nichts: es versucht es in einer halben Stunde noch einmal und bei jedem Öffnen."
			case "nummer":
				st.run.importErr = "Error: --phone does not match the number of the account that made this Signal backup"
				satz = saysSignalNummer
			case "abbruch": // the vault closes while it decrypts
				st.run.cancel = cancel
			}
			st.pb.Eingang.Stabil("signal")
			st.pb.Runde(ctx, st.innen)
			leer(t, st.innen.Arbeit(), "the vault's work folder")
			if _, err := os.Stat(p); err != nil {
				t.Fatal("the failed backup left the inbox")
			}
			if _, err := os.Stat(filepath.Join(st.inbox, "letzte", "signal")); err == nil {
				t.Fatal("a failed backup was kept as the newest")
			}
			_, f, _ := st.pb.Protokoll.Kopie()
			if fall == "abbruch" {
				if f != nil {
					t.Fatalf("closing the vault is no error: %+v", f)
				}
				return
			}
			if f == nil || f.Quelle != "signal" || f.Satz != satz {
				t.Fatalf("fehler %+v", f)
			}
			n := st.run.anzahl()
			st.runde()
			if st.run.anzahl() != n {
				t.Fatal("tried again at once")
			}
			st.run.entpacken, st.run.importErr = "", ""
			st.jetzt = st.jetzt.Add(Erneut + time.Minute)
			st.runde()
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatal("not imported once the cause was gone")
			}
			if _, f, _ := st.pb.Protokoll.Kopie(); f != nil {
				t.Fatalf("the error stayed: %+v", f)
			}
			leer(t, st.innen.Arbeit(), "the vault's work folder")
		})
	}
}

func TestSignalWrongPassphrase(t *testing.T) {
	st := neuerSignalTest(t)
	st.pb.Eingang.Stabil("signal")
	legen(t, st.inbox, "signal/signal-1.backup", 5, tag)
	legen(t, st.inbox, "signal/signal-2.backup", 5, tag.Add(time.Hour))
	st.run.falsch = true
	st.runde()
	st.jetzt = st.jetzt.Add(Erneut + time.Minute)
	st.runde()
	if st.run.anzahl() != 1 {
		t.Fatalf("the wrong passphrase was tried %d times", st.run.anzahl())
	}
	if _, f, _ := st.pb.Protokoll.Kopie(); f == nil || f.Satz != saysSignalFalsch {
		t.Fatalf("%+v", f)
	}
	s := statusBauen(Lage{Person: "anna", Stand: Stand{Zustand: Offen, Bereit: true}, Protokoll: st.pb.Protokoll, Jetzt: st.jetzt})
	if len(s.Findings) != 1 || s.Findings[0].Severity != "gelb" || !strings.Contains(s.Findings[0].Fix, "Signal-Passphrase") {
		t.Fatalf("finding %+v", s.Findings)
	}
	leer(t, st.innen.Arbeit(), "the vault's work folder")
	st.run.falsch = false
	st.pb.NeuerSignalSchluessel()
	st.runde()
	if _, f, _ := st.pb.Protokoll.Kopie(); f != nil {
		t.Fatalf("after the new key: %+v", f)
	}
	if l, _ := st.pb.Eingang.Liste("signal"); len(l) != 0 {
		t.Fatalf("not imported after the new key: %v", l)
	}
}

func TestSignalWaitsForTheKeys(t *testing.T) {
	st := neuerSignalTest(t)
	st.pb.Eingang.Stabil("signal")
	legen(t, st.inbox, "signal/signal-1.backup", 5, tag)
	os.Remove(filepath.Join(st.pb.Schluessel.Dir, SchluesselSignal))
	st.runde()
	os.Remove(filepath.Join(st.pb.Schluessel.Dir, SchluesselTelefon))
	st.pb.Schluessel.Speichern(SchluesselSignal, []byte(passphrase))
	st.runde()
	if st.run.anzahl() != 0 {
		t.Fatal("ran without the passphrase or the number")
	}
	if _, f, _ := st.pb.Protokoll.Kopie(); f != nil {
		t.Fatalf("a missing key is no error in the record: %+v", f)
	}
}

// An attachment import-signal skips for its size stays in the vault
// beside the archive, under a name the next backup lands on again.
func TestSignalAttachmentTooBigForMsgvaultIsKeptInTheVault(t *testing.T) {
	alt := SignalMaxAnhang
	SignalMaxAnhang = 10
	defer func() { SignalMaxAnhang = alt }()
	st := neuerSignalTest(t)
	st.run.gross = 11
	st.pb.Eingang.Stabil("signal")
	legen(t, st.inbox, "signal/signal-1.backup", 5, tag)
	st.runde()
	legen(t, st.inbox, "signal/signal-2.backup", 5, tag.Add(24*time.Hour))
	st.runde()
	e, _ := os.ReadDir(st.innen.GrosseAnhaenge())
	if len(e) != 1 || e[0].Name() != "signal-Attachment_8_2.bin" {
		t.Fatalf("grosse-anhaenge: %v", e)
	}
	leer(t, st.innen.Arbeit(), "the vault's work folder")
}

// The page and the status while Signal backups wait: keys missing, or the
// archive closed for days.
func TestSignalBackupsWaitingAreSaid(t *testing.T) {
	wt := neuesWeb(t)
	legen(t, wt.inbox, "signal/signal-1.backup", 5, tag)
	if !wt.w.fehlenSignalSchluessel() {
		t.Fatal("no passphrase stored, a backup waits: not said")
	}
	wt.w.Schluessel.Speichern(SchluesselSignal, []byte(passphrase))
	wt.w.Schluessel.Speichern(SchluesselTelefon, []byte(annaTel))
	if wt.w.fehlenSignalSchluessel() {
		t.Fatal("both keys stored, still said missing")
	}

	lage := Lage{Person: "anna", Stand: Stand{Zustand: Verschlossen}, Wartend: wartende(wt.w.Eingang), Protokoll: wt.w.Protokoll, URL: "https://archiv.example"}
	ankunft := *lage.Wartend["signal"].AeltesteSeit
	lage.Jetzt = ankunft.Add(48 * time.Hour)
	if s := statusBauen(lage); len(s.Findings) != 0 {
		t.Fatalf("two days: %+v", s.Findings)
	}
	lage.Jetzt = ankunft.Add(73 * time.Hour)
	s := statusBauen(lage)
	if len(s.Findings) != 1 || !strings.HasPrefix(s.Findings[0].Detail, "Dein Archiv ist zu, 1 Signal-Sicherung wartet seit") || !strings.Contains(s.Findings[0].Fix, "Öffne dein Archiv") {
		t.Fatalf("three days closed: %+v", s.Findings)
	}
	lage.Stand = Stand{Zustand: Offen, Bereit: true}
	if s := statusBauen(lage); len(s.Findings) != 0 {
		t.Fatalf("open: %+v", s.Findings)
	}

	wt.oeffnen(t)
	wt.w.Postbote.gang = Gang{Was: "signal-1.backup", Quelle: "signal", Seit: time.Now()}
	body := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if !strings.Contains(body, "Signal-Nachrichten ins Archiv übernehmen") || !strings.Contains(body, "Erst entschlüsselt die Box die Sicherung") {
		t.Fatalf("the page while it decrypts:\n%s", body)
	}
	wt.w.Postbote.gang.Fertig, wt.w.Postbote.gang.Neu = 5000, 4000
	body = wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if !strings.Contains(body, "Bisher 5.000 durchgesehen, davon 4.000 neu im Archiv.") {
		t.Fatalf("the page while it imports:\n%s", body)
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Befehl is one program call, built by a pure function below so a test
// can look at exactly what reaches argv and the environment. A secret
// (the Archiv-Passwort, the Signal passphrase) never goes there: it is
// written to the child's stdin (Eingabe) or, for a program that only
// reads a terminal, to a pseudo-terminal (Terminal).
type Befehl struct {
	Pfad     string
	Args     []string
	Env      []string // the child's whole environment, nothing inherited
	Eingabe  []byte   // secret, written to stdin, then stdin is closed
	Terminal []byte   // secret, typed into a pseudo-terminal on stdin once the program has switched echo off
	// Unterwegs, when set, gets the program's last output line every few
	// seconds while it runs: msgvault's import-whatsapp and import-signal
	// rewrite one progress line with \r ("Processed: N | Added: M …").
	Unterwegs func(zeile string)
}

// UnterwegsTakt: how often a running program's last line is looked at.
var UnterwegsTakt = 5 * time.Second

// letzteZeile is the last non-empty line of an output, \r and \n alike.
func letzteZeile(b []byte) string {
	f := strings.FieldsFunc(string(b), func(r rune) bool { return r == '\r' || r == '\n' })
	for i := len(f) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(f[i]); s != "" {
			return s
		}
	}
	return ""
}

// Grundumgebung is what every child gets: no inherited variable, so
// nothing of the supervisor's environment (PROXY_SECRET) reaches it.
func grundumgebung(home string) []string {
	env := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + home, "LANG=C.UTF-8"}
	if tz := os.Getenv("TZ"); tz != "" {
		env = append(env, "TZ="+tz)
	}
	return env
}

// msgvaultUmgebung adds what msgvault needs: its home inside the vault,
// telemetry off by every switch it honours (laib ADR 0029: it is on by default).
// When the relay to Radicale is set up (relais.go), msgvault trusts its
// certificate through SSL_CERT_FILE: the system's CAs and the relay's.
func msgvaultUmgebung(home string) []string {
	env := append(grundumgebung(home), "MSGVAULT_HOME="+home, "MSGVAULT_TELEMETRY_ENABLED=0", "TELEMETRY_ENABLED=0", "DO_NOT_TRACK=1")
	if ca := filepath.Join(home, kontakteCAName); dateiDa(ca) {
		env = append(env, "SSL_CERT_FILE="+ca)
	}
	return env
}

func dateiDa(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func zeile(geheim []byte) []byte {
	b := make([]byte, 0, len(geheim)+1)
	return append(append(b, geheim...), '\n')
}

// gocryptfsInit makes the cipher folder; the password comes on stdin
// (gocryptfs reads it from there when stdin is not a terminal).
func gocryptfsInit(bin, cipher string, pw []byte) Befehl {
	return Befehl{Pfad: bin, Args: []string{"-init", "-q", "--", cipher}, Env: grundumgebung("/"), Eingabe: zeile(pw)}
}

// gocryptfsMount mounts in the foreground: the supervisor is PID 1 and
// keeps gocryptfs as its own child instead of a daemon it would have to reap.
func gocryptfsMount(bin, cipher, mnt string, pw []byte) Befehl {
	return Befehl{Pfad: bin, Args: []string{"-fg", "-q", "-nosyslog", "--", cipher, mnt}, Env: grundumgebung("/"), Eingabe: zeile(pw)}
}

func fusermountU(bin, mnt string, lazy bool) Befehl {
	args := []string{"-u"}
	if lazy {
		args = append(args, "-z")
	}
	return Befehl{Pfad: bin, Args: append(args, mnt), Env: grundumgebung("/")}
}

// msgvaultServe runs the archive in the foreground; its config.toml (in
// home) binds it to loopback with the vault's API key.
func msgvaultServe(bin, home string) Befehl {
	return Befehl{Pfad: bin, Args: []string{"serve"}, Env: msgvaultUmgebung(home)}
}

// signalEntpacken decrypts a Signal backup into dir (database.sqlite and
// one Attachment_*.bin per attachment). dir must exist and be empty, and
// is passed with a trailing slash: given a path that is not an existing
// directory, signalbackup-tools writes an encrypted backup FILE instead and
// asks a second time, for that file's passphrase (main.cc, tag 20261014).
// The passphrase is typed into a terminal: signalbackup-tools asks for it
// when none is on the command line, and only reads a terminal (it fails on
// a pipe).
func signalEntpacken(bin, backup, dir string, passphrase []byte) Befehl {
	return Befehl{Pfad: bin, Args: []string{backup, "--output", strings.TrimSuffix(dir, "/") + "/", "--no-showprogress"},
		Env: grundumgebung("/tmp"), Terminal: zeile(passphrase)}
}

// msgvaultImportSignal imports a decrypted Signal backup (the folder
// signalEntpacken wrote) through the running msgvault, under msgvault's
// own "signal" account for the person's number. Additive: msgvault keys a
// message by its author's Signal id and sent time, a newer backup adds and
// updates, and import-signal has no option that deletes.
func msgvaultImportSignal(bin, home, dir, telefon string) Befehl {
	return Befehl{Pfad: bin, Args: []string{"import-signal", dir, "--phone", telefon}, Env: msgvaultUmgebung(home)}
}

// msgvaultImportSMS imports an SMS Backup & Restore XML (the phone's SMS
// and calls, or a packet) through the running msgvault (the CLI hands it
// to the daemon). Only import flags: import-synctech-sms
// has no option that deletes or mirrors deletions, and nothing here may
// add one — the archive is additive (laib#985).
func msgvaultImportSMS(bin, home, xml, telefon string) Befehl {
	return Befehl{Pfad: bin, Args: []string{"import-synctech-sms", xml, "--owner-phone", telefon}, Env: msgvaultUmgebung(home)}
}

// wadecrypt decrypts a WhatsApp crypt15 database with the 64-digit key
// (hex). In memory: wa-crypt-tools 0.1.0's streaming mode (-nm) writes an
// empty file (tried 2026-11-02; integration.sh holds the round trip). A
// msgstore is tens of MB, not GBs.
func wadecrypt(hexKey, von, nach string) Befehl {
	return Befehl{Pfad: "wadecrypt", Args: []string{hexKey, von, nach}, Env: []string{"PATH=/usr/local/bin:/usr/bin:/bin"}}
}

// msgvaultImportWhatsApp imports a decrypted msgstore.db. msgvault keys
// every message by its chat and id, so the daily database (the whole
// history again) adds only what is new.
func msgvaultImportWhatsApp(bin, home, db, telefon string) Befehl {
	return Befehl{Pfad: bin, Args: []string{"import-whatsapp", db, "--phone", telefon}, Env: msgvaultUmgebung(home)}
}

// msgvaultImportMaildir imports a mail packet's Maildir through the
// running msgvault under the person's mail account. Additive as above:
// import-maildir only adds (and adds folder labels to a message it has).
func msgvaultImportMaildir(bin, home, maildir, konto string) Befehl {
	// --no-resume: each packet is its own unpacked folder, so a checkpoint
	// left by an import a restart cut off names a folder that is gone and
	// msgvault refuses every later packet (2026-11-02). A packet is whole
	// and msgvault keeps one of each message, so starting fresh is safe.
	return Befehl{Pfad: bin, Args: []string{"import-maildir", maildir, "--identifier", konto, "--no-resume"}, Env: msgvaultUmgebung(home)}
}

// msgvaultAddCardDAV connects msgvault to the person's address book
// through the relay (relais.go): the relay's URL and user on argv, its
// password on stdin (add-carddav reads a piped password there), never
// Radicale's. No --schedule: the supervisor syncs while open.
func msgvaultAddCardDAV(bin, home, basis, benutzer string, pw []byte) Befehl {
	return Befehl{Pfad: bin, Args: []string{"add-carddav", basis, benutzer}, Env: msgvaultUmgebung(home), Eingabe: zeile(pw)}
}

// msgvaultBuchRollen gives the address book its roles: subscribed (its
// cards become persons) and lookup source (they resolve numbers and
// addresses to names) — never --write-target: msgvault publishes nothing
// to the person's address book (and the relay refuses any write anyway).
// set-role replaces all three roles, so the omitted one is false.
func msgvaultBuchRollen(bin, home string, buch int64) Befehl {
	return Befehl{Pfad: bin, Args: []string{"carddav", "books", "set-role", fmt.Sprint(buch), "--subscribed", "--lookup-source"}, Env: msgvaultUmgebung(home)}
}

// msgvaultSyncCardDAV pulls the address book's changes into msgvault.
func msgvaultSyncCardDAV(bin, home string) Befehl {
	return Befehl{Pfad: bin, Args: []string{"sync-carddav"}, Env: msgvaultUmgebung(home)}
}

// Ausgabe is what a finished program said: the tail of its output.
type Ausgabe struct {
	Code   int
	Text   string // stdout and stderr, at most the last 64 KiB
	Dauert time.Duration
}

// Ausfuehrer runs a Befehl to its end. The real one is exec; tests record.
type Ausfuehrer interface {
	Run(ctx context.Context, b Befehl) (Ausgabe, error)
}

type execAusfuehrer struct{}

// schwanz keeps the last n bytes written to it.
type schwanz struct {
	mu sync.Mutex
	n  int
	b  []byte
}

func (s *schwanz) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	if len(s.b) > s.n {
		s.b = append([]byte(nil), s.b[len(s.b)-s.n:]...)
	}
	return len(p), nil
}

func (s *schwanz) snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.b...)
}

func kommando(ctx context.Context, b Befehl) *exec.Cmd {
	cmd := exec.CommandContext(ctx, b.Pfad, b.Args...)
	cmd.Env = b.Env
	if cmd.Env == nil {
		cmd.Env = []string{} // never the supervisor's own
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 20 * time.Second
	return cmd
}

// Run starts the program, gives it its secret on stdin or a terminal, and
// waits. A non-zero exit is not an error here: Ausgabe.Code says it.
func (execAusfuehrer) Run(ctx context.Context, b Befehl) (Ausgabe, error) {
	start := time.Now()
	cmd := kommando(ctx, b)
	out := &schwanz{n: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	var term *terminal
	switch {
	case b.Terminal != nil:
		t, err := neuesTerminal()
		if err != nil {
			return Ausgabe{}, fmt.Errorf("terminal: %w", err)
		}
		term = t
		defer term.schliessen()
		cmd.Stdin = term.slave
	case b.Eingabe != nil:
		cmd.Stdin = bytes.NewReader(b.Eingabe)
	}
	if err := cmd.Start(); err != nil {
		return Ausgabe{}, err
	}
	if b.Unterwegs != nil {
		fertig, weg := make(chan struct{}), make(chan struct{})
		defer func() { close(fertig); <-weg }() // no line after Run returned
		go func() {
			defer close(weg)
			t := time.NewTicker(UnterwegsTakt)
			defer t.Stop()
			zuletzt := ""
			for {
				select {
				case <-fertig:
					return
				case <-t.C:
					if z := letzteZeile(out.snapshot()); z != "" && z != zuletzt {
						zuletzt = z
						b.Unterwegs(z)
					}
				}
			}
		}()
	}
	warten := make(chan error, 1)
	if term == nil {
		warten <- cmd.Wait()
	} else {
		term.slave.Close() // the child has its own copy
		term.slave = nil
		go io.Copy(io.Discard, term.master) // whatever the program writes to the terminal; ends with it
		lebt, aus := context.WithCancel(ctx)
		go func() { warten <- cmd.Wait(); aus() }()
		err := term.tippen(lebt, b.Terminal, 2*time.Minute)
		aus()
		if err != nil && lebt.Err() == nil { // it never asked, and still runs
			cmd.Process.Kill()
			<-warten
			return Ausgabe{Code: -1, Text: string(out.snapshot()), Dauert: time.Since(start)}, err
		}
	}
	err := <-warten
	a := Ausgabe{Text: string(out.snapshot()), Dauert: time.Since(start)}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		a.Code = ee.ExitCode()
		if ctx.Err() != nil {
			return a, ctx.Err()
		}
	default:
		return a, err
	}
	return a, nil
}

// schwaerzen removes every secret from a text before it reaches a log.
func schwaerzen(text string, geheim ...[]byte) string {
	for _, g := range geheim {
		s := strings.TrimSpace(string(g))
		if len(s) >= 4 {
			text = strings.ReplaceAll(text, s, "***")
		}
	}
	return text
}

// nullen overwrites a secret held in memory once it is used.
func nullen(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

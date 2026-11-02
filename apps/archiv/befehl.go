package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
func msgvaultUmgebung(home string) []string {
	return append(grundumgebung(home), "MSGVAULT_HOME="+home, "MSGVAULT_TELEMETRY_ENABLED=0", "TELEMETRY_ENABLED=0", "DO_NOT_TRACK=1")
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

// signalExport writes the backup as an SMS Backup & Restore XML into a
// named pipe (--overwrite: the pipe exists already; the tool only
// writes forward, it never seeks). Its
// passphrase is typed into a terminal: signalbackup-tools asks for it when
// none is on the command line, and only reads a terminal (it fails on a pipe).
func signalExport(bin, backup, xml, telefon string, passphrase []byte) Befehl {
	return Befehl{Pfad: bin, Args: []string{backup, "--exportxml", xml, "--overwrite", "--setselfid", telefon, "--no-showprogress"},
		Env: grundumgebung("/tmp"), Terminal: zeile(passphrase)}
}

// msgvaultImportSMS imports the XML through the running msgvault (the
// CLI hands it to the daemon). Only import flags: import-synctech-sms
// has no option that deletes or mirrors deletions, and nothing here may
// add one — the archive is additive (laib#985).
func msgvaultImportSMS(bin, home, xml, telefon string) Befehl {
	return Befehl{Pfad: bin, Args: []string{"import-synctech-sms", xml, "--owner-phone", telefon}, Env: msgvaultUmgebung(home)}
}

// msgvaultImportMaildir imports a mail packet's Maildir through the
// running msgvault under the person's mail account. Additive as above:
// import-maildir only adds (and adds folder labels to a message it has).
func msgvaultImportMaildir(bin, home, maildir, konto string) Befehl {
	return Befehl{Pfad: bin, Args: []string{"import-maildir", maildir, "--identifier", konto}, Env: msgvaultUmgebung(home)}
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

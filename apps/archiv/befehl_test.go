package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// TestMain doubles as the child of the exec tests: with ARCHIV_HELFER set
// the test binary plays a program and reports what it got.
func TestMain(m *testing.M) {
	switch os.Getenv("ARCHIV_HELFER") {
	case "":
		os.Exit(m.Run())
	case "stdin": // like gocryptfs: argv, env, and the line on stdin
		zeile, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Printf("ARGV=%q\nENV=%q\nSHA=%s\n", os.Args, os.Environ(), sha(strings.TrimSuffix(zeile, "\n")))
		os.Exit(0)
	case "terminal": // like signalbackup-tools: needs a terminal, switches echo off, reads a line
		var tio syscall.Termios
		if err := ioctl(0, syscall.TCGETS, uintptr(unsafe.Pointer(&tio))); err != nil {
			fmt.Println("Failed to set passphrase") // what signalbackup-tools says on a pipe
			os.Exit(1)
		}
		time.Sleep(100 * time.Millisecond) // a slow start: the supervisor must wait for the question
		tio.Lflag &^= syscall.ECHO | syscall.ICANON
		ioctl(0, syscall.TCSETS, uintptr(unsafe.Pointer(&tio)))
		fmt.Print("Please provide passphrase for input file 'x': ")
		var b []byte
		one := make([]byte, 1)
		for {
			if _, err := os.Stdin.Read(one); err != nil || one[0] == '\n' {
				break
			}
			b = append(b, one[0])
			fmt.Print("*")
		}
		fmt.Printf("\nARGV=%q\nENV=%q\nSHA=%s\n", os.Args, os.Environ(), sha(string(b)))
		os.Exit(0)
	case "frueh": // a program that ends before it asks (a missing file)
		fmt.Println("[Error]: file not found")
		os.Exit(1)
	}
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

const geheim = "Mein-Archiv-Passwort-1234"
const passphrase = "123451234512345123451234512345"

// Every builder that handles a secret keeps it out of argv and the
// environment; it reaches the program only by stdin or the terminal.
func TestNoSecretInArgvOrEnv(t *testing.T) {
	t.Setenv("PROXY_SECRET", "caddys-geheimnis")
	t.Setenv("ARCHIV_PASSWORT", geheim) // even if someone put it there, a child does not inherit it
	pw, pp := []byte(geheim), []byte(passphrase)
	for name, b := range map[string]Befehl{
		"gocryptfs -init": gocryptfsInit("gocryptfs", "/data/tresor", pw),
		"gocryptfs mount": gocryptfsMount("gocryptfs", "/data/tresor", "/run/tresor", pw),
		"fusermount":      fusermountU("fusermount3", "/run/tresor", false),
		"msgvault serve":  msgvaultServe("msgvault", "/run/tresor/msgvault"),
		"signal decrypt":  signalEntpacken("signalbackup-tools", "/eingang/signal/signal-1.backup", "/run/tresor/arbeit/signal-x", pp),
		"import-signal":   msgvaultImportSignal("msgvault", "/run/tresor/msgvault", "/run/tresor/arbeit/signal-x", "+4915112345678"),
		"msgvault import": msgvaultImportSMS("msgvault", "/run/tresor/msgvault", "/run/tresor/arbeit/x.xml", "+4915112345678"),
	} {
		alles := strings.Join(append(append([]string{b.Pfad}, b.Args...), b.Env...), "\x00")
		for _, s := range []string{geheim, passphrase, "caddys-geheimnis"} {
			if strings.Contains(alles, s) {
				t.Errorf("%s: %q in argv/env: %q", name, s, alles)
			}
		}
		for _, e := range b.Env {
			if strings.HasPrefix(e, "PROXY_SECRET=") || strings.HasPrefix(e, "ARCHIV_") {
				t.Errorf("%s: inherited %s", name, e)
			}
		}
	}
	if b := gocryptfsMount("g", "c", "m", pw); string(b.Eingabe) != geheim+"\n" || b.Terminal != nil {
		t.Errorf("mount: the password goes on stdin, got %q", b.Eingabe)
	}
	if b := signalEntpacken("s", "b", "x", pp); string(b.Terminal) != passphrase+"\n" || b.Eingabe != nil {
		t.Errorf("signal: the passphrase is typed into a terminal, got %q", b.Terminal)
	}
}

// msgvault runs with telemetry off by every switch it honours and its
// home inside the vault.
func TestMsgvaultEnvironment(t *testing.T) {
	for _, b := range []Befehl{msgvaultServe("msgvault", "/run/tresor/msgvault"), msgvaultImportSMS("msgvault", "/run/tresor/msgvault", "x", "+49"),
		msgvaultImportSignal("msgvault", "/run/tresor/msgvault", "x", "+49")} {
		env := strings.Join(b.Env, " ")
		for _, want := range []string{"MSGVAULT_HOME=/run/tresor/msgvault", "MSGVAULT_TELEMETRY_ENABLED=0", "DO_NOT_TRACK=1", "HOME=/run/tresor/msgvault"} {
			if !strings.Contains(env, want) {
				t.Errorf("%v: lacks %s", b.Args, want)
			}
		}
	}
}

// The import is additive: exactly import-synctech-sms with the owner, no
// other flag (msgvault has none that mirrors deletions; none may sneak in).
func TestImportCommandIsAdditiveOnly(t *testing.T) {
	b := msgvaultImportSMS("msgvault", "/h", "/x.xml", "+4915112345678")
	want := []string{"import-synctech-sms", "/x.xml", "--owner-phone", "+4915112345678"}
	if strings.Join(b.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("args %q, want %q", b.Args, want)
	}
}

// Signal: signalbackup-tools decrypts into a folder, import-signal reads
// it. The folder goes with a trailing slash: without one, a path that is
// not an existing folder makes signalbackup-tools write an encrypted
// backup file and ask for a second passphrase nobody types. Only import
// flags, as for SMS.
func TestSignalCommands(t *testing.T) {
	for _, dir := range []string{"/run/tresor/arbeit/signal-1", "/run/tresor/arbeit/signal-1/"} {
		b := signalEntpacken("signalbackup-tools", "/eingang/signal/signal-1.backup", dir, []byte(passphrase))
		want := []string{"/eingang/signal/signal-1.backup", "--output", "/run/tresor/arbeit/signal-1/", "--no-showprogress"}
		if strings.Join(b.Args, " ") != strings.Join(want, " ") {
			t.Fatalf("decrypt args %q, want %q", b.Args, want)
		}
	}
	b := msgvaultImportSignal("msgvault", "/h", "/run/tresor/arbeit/signal-1", "+4915112345678")
	want := []string{"import-signal", "/run/tresor/arbeit/signal-1", "--phone", "+4915112345678"}
	if strings.Join(b.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("import args %q, want %q", b.Args, want)
	}
}

func helfer(t *testing.T, art string, b Befehl) Befehl {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b.Pfad = exe
	b.Env = append(b.Env, "ARCHIV_HELFER="+art)
	return b
}

// The real exec path: the secret reaches the program on stdin and is in
// neither its argv nor its environment, as the program itself sees them
// (what /proc/<pid>/cmdline and environ show).
func TestExecFeedsTheSecretOnStdin(t *testing.T) {
	t.Setenv("PROXY_SECRET", "caddys-geheimnis")
	b := helfer(t, "stdin", gocryptfsInit("x", "/data/tresor", []byte(geheim)))
	a, err := execAusfuehrer{}.Run(context.Background(), b)
	if err != nil || a.Code != 0 {
		t.Fatalf("%v %d %s", err, a.Code, a.Text)
	}
	if !strings.Contains(a.Text, "SHA="+sha(geheim)) {
		t.Fatalf("the program did not get the password on stdin:\n%s", a.Text)
	}
	for _, s := range []string{geheim, "caddys-geheimnis"} {
		if strings.Contains(a.Text, s) {
			t.Fatalf("%q visible to the program in argv/env:\n%s", s, a.Text)
		}
	}
}

// A program that reads only a terminal (signalbackup-tools) gets the
// passphrase typed in once it has switched echo off; it never shows.
func TestExecTypesThePassphraseIntoATerminal(t *testing.T) {
	b := helfer(t, "terminal", signalEntpacken("x", "/eingang/signal/signal-1.backup", "/tmp/signal-x", []byte(passphrase)))
	a, err := execAusfuehrer{}.Run(context.Background(), b)
	if err != nil || a.Code != 0 {
		t.Fatalf("%v %d %s", err, a.Code, a.Text)
	}
	if !strings.Contains(a.Text, "SHA="+sha(passphrase)) {
		t.Fatalf("the program did not read the passphrase from its terminal:\n%s", a.Text)
	}
	if strings.Contains(a.Text, passphrase) {
		t.Fatalf("the passphrase is visible:\n%s", a.Text)
	}
	// a pipe instead of the terminal is what signalbackup-tools refuses: the helper does too
	b.Terminal, b.Eingabe = nil, []byte(passphrase+"\n")
	if a, _ := (execAusfuehrer{}).Run(context.Background(), b); a.Code == 0 {
		t.Fatalf("the helper accepted a pipe, so the terminal test proves nothing: %s", a.Text)
	}
}

// A program that ends before it asks does not hold the import for the
// two minutes of waiting for its question.
func TestExecDoesNotWaitForAQuestionThatNeverComes(t *testing.T) {
	b := helfer(t, "frueh", signalEntpacken("x", "/fehlt.backup", "/tmp/signal-x", []byte(passphrase)))
	start := time.Now()
	a, err := execAusfuehrer{}.Run(context.Background(), b)
	if err != nil || a.Code != 1 || !strings.Contains(a.Text, "file not found") {
		t.Fatalf("%v %d %q", err, a.Code, a.Text)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("waited %v for a program that had ended", d)
	}
}

func TestSchwaerzen(t *testing.T) {
	got := schwaerzen("gocryptfs: password "+geheim+" wrong; again "+geheim, []byte(geheim+"\n"))
	if strings.Contains(got, geheim) || !strings.Contains(got, "***") {
		t.Fatalf("%q", got)
	}
}

func TestMountinfoEscapes(t *testing.T) {
	if got := entschluesseln(`/run/mein\040tresor`); got != "/run/mein tresor" {
		t.Fatalf("%q", got)
	}
}

// A running import says how far it is: msgvault's import-whatsapp rewrites
// one line with \r; the runner hands its last line on while it runs
// (owner, 2026-11-02: „Wie weit es ist, meldet das Archiv erst am Ende …
// und seitdem keine Updates“).
func TestARunningProgramsLastLineIsHandedOn(t *testing.T) {
	alt := UnterwegsTakt
	UnterwegsTakt = 50 * time.Millisecond
	defer func() { UnterwegsTakt = alt }()
	var mu sync.Mutex
	var gesehen []string
	b := Befehl{Pfad: "/bin/sh", Args: []string{"-c", `printf '\r  Processed: 1000 | Added: 10 | Rate: 3/s    '; sleep 0.4; printf '\r  Processed: 2000 | Added: 20 | Rate: 3/s    '; sleep 0.4; printf '\nMessages: 2000 processed, 20 added\n'`},
		Unterwegs: func(z string) { mu.Lock(); gesehen = append(gesehen, z); mu.Unlock() }}
	a, err := execAusfuehrer{}.Run(context.Background(), b)
	if err != nil || a.Code != 0 {
		t.Fatalf("%v %+v", err, a)
	}
	mu.Lock()
	defer mu.Unlock()
	alle := strings.Join(gesehen, "\n")
	if !strings.Contains(alle, "Processed: 1000 | Added: 10") || !strings.Contains(alle, "Processed: 2000 | Added: 20") {
		t.Fatalf("the progress lines did not come while it ran: %q", gesehen)
	}
	for i := 1; i < len(gesehen); i++ {
		if gesehen[i] == gesehen[i-1] {
			t.Fatalf("the same line twice: %q", gesehen)
		}
	}
}

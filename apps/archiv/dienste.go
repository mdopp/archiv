package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// gocryptfs exit codes (internal/exitcodes): 12 is a wrong password.
const gocryptfsFalschesPasswort = 12

// Gocryptfs is the real Mounter. The mount runs in the foreground as the
// supervisor's child; Unmount ends it with fusermount.
type Gocryptfs struct {
	Bin        string // gocryptfs
	Fusermount string // fusermount3
	Run        Ausfuehrer

	mu   sync.Mutex
	cmd  *exec.Cmd
	ende chan error
}

func (g *Gocryptfs) Init(ctx context.Context, cipher string, pw []byte) error {
	a, err := g.Run.Run(ctx, gocryptfsInit(g.Bin, cipher, pw))
	if err != nil {
		return err
	}
	if a.Code != 0 {
		return fmt.Errorf("gocryptfs -init: Code %d: %s", a.Code, schwaerzen(a.Text, pw))
	}
	return nil
}

func (g *Gocryptfs) Mount(ctx context.Context, cipher, mnt string, pw []byte) error {
	b := gocryptfsMount(g.Bin, cipher, mnt, pw)
	cmd := exec.Command(b.Pfad, b.Args...) // not CommandContext: the mount outlives the request that opened it
	cmd.Env = b.Env
	cmd.Stdin = bytes.NewReader(b.Eingabe)
	out := &schwanz{n: 16 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return err
	}
	ende := make(chan error, 1)
	go func() { ende <- cmd.Wait() }()
	bis := time.Now().Add(90 * time.Second)
	for {
		select {
		case err := <-ende:
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() == gocryptfsFalschesPasswort {
				return ErrFalschesPasswort
			}
			return fmt.Errorf("gocryptfs endete vor dem Einhängen (%v): %s", err, schwaerzen(string(out.snapshot()), pw))
		case <-time.After(50 * time.Millisecond):
		}
		if eingehaengt(mnt) {
			g.mu.Lock()
			g.cmd, g.ende = cmd, ende
			g.mu.Unlock()
			return nil
		}
		if time.Now().After(bis) || ctx.Err() != nil {
			cmd.Process.Signal(syscall.SIGTERM)
			<-ende
			return fmt.Errorf("gocryptfs hängte nicht ein: %s", schwaerzen(string(out.snapshot()), pw))
		}
	}
}

func (g *Gocryptfs) Unmount(ctx context.Context, mnt string) error {
	g.mu.Lock()
	cmd, ende := g.cmd, g.ende
	g.mu.Unlock()
	if eingehaengt(mnt) {
		a, err := g.Run.Run(ctx, fusermountU(g.Fusermount, mnt, false))
		if err != nil || a.Code != 0 {
			log.Printf("archiv: fusermount -u: %v %d %s; versuche es lazy", err, a.Code, strings.TrimSpace(a.Text))
			if a, err := g.Run.Run(ctx, fusermountU(g.Fusermount, mnt, true)); err != nil || a.Code != 0 {
				return fmt.Errorf("fusermount -u -z: %v %d %s", err, a.Code, strings.TrimSpace(a.Text))
			}
		}
	}
	if cmd != nil {
		select {
		case <-ende:
		case <-time.After(30 * time.Second):
			cmd.Process.Signal(syscall.SIGTERM)
			<-ende
		}
		g.mu.Lock()
		g.cmd, g.ende = nil, nil
		g.mu.Unlock()
	}
	if eingehaengt(mnt) {
		return errors.New("der Tresor ist nach fusermount noch eingehängt")
	}
	return nil
}

// eingehaengt reports whether mnt is a mount point (/proc/self/mountinfo).
func eingehaengt(mnt string) bool {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64<<10), 1<<20)
	for s.Scan() {
		felder := strings.Fields(s.Text())
		if len(felder) > 4 && entschluesseln(felder[4]) == mnt {
			return true
		}
	}
	return false
}

// entschluesseln undoes mountinfo's octal escapes (\040 for a space).
func entschluesseln(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var n int
			if _, err := fmt.Sscanf(s[i+1:i+4], "%o", &n); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Msgvault is the real Dienst: msgvault serve as a child, its output in
// the container log, ready when /health answers.
type Msgvault struct {
	Bin  string
	Port int

	mu   sync.Mutex
	cmd  *exec.Cmd
	ende chan error
}

func (m *Msgvault) Start(ctx context.Context, home string) error {
	b := msgvaultServe(m.Bin, home)
	cmd := exec.Command(b.Pfad, b.Args...)
	cmd.Env = b.Env
	cmd.Dir = home
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	ende := make(chan error, 1)
	go func() { ende <- cmd.Wait() }()
	m.mu.Lock()
	m.cmd, m.ende = cmd, ende
	m.mu.Unlock()
	url := fmt.Sprintf("http://127.0.0.1:%d/health", m.Port)
	c := &http.Client{Timeout: 2 * time.Second}
	bis := time.Now().Add(3 * time.Minute)
	for {
		if r, err := c.Get(url); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return nil
			}
		}
		select {
		case err := <-ende:
			ende <- err // Stop reads it too
			return fmt.Errorf("msgvault serve endete: %v", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		if time.Now().After(bis) {
			return errors.New("msgvault antwortete nach 3 Minuten nicht")
		}
	}
}

func (m *Msgvault) Stop(ctx context.Context) error {
	m.mu.Lock()
	cmd, ende := m.cmd, m.ende
	m.cmd, m.ende = nil, nil
	m.mu.Unlock()
	if cmd == nil {
		return nil
	}
	cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-ende:
		return nil
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		<-ende
		return errors.New("msgvault endete erst nach SIGKILL")
	}
}

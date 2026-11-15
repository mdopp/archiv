package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdopp/archiv/apps/archiv/tresor"
)

// ablauf records what happened, in order, from every fake.
type ablauf struct {
	mu sync.Mutex
	s  []string
}

func (a *ablauf) note(x string) {
	a.mu.Lock()
	a.s = append(a.s, x)
	a.mu.Unlock()
}

func (a *ablauf) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.s, ",")
}

// fakeMounter keeps the "plain" side in <cipher>/klar while closed and
// moves it to the mount point while open; the password sits in a fake
// gocryptfs.conf. Only the state machine is under test with it.
type fakeMounter struct {
	ereignis *ablauf
	pws      [][]byte // every password it was handed, to check the callers zero them
	mountErr error    // the next Mount fails with this (once)
}

func (f *fakeMounter) note(s string) { f.ereignis.note(s) }

func (f *fakeMounter) Init(ctx context.Context, cipher string, pw []byte) error {
	f.note("init")
	f.pws = append(f.pws, pw)
	if err := os.MkdirAll(filepath.Join(cipher, "klar"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cipher, "gocryptfs.conf"), append([]byte(nil), pw...), 0o600)
}

func (f *fakeMounter) Mount(ctx context.Context, cipher, mnt string, pw []byte) error {
	f.note("mount")
	f.pws = append(f.pws, pw)
	if f.mountErr != nil {
		err := f.mountErr
		f.mountErr = nil
		return err
	}
	want, err := os.ReadFile(filepath.Join(cipher, "gocryptfs.conf"))
	if err != nil {
		return err
	}
	if string(want) != string(pw) {
		return ErrFalschesPasswort
	}
	os.Remove(mnt)
	return os.Rename(filepath.Join(filepath.Dir(cipher), filepath.Base(cipher), "klar"), mnt)
}

func (f *fakeMounter) Unmount(ctx context.Context, mnt string) error {
	f.note("unmount")
	// the cipher folder is the mount point's sibling in these tests
	cipher := filepath.Join(filepath.Dir(mnt), "tresor")
	if err := os.Rename(mnt, filepath.Join(cipher, "klar")); err != nil {
		return err
	}
	return os.MkdirAll(mnt, 0o700)
}

type fakeDienst struct {
	mu       sync.Mutex
	ereignis *ablauf
	home     string
	laeuft   bool
	startErr error
}

func (d *fakeDienst) Start(ctx context.Context, home string) error {
	d.ereignis.note("start")
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.startErr != nil {
		return d.startErr
	}
	d.home, d.laeuft = home, true
	return nil
}

func (d *fakeDienst) Stop(ctx context.Context) error {
	d.ereignis.note("stop")
	d.mu.Lock()
	defer d.mu.Unlock()
	d.laeuft = false
	return nil
}

// uhr is a clock a test moves by hand.
type uhr struct {
	mu sync.Mutex
	t  time.Time
}

func (u *uhr) Now() time.Time { u.mu.Lock(); defer u.mu.Unlock(); return u.t }
func (u *uhr) vor(d time.Duration) {
	u.mu.Lock()
	u.t = u.t.Add(d)
	u.mu.Unlock()
}

type testTresor struct {
	*Tresor
	dir        string
	mnt        string // where the fake mounts the plain side: the test plays the disk
	Empfaenger string // the Briefkasten's public key, outside the vault
	ereignis   *ablauf
	mounter    *fakeMounter
	dienst     *fakeDienst
	uhr        *uhr
	lauf       chan Innen
	P          Programme
	// ablehnungErwartet: this test provokes a refusal on purpose
	ablehnungErwartet bool
	alles             Innen // a test program the person granted every area
}

// The tests have a program of their own, granted every area: it plays the
// disk (innen_test.go). Every other program is today's, with what it asks.
func init() {
	r := tresor.Rechte{}
	for _, b := range tresor.Bereiche {
		r[b] = tresor.Lesen | tresor.Schreiben
	}
	programme = append(programme, programm{id: "test", name: "Test", wozu: "legt in den Tests die Dateien hin", will: r})
}

func testFreigaben() tresor.Freigaben { return alleWuensche() }

// keineAblehnung fails t when a program of today's, granted all it asks
// for, was refused an access: then a feature reached outside its program's
// areas. A test that provokes a refusal sets tt.ablehnungErwartet.
func (tt *testTresor) keineAblehnung(t *testing.T) {
	for _, d := range []string{tt.mnt, filepath.Join(tt.dir, "tresor", "klar")} {
		b, err := os.ReadFile(filepath.Join(d, "zugriffe.jsonl"))
		if err != nil {
			continue
		}
		for _, z := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var e tresor.Zugriff
			if json.Unmarshal([]byte(z), &e) == nil && e.Verweigert != "" && e.Verweigert != "Schlüssel gilt nicht" && e.Programm != "test" && !tt.ablehnungErwartet {
				t.Errorf("the program %s was refused %s %s %q (%s): a feature reached outside its areas", e.Programm, e.Bereich, e.Recht, e.Was, e.Verweigert)
			}
		}
	}
}

func neuerTresor(t *testing.T) *testTresor {
	t.Helper()
	dir := t.TempDir()
	ev := &ablauf{}
	m := &fakeMounter{ereignis: ev}
	d := &fakeDienst{ereignis: ev}
	u := &uhr{t: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	tt := &testTresor{dir: dir, mnt: filepath.Join(dir, "mnt"), Empfaenger: filepath.Join(dir, "briefkasten.pub"), ereignis: ev, mounter: m, dienst: d, uhr: u, lauf: make(chan Innen, 10)}
	tt.Tresor = tresor.Neu(tresor.Konfig{Cipher: filepath.Join(dir, "tresor"), Mnt: tt.mnt, Port: 18890, Empfaenger: tt.Empfaenger,
		Mounter: m, Dienst: d, Leerlauf: 2 * time.Hour, MsgvaultConfig: msgvaultConfig})
	tt.Now = u.Now
	tt.P = neueProgramme(tt.Tresor)
	tt.alles = Innen{tt.Programm("test")}
	for _, i := range []Innen{tt.P.Importe, tt.P.Nachrichten, tt.P.Stammbaum, tt.P.Datenpfleger, tt.alles} {
		testWurzeln.Store(i.p, tt.mnt)
	}
	tt.Tresor.Lauf = func(ctx context.Context) {
		tt.lauf <- tt.alles
		<-ctx.Done()
		m.note("lauf-ende")
	}
	tt.Erkennen()
	t.Cleanup(func() { tt.keineAblehnung(t) })
	return tt
}

// Anlegen and Oeffnen grant what testFreigaben grants.
func (tt *testTresor) Anlegen(ctx context.Context, pw []byte) error {
	return tt.Tresor.Anlegen(ctx, pw, testFreigaben())
}

func (tt *testTresor) Oeffnen(ctx context.Context, pw []byte) error {
	return tt.Tresor.Oeffnen(ctx, pw, testFreigaben())
}

// Innen is the test program's way in, and whether the vault is open.
func (tt *testTresor) Innen() (Innen, bool) { return tt.alles, tt.Stand().Zustand == Offen }

// APIKey is msgvault's key as the vault keeps it, "" while it is not answering.
func (tt *testTresor) APIKey() string {
	if !tt.Stand().Bereit {
		return ""
	}
	b, _ := os.ReadFile(filepath.Join(tt.mnt, "msgvault", "api-key"))
	return string(trimNL(b))
}

// bereit waits until msgvault "answers" (the background start ran).
func (tt *testTresor) bereit(t *testing.T) {
	t.Helper()
	bis := time.Now().Add(5 * time.Second)
	for !tt.Stand().Bereit {
		if time.Now().After(bis) {
			t.Fatalf("not ready: %+v", tt.Stand())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

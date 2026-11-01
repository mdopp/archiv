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
	dir      string
	ereignis *ablauf
	mounter  *fakeMounter
	dienst   *fakeDienst
	uhr      *uhr
	lauf     chan Innen
}

func neuerTresor(t *testing.T) *testTresor {
	t.Helper()
	dir := t.TempDir()
	ev := &ablauf{}
	m := &fakeMounter{ereignis: ev}
	d := &fakeDienst{ereignis: ev}
	u := &uhr{t: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	tt := &testTresor{dir: dir, ereignis: ev, mounter: m, dienst: d, uhr: u, lauf: make(chan Innen, 10)}
	tt.Tresor = &Tresor{Cipher: filepath.Join(dir, "tresor"), Mnt: filepath.Join(dir, "mnt"), Port: 18890,
		Mounter: m, Dienst: d, Leerlauf: 2 * time.Hour, Now: u.Now}
	tt.Tresor.Lauf = func(ctx context.Context, innen Innen) {
		tt.lauf <- innen
		<-ctx.Done()
		m.note("lauf-ende")
	}
	tt.Erkennen()
	return tt
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

func (tt *testTresor) ereignisse() string { return tt.ereignis.String() }

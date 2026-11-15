package tresor

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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
// gocryptfs.conf.
type fakeMounter struct{ ereignis *ablauf }

func (f *fakeMounter) Init(ctx context.Context, cipher string, pw []byte) error {
	f.ereignis.note("init")
	if err := os.MkdirAll(filepath.Join(cipher, "klar"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cipher, "gocryptfs.conf"), append([]byte(nil), pw...), 0o600)
}

func (f *fakeMounter) Mount(ctx context.Context, cipher, mnt string, pw []byte) error {
	f.ereignis.note("mount")
	want, err := os.ReadFile(filepath.Join(cipher, "gocryptfs.conf"))
	if err != nil {
		return err
	}
	if string(want) != string(pw) {
		return ErrFalschesPasswort
	}
	os.Remove(mnt)
	return os.Rename(filepath.Join(cipher, "klar"), mnt)
}

func (f *fakeMounter) Unmount(ctx context.Context, mnt string) error {
	f.ereignis.note("unmount")
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
	startErr error
}

func (d *fakeDienst) Start(ctx context.Context, home string) error {
	d.ereignis.note("start")
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.startErr != nil {
		return d.startErr
	}
	d.home = home
	return nil
}

func (d *fakeDienst) Stop(ctx context.Context) error {
	d.ereignis.note("stop")
	return nil
}

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
	mnt      string
	ereignis *ablauf
	dienst   *fakeDienst
	uhr      *uhr
	lauf     chan struct{}
}

const pw = "richtig-langes-pw"

// alles grants every program named every area, read and write.
func alles(programme ...string) Freigaben {
	f := Freigaben{}
	for _, p := range programme {
		r := Rechte{}
		for _, b := range Bereiche {
			r[b] = Lesen | Schreiben
		}
		f[p] = r
	}
	return f
}

func neuerTresor(t *testing.T) *testTresor {
	t.Helper()
	dir := t.TempDir()
	ev := &ablauf{}
	d := &fakeDienst{ereignis: ev}
	u := &uhr{t: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	tt := &testTresor{dir: dir, mnt: filepath.Join(dir, "mnt"), ereignis: ev, dienst: d, uhr: u, lauf: make(chan struct{}, 10)}
	tt.Tresor = Neu(Konfig{Cipher: filepath.Join(dir, "tresor"), Mnt: tt.mnt, Port: 18890,
		Mounter: &fakeMounter{ereignis: ev}, Dienst: d, Leerlauf: 2 * time.Hour,
		MsgvaultConfig: func(port int) string { return "port = " + strconv.Itoa(port) + "\n" }})
	tt.Now = u.Now
	tt.Lauf = func(ctx context.Context) {
		tt.lauf <- struct{}{}
		<-ctx.Done()
		ev.note("lauf-ende")
	}
	tt.Erkennen()
	return tt
}

// offen makes the vault with f and waits until msgvault "answers".
func (tt *testTresor) offen(t *testing.T, f Freigaben) {
	t.Helper()
	if err := tt.Anlegen(context.Background(), []byte(pw), f); err != nil {
		t.Fatal(err)
	}
	tt.bereit(t)
}

func (tt *testTresor) bereit(t *testing.T) {
	t.Helper()
	bis := time.Now().Add(5 * time.Second)
	for !tt.Stand().Bereit {
		if time.Now().After(bis) {
			t.Fatalf("not ready: %+v", tt.Stand())
		}
		time.Sleep(5 * time.Millisecond)
	}
	<-tt.lauf
}

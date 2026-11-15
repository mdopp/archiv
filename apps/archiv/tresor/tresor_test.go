package tresor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAnlegenOpensAndFurnishesTheVault(t *testing.T) {
	tt := neuerTresor(t)
	if tt.Stand().Zustand != Uneingerichtet {
		t.Fatalf("fresh: %v", tt.Stand().Zustand)
	}
	if err := tt.Oeffnen(context.Background(), []byte("irgendwas-langes"), nil); !errors.Is(err, ErrZustand) {
		t.Fatalf("open before anlegen: %v", err)
	}
	tt.offen(t, alles("p"))
	key, err := os.ReadFile(filepath.Join(tt.mnt, "msgvault", "api-key"))
	if err != nil || len(key) != 64 {
		t.Fatalf("api key: %q %v", key, err)
	}
	if fi, _ := os.Stat(filepath.Join(tt.mnt, "msgvault", "api-key")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("api key mode %v (msgvault refuses anything but owner-only)", fi.Mode())
	}
	z, _ := tt.Schluessel("p")
	if got, _ := z.MsgvaultSchluessel(Lesen, "/api/v1/stats"); got != string(key) {
		t.Fatalf("the key a program gets %q is not the file's", got)
	}
	if cfg, _ := os.ReadFile(filepath.Join(tt.mnt, "msgvault", "config.toml")); string(cfg) != "port = 18890\n" {
		t.Fatalf("config.toml: %q", cfg)
	}
	if tt.dienst.home != filepath.Join(tt.mnt, "msgvault") {
		t.Fatalf("msgvault home %q, want inside the vault", tt.dienst.home)
	}
	if _, err := os.Stat(filepath.Join(tt.mnt, "age-identitaet")); err != nil {
		t.Fatalf("no key pair: %v", err)
	}
	if err := tt.Anlegen(context.Background(), []byte(pw), nil); !errors.Is(err, ErrZustand) {
		t.Fatalf("second anlegen: %v", err)
	}
}

func TestAbschliessenStopsTheProgramsThenMsgvaultThenUnmounts(t *testing.T) {
	tt := neuerTresor(t)
	tt.offen(t, alles("p"))
	if err := tt.Abschliessen(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if got := tt.ereignis.String(); got != "init,mount,start,lauf-ende,stop,unmount" {
		t.Fatalf("order: %s", got)
	}
	st := tt.Stand()
	if st.Zustand != Verschlossen || st.Bereit || st.ZuSeit.IsZero() {
		t.Fatalf("after close: %+v", st)
	}
	if _, ok := tt.Sitzung(); ok {
		t.Fatal("a session after close")
	}
	if e, _ := os.ReadDir(tt.mnt); len(e) != 0 {
		t.Fatalf("the mount point still shows %d entries", len(e))
	}
	if err := tt.Abschliessen(context.Background(), "nochmal"); err != nil {
		t.Fatalf("closing a closed vault: %v", err)
	}
}

func TestOeffnenWithTheWrongPasswordStaysClosed(t *testing.T) {
	tt := neuerTresor(t)
	tt.offen(t, alles("p"))
	key, _ := os.ReadFile(filepath.Join(tt.mnt, "msgvault", "api-key"))
	tt.Abschliessen(context.Background(), "test")
	if err := tt.Oeffnen(context.Background(), []byte("falsch-falsch-falsch"), alles("p")); !errors.Is(err, ErrFalschesPasswort) {
		t.Fatalf("wrong password: %v", err)
	}
	if tt.Stand().Zustand != Verschlossen {
		t.Fatalf("after a wrong password: %v", tt.Stand().Zustand)
	}
	if _, err := tt.Schluessel("p"); !errors.Is(err, ErrAbgeschlossen) {
		t.Fatalf("a key after a wrong password: %v", err)
	}
	if err := tt.Oeffnen(context.Background(), []byte(pw), alles("p")); err != nil {
		t.Fatal(err)
	}
	tt.bereit(t)
	if again, _ := os.ReadFile(filepath.Join(tt.mnt, "msgvault", "api-key")); string(again) != string(key) {
		t.Fatal("reopening made a new msgvault key")
	}
}

func TestErkennenFindsAnExistingVaultClosed(t *testing.T) {
	tt := neuerTresor(t)
	tt.offen(t, alles("p"))
	tt.Abschliessen(context.Background(), "test")
	again := Neu(Konfig{Cipher: filepath.Join(tt.dir, "tresor"), Mnt: tt.mnt})
	again.Erkennen()
	if again.Stand().Zustand != Verschlossen {
		t.Fatalf("restart: %v", again.Stand().Zustand)
	}
}

func TestIdleCloseAfterLeerlaufButNotDuringAnImport(t *testing.T) {
	tt := neuerTresor(t)
	tt.offen(t, alles("p"))
	tt.uhr.vor(119 * time.Minute)
	tt.Pruefen(context.Background())
	if tt.Stand().Zustand != Offen {
		t.Fatal("closed before two hours")
	}
	tt.Aktiv()
	tt.uhr.vor(119 * time.Minute)
	tt.Pruefen(context.Background())
	if tt.Stand().Zustand != Offen {
		t.Fatal("a use did not keep it open")
	}
	fertig := tt.Arbeit()
	tt.uhr.vor(5 * time.Hour)
	tt.Pruefen(context.Background())
	if tt.Stand().Zustand != Offen {
		t.Fatal("closed in the middle of an import")
	}
	fertig()
	tt.uhr.vor(2 * time.Hour)
	tt.Pruefen(context.Background())
	if tt.Stand().Zustand != Verschlossen {
		t.Fatalf("not closed after two idle hours: %+v", tt.Stand())
	}
}

func TestOpeningDeletesPlaintextLeftByACrash(t *testing.T) {
	tt := neuerTresor(t)
	tt.offen(t, alles("p"))
	rest := filepath.Join(tt.mnt, "arbeit", "signal-abc.xml")
	os.WriteFile(rest, []byte("<smses>klartext</smses>"), 0o600)
	tt.Abschliessen(context.Background(), "test")
	tt.Oeffnen(context.Background(), []byte(pw), nil)
	tt.bereit(t)
	if _, err := os.Stat(rest); !os.IsNotExist(err) {
		t.Fatalf("a leftover plaintext export survived the reopening: %v", err)
	}
}

func TestMsgvaultThatDoesNotStartIsSaidAndTheVaultCanStillClose(t *testing.T) {
	tt := neuerTresor(t)
	tt.dienst.startErr = errors.New("kaputt")
	tt.Anlegen(context.Background(), []byte(pw), alles("p"))
	bis := time.Now().Add(2 * time.Second)
	for tt.Stand().Fehler == "" && time.Now().Before(bis) {
		time.Sleep(5 * time.Millisecond)
	}
	st := tt.Stand()
	z, _ := tt.Schluessel("p")
	if k, err := z.MsgvaultSchluessel(Lesen, "/"); st.Fehler == "" || st.Bereit || k != "" || err == nil {
		t.Fatalf("a failed start: %+v, key %q", st, k)
	}
	if err := tt.Abschliessen(context.Background(), "test"); err != nil || tt.Stand().Zustand != Verschlossen {
		t.Fatalf("close after a failed start: %v %v", err, tt.Stand().Zustand)
	}
}

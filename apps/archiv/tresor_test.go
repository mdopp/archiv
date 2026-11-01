package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnlegenOpensAndFurnishesTheVault(t *testing.T) {
	tt := neuerTresor(t)
	if tt.Stand().Zustand != Uneingerichtet {
		t.Fatalf("fresh: %v", tt.Stand().Zustand)
	}
	if err := tt.Oeffnen(context.Background(), []byte("irgendwas-langes")); !errors.Is(err, ErrZustand) {
		t.Fatalf("open before anlegen: %v", err)
	}
	if err := tt.Anlegen(context.Background(), []byte("richtig-langes-pw")); err != nil {
		t.Fatal(err)
	}
	tt.bereit(t)
	innen := <-tt.lauf
	if innen.Wurzel != tt.Mnt || tt.Stand().Zustand != Offen {
		t.Fatalf("after anlegen: %+v %v", innen, tt.Stand())
	}
	key, err := os.ReadFile(innen.APIKey())
	if err != nil || len(key) != 64 {
		t.Fatalf("api key: %q %v", key, err)
	}
	if fi, _ := os.Stat(innen.APIKey()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("api key mode %v (msgvault refuses anything but owner-only)", fi.Mode())
	}
	if tt.APIKey() != string(key) {
		t.Fatalf("the proxy's key %q is not the file's", tt.APIKey())
	}
	cfg, _ := os.ReadFile(filepath.Join(innen.Home(), "config.toml"))
	for _, want := range []string{`bind_addr = "127.0.0.1"`, "api_port = 18890", `api_key_file = "api-key"`, "daemon_auto_start = false", "[telemetry]\nenabled = false"} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("config.toml lacks %q:\n%s", want, cfg)
		}
	}
	if tt.dienst.home != innen.Home() {
		t.Fatalf("msgvault home %q, want inside the vault %q", tt.dienst.home, innen.Home())
	}
	if err := tt.Anlegen(context.Background(), []byte("richtig-langes-pw")); !errors.Is(err, ErrZustand) {
		t.Fatalf("second anlegen: %v", err)
	}
}

func TestAbschliessenStopsTheImportThenMsgvaultThenUnmounts(t *testing.T) {
	tt := neuerTresor(t)
	if err := tt.Anlegen(context.Background(), []byte("richtig-langes-pw")); err != nil {
		t.Fatal(err)
	}
	tt.bereit(t)
	<-tt.lauf
	if err := tt.Abschliessen(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if got := tt.ereignisse(); got != "init,mount,start,lauf-ende,stop,unmount" {
		t.Fatalf("order: %s", got)
	}
	st := tt.Stand()
	if st.Zustand != Verschlossen || st.Bereit || tt.APIKey() != "" || st.ZuSeit.IsZero() {
		t.Fatalf("after close: %+v key %q", st, tt.APIKey())
	}
	if _, ok := tt.Innen(); ok {
		t.Fatal("Innen says open after close")
	}
	if e, _ := os.ReadDir(tt.Mnt); len(e) != 0 {
		t.Fatalf("the mount point still shows %d entries", len(e))
	}
	if err := tt.Abschliessen(context.Background(), "nochmal"); err != nil {
		t.Fatalf("closing a closed vault: %v", err)
	}
}

func TestOeffnenWithTheWrongPasswordStaysClosed(t *testing.T) {
	tt := neuerTresor(t)
	tt.Anlegen(context.Background(), []byte("richtig-langes-pw"))
	tt.bereit(t)
	<-tt.lauf
	key := tt.APIKey()
	tt.Abschliessen(context.Background(), "test")

	if err := tt.Oeffnen(context.Background(), []byte("falsch-falsch-falsch")); !errors.Is(err, ErrFalschesPasswort) {
		t.Fatalf("wrong password: %v", err)
	}
	if tt.Stand().Zustand != Verschlossen {
		t.Fatalf("after a wrong password: %v", tt.Stand().Zustand)
	}
	if err := tt.Oeffnen(context.Background(), []byte("richtig-langes-pw")); err != nil {
		t.Fatal(err)
	}
	tt.bereit(t)
	<-tt.lauf
	if tt.APIKey() != key {
		t.Fatal("reopening made a new API key: agents would lose access")
	}
}

func TestErkennenFindsAnExistingVaultClosed(t *testing.T) {
	tt := neuerTresor(t)
	tt.Anlegen(context.Background(), []byte("richtig-langes-pw"))
	tt.bereit(t)
	<-tt.lauf
	tt.Abschliessen(context.Background(), "test")
	again := &Tresor{Cipher: tt.Cipher, Mnt: tt.Mnt}
	again.Erkennen()
	if again.Stand().Zustand != Verschlossen {
		t.Fatalf("restart: %v", again.Stand().Zustand)
	}
}

func TestIdleCloseAfterLeerlaufButNotDuringAnImport(t *testing.T) {
	tt := neuerTresor(t)
	tt.Anlegen(context.Background(), []byte("richtig-langes-pw"))
	tt.bereit(t)
	<-tt.lauf

	tt.uhr.vor(119 * time.Minute)
	tt.Pruefen(context.Background())
	if tt.Stand().Zustand != Offen {
		t.Fatal("closed before two hours")
	}
	tt.Aktiv() // a use starts the clock again
	tt.uhr.vor(119 * time.Minute)
	tt.Pruefen(context.Background())
	if tt.Stand().Zustand != Offen {
		t.Fatal("a use did not keep it open")
	}

	fertig := tt.Arbeit() // an import runs
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
	tt.Anlegen(context.Background(), []byte("richtig-langes-pw"))
	tt.bereit(t)
	innen := <-tt.lauf
	rest := filepath.Join(innen.Arbeit(), "signal-abc.xml")
	os.WriteFile(rest, []byte("<smses>klartext</smses>"), 0o600)
	tt.Abschliessen(context.Background(), "test")
	tt.Oeffnen(context.Background(), []byte("richtig-langes-pw"))
	tt.bereit(t)
	<-tt.lauf
	if _, err := os.Stat(rest); !os.IsNotExist(err) {
		t.Fatalf("a leftover plaintext export survived the reopening: %v", err)
	}
}

func TestMsgvaultThatDoesNotStartIsSaidAndTheVaultCanStillClose(t *testing.T) {
	tt := neuerTresor(t)
	tt.dienst.startErr = errors.New("kaputt")
	tt.Anlegen(context.Background(), []byte("richtig-langes-pw"))
	bis := time.Now().Add(2 * time.Second)
	for tt.Stand().Fehler == "" && time.Now().Before(bis) {
		time.Sleep(5 * time.Millisecond)
	}
	if st := tt.Stand(); st.Fehler == "" || st.Bereit || tt.APIKey() != "" {
		t.Fatalf("a failed start: %+v", st)
	}
	if err := tt.Abschliessen(context.Background(), "test"); err != nil || tt.Stand().Zustand != Verschlossen {
		t.Fatalf("close after a failed start: %v %v", err, tt.Stand().Zustand)
	}
}

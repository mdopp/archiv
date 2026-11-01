package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filippo.io/age"
)

// The Tresor (laib ADR 0029 §3): the person's whole archive lives in a
// gocryptfs cipher folder in the data volume and is mounted only while
// open. uneingerichtet → (anlegen) → offen ↔ verschlossen. Open: mount,
// start msgvault with its home inside the vault, then import what waits.
// Close: on „Abschließen“, after the idle time, or when the container
// stops: stop msgvault, unmount.

type Zustand string

const (
	Uneingerichtet Zustand = "uneingerichtet"
	Verschlossen   Zustand = "verschlossen"
	Offen          Zustand = "offen"
)

var (
	ErrFalschesPasswort = errors.New("das Archiv-Passwort passt nicht")
	ErrZustand          = errors.New("geht in diesem Zustand nicht")
)

// Mounter makes, mounts and unmounts the cipher folder. The real one is
// gocryptfs; tests use a fake that keeps the plain folder elsewhere.
type Mounter interface {
	Init(ctx context.Context, cipher string, pw []byte) error
	Mount(ctx context.Context, cipher, mnt string, pw []byte) error // ErrFalschesPasswort on a wrong one
	Unmount(ctx context.Context, mnt string) error
}

// Dienst is msgvault serve: Start returns once it answers (or fails).
type Dienst interface {
	Start(ctx context.Context, home string) error
	Stop(ctx context.Context) error
}

// Innen are the paths inside the open vault.
type Innen struct{ Wurzel string }

func (i Innen) Home() string       { return filepath.Join(i.Wurzel, "msgvault") }
func (i Innen) Identitaet() string { return filepath.Join(i.Wurzel, "age-identitaet") } // the private key of the Briefkasten
func (i Innen) Arbeit() string     { return filepath.Join(i.Wurzel, "arbeit") }         // plaintext exports while an import runs, emptied after each
func (i Innen) APIKey() string     { return filepath.Join(i.Home(), "api-key") }

// Tresor is the state machine. All transitions hold mu.
type Tresor struct {
	Cipher     string
	Mnt        string
	Port       int    // msgvault's loopback port, written into its config.toml
	Empfaenger string // the public key of the Briefkasten, outside the vault
	Mounter    Mounter
	Dienst     Dienst
	Leerlauf   time.Duration
	// Lauf runs in the background while the vault is open and msgvault
	// answers (the import); it must return soon after ctx ends.
	Lauf func(ctx context.Context, innen Innen)
	// Geaendert is called after every transition (the status file).
	Geaendert func()
	Now       func() time.Time

	mu        sync.Mutex
	zustand   Zustand
	bereit    bool      // msgvault answers
	fehler    string    // a German sentence when msgvault did not start
	letzte    time.Time // last use
	zuSeit    time.Time // closed since (zero: never opened in this run)
	apiKey    string
	halt      context.CancelFunc
	fertig    chan struct{}
	arbeitet  int // imports running: the vault does not close by idleness meanwhile
	vorgaenge sync.Mutex
}

func (t *Tresor) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Erkennen reads the state from disk once, at start.
func (t *Tresor) Erkennen() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := os.Stat(filepath.Join(t.Cipher, "gocryptfs.conf")); err == nil {
		t.zustand = Verschlossen
	} else {
		t.zustand = Uneingerichtet
	}
}

// Stand is a snapshot for the pages and the status file.
type Stand struct {
	Zustand  Zustand
	Bereit   bool
	Fehler   string
	Letzte   time.Time
	ZuSeit   time.Time
	ZuUm     time.Time // when it closes by idleness, if nothing happens
	Arbeitet bool
}

func (t *Tresor) Stand() Stand {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := Stand{Zustand: t.zustand, Bereit: t.bereit, Fehler: t.fehler, Letzte: t.letzte, ZuSeit: t.zuSeit, Arbeitet: t.arbeitet > 0}
	if t.zustand == Offen {
		s.ZuUm = t.letzte.Add(t.Leerlauf)
	}
	return s
}

// APIKey is msgvault's key while open and answering, else "".
func (t *Tresor) APIKey() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.zustand != Offen || !t.bereit {
		return ""
	}
	return t.apiKey
}

// Innen is the open vault's inside; ok false while closed.
func (t *Tresor) Innen() (Innen, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Innen{t.Mnt}, t.zustand == Offen
}

// Aktiv notes a use: the idle clock starts again.
func (t *Tresor) Aktiv() {
	t.mu.Lock()
	t.letzte = t.now()
	t.mu.Unlock()
}

// Arbeit marks an import running (no idle close meanwhile); the returned
// func ends it.
func (t *Tresor) Arbeit() func() {
	t.mu.Lock()
	t.arbeitet++
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		t.arbeitet--
		t.letzte = t.now()
		t.mu.Unlock()
	}
}

func (t *Tresor) geaendert() {
	if t.Geaendert != nil {
		t.Geaendert()
	}
}

// Anlegen makes the vault with the person's password and opens it.
func (t *Tresor) Anlegen(ctx context.Context, pw []byte) error {
	t.vorgaenge.Lock()
	defer t.vorgaenge.Unlock()
	if t.Stand().Zustand != Uneingerichtet {
		return ErrZustand
	}
	if err := os.MkdirAll(t.Cipher, 0o700); err != nil {
		return err
	}
	if eintraege, _ := os.ReadDir(t.Cipher); len(eintraege) > 0 {
		return fmt.Errorf("der Tresor-Ordner %s ist nicht leer", t.Cipher)
	}
	if err := t.Mounter.Init(ctx, t.Cipher, pw); err != nil {
		return err
	}
	t.mu.Lock()
	t.zustand = Verschlossen
	t.mu.Unlock()
	log.Printf("archiv: Tresor angelegt")
	return t.oeffnen(ctx, pw)
}

// Oeffnen mounts the vault and starts msgvault in the background.
func (t *Tresor) Oeffnen(ctx context.Context, pw []byte) error {
	t.vorgaenge.Lock()
	defer t.vorgaenge.Unlock()
	switch t.Stand().Zustand {
	case Offen:
		t.Aktiv()
		return nil
	case Uneingerichtet:
		return ErrZustand
	}
	return t.oeffnen(ctx, pw)
}

func (t *Tresor) oeffnen(ctx context.Context, pw []byte) error {
	if err := os.MkdirAll(t.Mnt, 0o700); err != nil {
		return err
	}
	if err := t.Mounter.Mount(ctx, t.Cipher, t.Mnt, pw); err != nil {
		return err
	}
	innen := Innen{t.Mnt}
	key, err := einrichten(innen, t.Port, t.Empfaenger)
	if err != nil {
		t.Mounter.Unmount(context.Background(), t.Mnt)
		return fmt.Errorf("Tresor einrichten: %w", err)
	}
	lauf, halt := context.WithCancel(context.Background())
	fertig := make(chan struct{})
	t.mu.Lock()
	t.zustand, t.bereit, t.fehler, t.apiKey = Offen, false, "", key
	t.letzte = t.now()
	t.halt, t.fertig = halt, fertig
	t.mu.Unlock()
	log.Printf("archiv: Tresor offen")
	t.geaendert()
	go t.laufen(lauf, innen, fertig)
	return nil
}

// laufen starts msgvault, then runs the import until the vault closes.
func (t *Tresor) laufen(ctx context.Context, innen Innen, fertig chan struct{}) {
	defer close(fertig)
	if err := t.Dienst.Start(ctx, innen.Home()); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("archiv: msgvault startet nicht: %v", err)
		t.mu.Lock()
		t.fehler = "Das Archiv-Programm startete nicht. Schließ das Archiv ab und öffne es noch einmal; hilft das nicht, sag dem Admin Bescheid."
		t.mu.Unlock()
		t.geaendert()
		return
	}
	t.mu.Lock()
	t.bereit = true
	t.mu.Unlock()
	t.geaendert()
	if t.Lauf != nil {
		t.Lauf(ctx, innen)
	}
}

// Abschliessen stops the import and msgvault and unmounts.
func (t *Tresor) Abschliessen(ctx context.Context, grund string) error {
	t.vorgaenge.Lock()
	defer t.vorgaenge.Unlock()
	t.mu.Lock()
	if t.zustand != Offen {
		t.mu.Unlock()
		return nil
	}
	halt, fertig := t.halt, t.fertig
	t.mu.Unlock()
	halt()
	<-fertig // the import cleans its plaintext up before it returns
	if err := t.Dienst.Stop(ctx); err != nil {
		log.Printf("archiv: msgvault anhalten: %v", err)
	}
	if err := t.Mounter.Unmount(ctx, t.Mnt); err != nil {
		t.mu.Lock()
		t.fehler = "Das Archiv ließ sich nicht abschließen. Sag dem Admin Bescheid."
		t.mu.Unlock()
		t.geaendert()
		return err
	}
	t.mu.Lock()
	t.zustand, t.bereit, t.fehler, t.apiKey = Verschlossen, false, "", ""
	t.zuSeit = t.now()
	t.mu.Unlock()
	log.Printf("archiv: Tresor abgeschlossen (%s)", grund)
	t.geaendert()
	return nil
}

// Pruefen closes the vault after Leerlauf without use and without a
// running import. Called by a ticker.
func (t *Tresor) Pruefen(ctx context.Context) {
	t.mu.Lock()
	zu := t.zustand == Offen && t.arbeitet == 0 && t.Leerlauf > 0 && !t.now().Before(t.letzte.Add(t.Leerlauf))
	t.mu.Unlock()
	if zu {
		if err := t.Abschliessen(ctx, "leerlauf"); err != nil {
			log.Printf("archiv: abschließen nach Leerlauf: %v", err)
		}
	}
}

// einrichten makes the inside of a vault (idempotent): msgvault's home
// with its key and config, an empty work folder, and the Briefkasten's
// key pair — the private key inside, the public key written outside
// (empfaenger) so the inbox can be packed while the vault is closed. It
// returns the API key.
func einrichten(innen Innen, port int, empfaenger string) (string, error) {
	for _, d := range []string{innen.Home(), innen.Arbeit()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	// a plaintext export left by a crash: gone before anything else runs
	if alt, _ := os.ReadDir(innen.Arbeit()); len(alt) > 0 {
		for _, e := range alt {
			os.RemoveAll(filepath.Join(innen.Arbeit(), e.Name()))
		}
		log.Printf("archiv: %d Reste eines abgebrochenen Imports gelöscht", len(alt))
	}
	key, err := os.ReadFile(innen.APIKey())
	if errors.Is(err, os.ErrNotExist) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		key = []byte(hex.EncodeToString(b))
		if err := schreibenAtomar(innen.APIKey(), key, 0o600); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if err := schluesselpaar(innen.Identitaet(), empfaenger); err != nil {
		return "", fmt.Errorf("Schlüsselpaar: %w", err)
	}
	cfg := filepath.Join(innen.Home(), "config.toml")
	if _, err := os.Stat(cfg); errors.Is(err, os.ErrNotExist) {
		if err := schreibenAtomar(cfg, []byte(msgvaultConfig(port)), 0o600); err != nil {
			return "", err
		}
	}
	return string(trimNL(key)), nil
}

// schluesselpaar makes the age X25519 pair once and keeps the public
// key outside in step with the private one (the private one decides).
func schluesselpaar(identitaet, empfaenger string) error {
	id, err := identitaetLesen(identitaet)
	if errors.Is(err, os.ErrNotExist) {
		id, err = age.GenerateX25519Identity()
		if err != nil {
			return err
		}
		if err := schreibenAtomar(identitaet, []byte(id.String()+"\n"), 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if empfaenger == "" {
		return nil
	}
	pub := id.Recipient().String()
	if alt, err := os.ReadFile(empfaenger); err == nil && string(trimNL(alt)) == pub {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(empfaenger), 0o755); err != nil {
		return err
	}
	return schreibenAtomar(empfaenger, []byte(pub+"\n"), 0o644)
}

// msgvaultConfig: loopback only, the vault's key from a file beside it,
// no daemon of its own (the supervisor runs serve), telemetry off, and
// the supervisor on 127.0.0.1 trusted for the forwarded scheme and host.
func msgvaultConfig(port int) string {
	return fmt.Sprintf(`# Written by archiv when the vault was first opened (laib ADR 0029).
[server]
bind_addr = "127.0.0.1"
api_port = %d
api_key_file = "api-key"
daemon_auto_start = false
daemon_auto_restart = "never"
trusted_proxies = ["127.0.0.1"]

[telemetry]
enabled = false
`, port)
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

// schreibenAtomar writes a file through a temporary beside it.
func schreibenAtomar(pfad string, inhalt []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(pfad), "."+filepath.Base(pfad)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(inhalt); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), pfad)
}

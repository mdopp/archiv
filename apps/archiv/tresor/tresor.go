// Package tresor is the vault (archiv ADR 0001): one encrypted store per
// person and nothing else. It unlocks with the person's password, opens a
// session that expires, and serves its data only through keys bound to
// areas, read and write separately, each access in its protocol. Locked,
// nobody reads it. The features are not in here: they are programs that
// hold a key (sitzung.go), and a new one needs no change to this package.
package tresor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
)

// The vault (laib ADR 0029 §3): the person's whole archive lives in a
// gocryptfs cipher folder in the data volume and is mounted only while
// open. uneingerichtet → (anlegen) → offen ↔ verschlossen. Open: mount,
// start a session, start msgvault with its home inside the vault, then
// the programs run. Close: on „Abschließen“, after the idle time, when the
// session ends, or when the container stops: void every key, stop the
// programs and msgvault, unmount.

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

// Konfig is what the vault is made of. The mount point stays inside
// the vault: nothing outside this package learns it from a Tresor.
type Konfig struct {
	Cipher         string
	Mnt            string
	Port           int    // msgvault's loopback port, written into its config.toml
	Empfaenger     string // the public key of the Briefkasten, outside the vault
	Mounter        Mounter
	Dienst         Dienst
	Leerlauf       time.Duration // closes after this long without use (0: never by idleness)
	Sitzung        time.Duration // a session ends this long after the unlock (0: SitzungStandard)
	Schluessel     time.Duration // a key lives this long at most (0: SchluesselStandard)
	MsgvaultConfig func(port int) string
}

const (
	SitzungStandard    = 12 * time.Hour
	SchluesselStandard = 15 * time.Minute
)

// Tresor is the state machine. All transitions hold mu.
type Tresor struct {
	k Konfig

	// Lauf runs in the background while the vault is open and msgvault
	// answers (the programs); it must return soon after ctx ends.
	Lauf func(ctx context.Context)
	// Einziehen runs on every opening, after the session started and
	// before msgvault starts (a key left outside moves in). An error is
	// logged and the vault opens anyway; the next opening tries again.
	Einziehen func() error
	// Vorbereiten runs on every opening after Einziehen and before
	// msgvault starts. An error is logged and the vault opens anyway.
	Vorbereiten func() error
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
	sitzung   *sitzung // nil: no session, every key is void
	halt      context.CancelFunc
	fertig    chan struct{}
	arbeitet  int // imports running: the vault does not close by idleness meanwhile
	vorgaenge sync.Mutex
	// zugriff: an access holds it for reading from its check to the end of
	// its read or write; closing takes it whole once, after the keys are
	// void, so no access that passed its check is still writing when the
	// vault unmounts.
	zugriff sync.RWMutex
	prot    zugriffe
}

// Neu is a vault of k; Erkennen reads its state.
func Neu(k Konfig) *Tresor {
	if k.Sitzung <= 0 {
		k.Sitzung = SitzungStandard
	}
	if k.Schluessel <= 0 {
		k.Schluessel = SchluesselStandard
	}
	return &Tresor{k: k}
}

func (t *Tresor) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tresor) pfad(rel string) string { return filepath.Join(t.k.Mnt, filepath.FromSlash(rel)) }

// Erkennen reads the state from disk once, at start.
func (t *Tresor) Erkennen() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := os.Stat(filepath.Join(t.k.Cipher, "gocryptfs.conf")); err == nil {
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
	ZuUm     time.Time // when it closes by idleness or by the session's end, if nothing happens
	Arbeitet bool
}

func (t *Tresor) Stand() Stand {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := Stand{Zustand: t.zustand, Bereit: t.bereit, Fehler: t.fehler, Letzte: t.letzte, ZuSeit: t.zuSeit, Arbeitet: t.arbeitet > 0}
	if t.zustand == Offen {
		s.ZuUm = t.letzte.Add(t.k.Leerlauf)
		if t.sitzung != nil && (t.k.Leerlauf <= 0 || t.sitzung.bis.Before(s.ZuUm)) {
			s.ZuUm = t.sitzung.bis
		}
	}
	return s
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

// Anlegen makes the vault with the person's password and opens it with
// her grants.
func (t *Tresor) Anlegen(ctx context.Context, pw []byte, f Freigaben) error {
	t.vorgaenge.Lock()
	defer t.vorgaenge.Unlock()
	if t.Stand().Zustand != Uneingerichtet {
		return ErrZustand
	}
	if err := os.MkdirAll(t.k.Cipher, 0o700); err != nil {
		return err
	}
	if eintraege, _ := os.ReadDir(t.k.Cipher); len(eintraege) > 0 {
		return fmt.Errorf("der Tresor-Ordner %s ist nicht leer", t.k.Cipher)
	}
	if err := t.k.Mounter.Init(ctx, t.k.Cipher, pw); err != nil {
		return err
	}
	t.mu.Lock()
	t.zustand = Verschlossen
	t.mu.Unlock()
	log.Printf("archiv: Tresor angelegt")
	return t.oeffnen(ctx, pw, f)
}

// Oeffnen mounts the vault, starts a session with the person's grants
// and msgvault in the background. Open already, it is a use, and the
// session keeps its grants: other grants need a new session.
func (t *Tresor) Oeffnen(ctx context.Context, pw []byte, f Freigaben) error {
	t.vorgaenge.Lock()
	defer t.vorgaenge.Unlock()
	switch t.Stand().Zustand {
	case Offen:
		t.Aktiv()
		return nil
	case Uneingerichtet:
		return ErrZustand
	}
	return t.oeffnen(ctx, pw, f)
}

func (t *Tresor) oeffnen(ctx context.Context, pw []byte, f Freigaben) error {
	if err := os.MkdirAll(t.k.Mnt, 0o700); err != nil {
		return err
	}
	if err := t.k.Mounter.Mount(ctx, t.k.Cipher, t.k.Mnt, pw); err != nil {
		return err
	}
	key, err := t.einrichten()
	if err != nil {
		t.k.Mounter.Unmount(context.Background(), t.k.Mnt)
		return fmt.Errorf("Tresor einrichten: %w", err)
	}
	t.mu.Lock()
	t.sitzung = neueSitzung(t.now(), t.k.Sitzung, f)
	t.apiKey = key
	t.mu.Unlock()
	t.prot.beginnen(t.pfad(protokollName))
	t.sitzungProtokoll("geöffnet")
	if t.Einziehen != nil {
		if err := t.Einziehen(); err != nil {
			log.Printf("archiv: Signal-Passphrase in den Tresor übernehmen: %v (bleibt außen, nächstes Öffnen versucht es wieder)", err)
		}
	}
	if t.Vorbereiten != nil {
		if err := t.Vorbereiten(); err != nil {
			log.Printf("archiv: Kontakte vorbereiten: %v (das Archiv öffnet trotzdem, ohne Kontakte)", err)
		}
	}
	lauf, halt := context.WithCancel(context.Background())
	fertig := make(chan struct{})
	t.mu.Lock()
	t.zustand, t.bereit, t.fehler = Offen, false, ""
	t.letzte = t.now()
	t.halt, t.fertig = halt, fertig
	t.mu.Unlock()
	log.Printf("archiv: Tresor offen")
	t.geaendert()
	go t.laufen(lauf, fertig)
	return nil
}

// laufen starts msgvault, then runs the programs until the vault closes.
func (t *Tresor) laufen(ctx context.Context, fertig chan struct{}) {
	defer close(fertig)
	if err := t.k.Dienst.Start(ctx, t.pfad("msgvault")); err != nil {
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
		t.Lauf(ctx)
	}
}

// Abschliessen voids every key at once, then stops the programs and
// msgvault and unmounts.
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
	t.sitzungProtokoll("abgeschlossen: " + grund)
	t.mu.Lock()
	t.sitzung, t.apiKey, t.bereit = nil, "", false
	t.mu.Unlock()
	t.zugriff.Lock() // the accesses that passed their check before have ended
	t.prot.enden()
	t.zugriff.Unlock()
	halt()
	<-fertig // the import cleans its plaintext up before it returns
	if err := t.k.Dienst.Stop(ctx); err != nil {
		log.Printf("archiv: msgvault anhalten: %v", err)
	}
	if err := t.k.Mounter.Unmount(ctx, t.k.Mnt); err != nil {
		t.mu.Lock()
		t.fehler = "Das Archiv ließ sich nicht abschließen. Sag dem Admin Bescheid."
		t.mu.Unlock()
		t.geaendert()
		return err
	}
	t.mu.Lock()
	t.zustand, t.fehler = Verschlossen, ""
	t.zuSeit = t.now()
	t.mu.Unlock()
	log.Printf("archiv: Tresor abgeschlossen (%s)", grund)
	t.geaendert()
	return nil
}

// Pruefen closes the vault after Leerlauf without use and without a
// running import, and when the session ended. Called by a ticker.
func (t *Tresor) Pruefen(ctx context.Context) {
	t.mu.Lock()
	jetzt := t.now()
	offen := t.zustand == Offen
	leer := offen && t.arbeitet == 0 && t.k.Leerlauf > 0 && !jetzt.Before(t.letzte.Add(t.k.Leerlauf))
	um := offen && t.sitzung != nil && !jetzt.Before(t.sitzung.bis)
	t.mu.Unlock()
	grund := ""
	switch {
	case um:
		grund = "Sitzung abgelaufen"
	case leer:
		grund = "leerlauf"
	default:
		return
	}
	if err := t.Abschliessen(ctx, grund); err != nil {
		log.Printf("archiv: abschließen (%s): %v", grund, err)
	}
}

// einrichten makes the inside of a vault (idempotent): msgvault's home
// with its key and config, an empty work folder, and the Briefkasten's
// key pair — the private key inside, the public key written outside
// (Empfaenger) so the inbox can be packed while the vault is closed. It
// returns the API key.
func (t *Tresor) einrichten() (string, error) {
	home, arbeit := t.pfad("msgvault"), t.pfad("arbeit")
	for _, d := range []string{home, arbeit} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	// a plaintext export left by a crash: gone before anything else runs
	if alt, _ := os.ReadDir(arbeit); len(alt) > 0 {
		for _, e := range alt {
			os.RemoveAll(filepath.Join(arbeit, e.Name()))
		}
		log.Printf("archiv: %d Reste eines abgebrochenen Imports gelöscht", len(alt))
	}
	keyDatei := filepath.Join(home, "api-key")
	key, err := os.ReadFile(keyDatei)
	if errors.Is(err, os.ErrNotExist) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		key = []byte(hex.EncodeToString(b))
		if err := SchreibenAtomar(keyDatei, key, 0o600); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if err := schluesselpaar(t.pfad("age-identitaet"), t.k.Empfaenger); err != nil {
		return "", fmt.Errorf("Schlüsselpaar: %w", err)
	}
	cfg := filepath.Join(home, "config.toml")
	if _, err := os.Stat(cfg); errors.Is(err, os.ErrNotExist) && t.k.MsgvaultConfig != nil {
		if err := SchreibenAtomar(cfg, []byte(t.k.MsgvaultConfig(t.k.Port)), 0o600); err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(string(key)), nil
}

// schluesselpaar makes the age X25519 pair once and keeps the public
// key outside in step with the private one (the private one decides).
func schluesselpaar(identitaet, empfaenger string) error {
	var id *age.X25519Identity
	b, err := os.ReadFile(identitaet)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if id, err = age.GenerateX25519Identity(); err != nil {
			return err
		}
		if err := SchreibenAtomar(identitaet, []byte(id.String()+"\n"), 0o600); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		id, err = age.ParseX25519Identity(strings.TrimSpace(string(b)))
		clear(b)
		if err != nil {
			return err
		}
	}
	if empfaenger == "" {
		return nil
	}
	pub := id.Recipient().String()
	if alt, err := os.ReadFile(empfaenger); err == nil && strings.TrimSpace(string(alt)) == pub {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(empfaenger), 0o755); err != nil {
		return err
	}
	return SchreibenAtomar(empfaenger, []byte(pub+"\n"), 0o644)
}

// SchreibenAtomar writes a file through a temporary beside it.
func SchreibenAtomar(pfad string, inhalt []byte, mode os.FileMode) error {
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

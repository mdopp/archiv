package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The IMAP login and the person's number live as owner-only files in
// /data/schluessel, OUTSIDE the vault (owner, 2026-11-01: the Mailholer
// fetches mail while the vault is closed; the number is no secret, and the
// page and the status show it while closed).
// They are accepted only while the vault is open, so only the person sets
// them, and never shown again: the page says only „hinterlegt am …“. Names
// are fixed; nothing a request says becomes a path.
//
// The Signal passphrase is not one of them any more (owner, 2026-11-02:
// „Bring den Key dann doch in Sicherheit“): Signal is read only while the
// vault is open, so the passphrase lives inside it (Innen.SignalPassphrase),
// as the WhatsApp key does. SignalEinziehen moves one left outside.

const (
	SchluesselTelefon = "telefon" // the person's own number, E.164 (the XML import's owner)
	SchluesselIMAP    = "imap"    // the person's mailboxes, a JSON list of imapZugang (before 2026-11-11 one object: imapKontenAus)

	altSignal = "signal" // where the Signal passphrase was kept before 2026-11-02; only SignalEinziehen reads it
)

var schluesselNamen = map[string]bool{SchluesselTelefon: true, SchluesselIMAP: true}

// What the key forms answer, one German sentence each.
const (
	saysSignalFormat  = "Die Signal-Passphrase hat genau 30 Ziffern. Prüf sie und gib sie noch einmal ein."
	saysTelefonFormat = "Gib deine Nummer mit Ländervorwahl ein, zum Beispiel +49 151 2345678."
	saysIMAPFehlt     = "Für die E-Mail braucht das Archiv Server, Benutzername und App-Passwort."
	saysIMAPPort      = "Der Port ist eine Zahl zwischen 1 und 65535, meist 993. Lass das Feld leer, dann nimmt das Archiv 993."
	saysZu            = "Das Archiv ist abgeschlossen. Öffne es zuerst, dann kannst du Schlüssel hinterlegen."
)

var errFormat = errors.New("format")

// signalPassphrase keeps only the digits; 30 of them or the sentence.
// whatsappSchluessel reads the 64-digit key of a WhatsApp backup as
// WhatsApp shows it: 64 hex digits in groups of four, any case.
func whatsappSchluessel(s string) ([]byte, error) {
	var b []byte
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			b = append(b, byte(r))
		case r >= 'A' && r <= 'F':
			b = append(b, byte(r-'A'+'a'))
		case r == ' ' || r == '-' || r == '\t' || r == '\n' || r == '\r':
		default:
			return nil, errFormat
		}
	}
	if len(b) != 64 {
		return nil, errFormat
	}
	return b, nil
}

func signalPassphrase(s string) ([]byte, error) {
	var b []byte
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b = append(b, byte(r))
		case r == ' ' || r == '-' || r == '\t':
		default:
			return nil, errFormat
		}
	}
	if len(b) != 30 {
		return nil, errFormat
	}
	return b, nil
}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// telefonnummer makes E.164 of what a person types: spaces, dashes,
// slashes and brackets go; 00 becomes +.
func telefonnummer(s string) (string, error) {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(" -/()\t.", r) {
			return -1
		}
		return r
	}, s)
	if strings.HasPrefix(s, "00") {
		s = "+" + s[2:]
	}
	if !e164.MatchString(s) {
		return "", errFormat
	}
	return s, nil
}

// imapZugang is one of the person's mailboxes. ID names its fetch state
// and its settings in the vault; it never changes while host and user stay.
type imapZugang struct {
	ID       string    `json:"id,omitempty"`
	Name     string    `json:"name,omitempty"` // what the person calls it: „Arbeit“, „GMX“
	Host     string    `json:"host"`
	Port     int       `json:"port,omitempty"` // 0: in Host, or 993
	Benutzer string    `json:"benutzer"`
	Passwort string    `json:"passwort"`
	Am       time.Time `json:"am,omitempty"` // when the person stored the password
}

// kontoErstes is the ID of the one mailbox kept before several were
// possible: its fetch state stays where it was (mail-stand.json).
const kontoErstes = "erstes"

var kontoIDMuster = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

// Anzeige is how the page names the mailbox.
func (z imapZugang) Anzeige() string {
	if z.Name != "" {
		return z.Name
	}
	return z.Benutzer
}

// imapKontenAus reads the stored mailboxes. One object is the single
// mailbox of before (migrated as kontoErstes, unchanged on disk until the
// person changes something); a list is what this version writes.
func imapKontenAus(roh []byte) ([]imapZugang, error) {
	roh = []byte(strings.TrimSpace(string(roh)))
	if len(roh) > 0 && roh[0] == '{' {
		var z imapZugang
		if err := json.Unmarshal(roh, &z); err != nil {
			return nil, err
		}
		z.ID = kontoErstes
		return []imapZugang{z}, nil
	}
	var k []imapZugang
	if err := json.Unmarshal(roh, &k); err != nil {
		return nil, err
	}
	out := k[:0]
	for _, z := range k {
		if !kontoIDMuster.MatchString(z.ID) || z.ID == kontoDokumente {
			log.Printf("archiv: IMAP-Zugang mit ungültiger Kennung übersprungen")
			continue
		}
		out = append(out, z)
	}
	return out, nil
}

// imapKonten are the person's mailboxes; none (and no error) when none is stored.
func imapKonten(s Schluessel) ([]imapZugang, error) {
	roh, err := s.Lesen(SchluesselIMAP)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer nullen(roh)
	return imapKontenAus(roh)
}

func imapKontenSpeichern(s Schluessel, k []imapZugang) error {
	b, err := json.Marshal(k)
	if err != nil {
		return err
	}
	defer nullen(b)
	return s.Speichern(SchluesselIMAP, b)
}

// ohnePasswort: the mailboxes as the page may show them.
func ohnePasswort(k []imapZugang) []imapZugang {
	out := make([]imapZugang, len(k))
	for i, z := range k {
		z.Passwort = ""
		out[i] = z
	}
	return out
}

// imapPort reads the port field: empty is 0 (993, or what Host names).
func imapPort(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, errFormat
	}
	return n, nil
}

// Schluessel is the key folder of an open vault.
type Schluessel struct{ Dir string }

func (s Schluessel) pfad(name string) string {
	if !schluesselNamen[name] {
		panic("unbekannter Schlüssel " + name)
	}
	return filepath.Join(s.Dir, name)
}

func (s Schluessel) Speichern(name string, wert []byte) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return schreibenAtomar(s.pfad(name), wert, 0o600)
}

func (s Schluessel) Lesen(name string) ([]byte, error) {
	b, err := os.ReadFile(s.pfad(name))
	return trimNL(b), err
}

// Hinterlegt says when a key was stored; ok false when it is not.
func (s Schluessel) Hinterlegt(name string) (time.Time, bool) {
	fi, err := os.Stat(s.pfad(name))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// SignalEinziehen moves a Signal passphrase left in the outside store
// into the open vault: written inside atomically (unless the inside
// already holds one, which is newer: the page writes only inside), read
// back, then the outside file is overwritten and removed. Idempotent:
// nothing outside, nothing to do. Called on every opening; on an error the
// outside file stays and the next opening tries again.
func (s Schluessel) SignalEinziehen(innen Innen) error {
	alt := filepath.Join(s.Dir, altSignal)
	roh, err := os.ReadFile(alt)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer nullen(roh)
	wert := trimNL(roh)
	innenDa := false
	if b, err := os.ReadFile(innen.SignalPassphrase()); err == nil {
		innenDa = len(trimNL(b)) > 0
		nullen(b)
	}
	if !innenDa && len(wert) > 0 {
		if err := schreibenAtomar(innen.SignalPassphrase(), wert, 0o600); err != nil {
			return err
		}
		zurueck, err := os.ReadFile(innen.SignalPassphrase())
		if err != nil {
			return err
		}
		gleich := string(trimNL(zurueck)) == string(wert)
		nullen(zurueck)
		if !gleich {
			return errors.New("die Passphrase im Tresor liest sich nicht so zurück, wie sie geschrieben wurde")
		}
	}
	if err := ueberschreibenUndLoeschen(alt); err != nil {
		return err
	}
	if innenDa {
		log.Printf("archiv: alte Signal-Passphrase außerhalb des Tresors gelöscht (im Tresor lag schon eine)")
	} else {
		log.Printf("archiv: Signal-Passphrase in den Tresor übernommen und außerhalb gelöscht")
	}
	return nil
}

// ueberschreibenUndLoeschen fills a file with zeros, syncs it and removes
// it, so its content is not left in the blocks the name pointed to (as far
// as the file system writes in place).
func ueberschreibenUndLoeschen(pfad string) error {
	f, err := os.OpenFile(pfad, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err == nil {
		_, err = f.Write(make([]byte, fi.Size()))
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Remove(pfad)
}

func imapJSON(host, benutzer, passwort string) ([]byte, error) {
	host, benutzer = strings.TrimSpace(host), strings.TrimSpace(benutzer)
	if host == "" || benutzer == "" || passwort == "" {
		return nil, errFormat
	}
	return json.Marshal(imapZugang{Host: host, Benutzer: benutzer, Passwort: passwort})
}

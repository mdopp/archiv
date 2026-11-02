package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The person's keys live as owner-only files in /data/schluessel, OUTSIDE
// the vault (owner, 2026-11-01: the Briefkasten reads the inbox while the
// vault is closed). They are accepted only while the vault is open, so
// only the person sets them, and never shown again: the page says only
// „hinterlegt am …“. Names are fixed; nothing a request says becomes a path.

const (
	SchluesselSignal  = "signal"  // the 30-digit Signal passphrase
	SchluesselTelefon = "telefon" // the person's own number, E.164 (the XML import's owner)
	SchluesselIMAP    = "imap"    // {host, benutzer, passwort} as JSON
)

var schluesselNamen = map[string]bool{SchluesselSignal: true, SchluesselTelefon: true, SchluesselIMAP: true}

// What the key forms answer, one German sentence each.
const (
	saysSignalFormat  = "Die Signal-Passphrase hat genau 30 Ziffern. Prüf sie und gib sie noch einmal ein."
	saysTelefonFormat = "Gib deine Nummer mit Ländervorwahl ein, zum Beispiel +49 151 2345678."
	saysIMAPFehlt     = "Für die E-Mail braucht das Archiv Server, Benutzername und App-Passwort."
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

type imapZugang struct {
	Host     string `json:"host"`
	Benutzer string `json:"benutzer"`
	Passwort string `json:"passwort"`
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

func imapJSON(host, benutzer, passwort string) ([]byte, error) {
	host, benutzer = strings.TrimSpace(host), strings.TrimSpace(benutzer)
	if host == "" || benutzer == "" || passwort == "" {
		return nil, errFormat
	}
	return json.Marshal(imapZugang{Host: host, Benutzer: benutzer, Passwort: passwort})
}

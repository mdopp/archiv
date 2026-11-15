package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/mdopp/archiv/apps/archiv/tresor"
)

// The vault is its own package (tresor/, archiv ADR 0001). This file is
// how the archive's features reach it: each feature belongs to one
// program (programme.go), and a program reaches the vault only through
// its key — per area, read and write separately, every access in the
// vault's protocol. Nothing here knows where the vault is mounted.

type (
	Zustand = tresor.Zustand
	Stand   = tresor.Stand
	Tresor  = tresor.Tresor
)

const (
	Uneingerichtet = tresor.Uneingerichtet
	Verschlossen   = tresor.Verschlossen
	Offen          = tresor.Offen
)

var (
	ErrFalschesPasswort = tresor.ErrFalschesPasswort
	ErrZustand          = tresor.ErrZustand
	schreibenAtomar     = tresor.SchreibenAtomar
)

// Innen is one program's way into the open vault. The zero Innen reaches
// nothing.
type Innen struct{ p *tresor.Programm }

// Fach is one file of the vault: its area and its name there.
type Fach struct {
	b    tresor.Bereich
	name string
}

// The files the features keep in the vault, each in its area
// (tresor/bereiche.go is where the vault decides which name is which).
var (
	fachIdentitaet         = Fach{tresor.Nachrichten, "age-identitaet"}      // the private key of the Briefkasten
	fachWhatsAppSchluessel = Fach{tresor.Nachrichten, "whatsapp-schluessel"} // the 64-digit key: WhatsApp is read only while open
	fachSignalPassphrase   = Fach{tresor.Nachrichten, "signal-passphrase"}   // the 30 digits: Signal is read only while open
	fachWhatsAppStand      = Fach{tresor.Nachrichten, "whatsapp-stand"}      // which database was imported last
	fachMailAufraeumen     = Fach{tresor.Nachrichten, "mail-aufraeumen.json"}
	fachKontakteRelais     = Fach{tresor.Kontakte, "kontakte-relais"}    // the password msgvault logs into the relay with
	fachKontakteVerbunden  = Fach{tresor.Kontakte, "kontakte-verbunden"} // the relay's URL once msgvault is connected to it
	fachSozial             = Fach{tresor.Kontakte, "umfeld/sozial.json"}
	fachImmichSchluessel   = Fach{tresor.Fotos, "immich-schluessel"} // the person's Immich API key
	fachNetz               = Fach{tresor.Beziehungen, "umfeld/netz.json"}
	fachVerborgen          = Fach{tresor.Beziehungen, "umfeld/verborgen.json"}
	fachAntworten          = Fach{tresor.Beziehungen, "umfeld/antworten.json"} // „Wie steht X zu Y?“, fragen.go
	fachGruppen            = Fach{tresor.Beziehungen, "umfeld/gruppen.json"}
	fachGruppenModell      = Fach{tresor.Beziehungen, "umfeld/gruppen-modell.json"}
	fachPaare              = Fach{tresor.Beziehungen, "umfeld/paare.json"}
	fachPaareWeg           = Fach{tresor.Beziehungen, "umfeld/paare-weg.json"}
	fachHerkunft           = Fach{tresor.Beziehungen, "umfeld/herkunft.json"}
	fachHerkunftAntworten  = Fach{tresor.Beziehungen, "umfeld/herkunft-antworten.json"}
	fachVerstorben         = Fach{tresor.Beziehungen, "umfeld/verstorben.json"}
	fachGeburtstage        = Fach{tresor.Beziehungen, "umfeld/geburtstage.json"}
	fachGebHinweise        = Fach{tresor.Beziehungen, "umfeld/geburtstag-hinweise.json"}
	fachAngelegt           = Fach{tresor.Beziehungen, "umfeld/angelegt.json"}
	fachRuhen              = Fach{tresor.Beziehungen, "umfeld/ruhen.json"}
	fachHinweise           = Fach{tresor.Beziehungen, "umfeld/hinweise.json"}
	fachPflegePasst        = Fach{tresor.Notizen, "pflege-passt.json"}
)

// The folders a process works in.
const (
	ordnerMsgvault = "msgvault"        // msgvault's home: its database, its config, its key
	ordnerArbeit   = "arbeit"          // plaintext exports while an import runs, emptied after each
	ordnerGross    = "grosse-anhaenge" // attachments msgvault refuses (MaxAnhang), kept beside it
	ordnerKalender = "kalender"        // one file per calendar, every event ever seen
	ordnerKontakte = "kontakte"        // the archive's contact store
)

func kalenderFach(name string) Fach { return Fach{tresor.Kontakte, path.Join(ordnerKalender, name)} }

var errKeinProgramm = errors.New("kein Zugang zum Tresor")

// auf: the vault is open and the person granted this program something.
func (i Innen) auf() bool {
	if i.p == nil {
		return false
	}
	_, err := i.p.Schluessel()
	return err == nil
}

// darf: the program holds b with r in this session.
func (i Innen) darf(b tresor.Bereich, r tresor.Recht) bool { return i.p != nil && i.p.Darf(b, r) }

func (i Innen) lesen(d Fach) ([]byte, error) {
	if i.p == nil {
		return nil, errKeinProgramm
	}
	return i.p.Lesen(d.b, d.name)
}

func (i Innen) schreiben(d Fach, inhalt []byte) error {
	if i.p == nil {
		return errKeinProgramm
	}
	return i.p.Schreiben(d.b, d.name, inhalt)
}

// seit says when d was written; false when it is missing, empty or not granted.
func (i Innen) seit(d Fach) (time.Time, bool) {
	if i.p == nil {
		return time.Time{}, false
	}
	return i.p.Seit(d.b, d.name)
}

func (i Innen) namen(b tresor.Bereich, ordner string) ([]string, error) {
	if i.p == nil {
		return nil, errKeinProgramm
	}
	return i.p.Namen(b, ordner)
}

// ordner is a folder of b for a process to work in (made if missing).
func (i Innen) ordner(b tresor.Bereich, name string) (string, error) {
	if i.p == nil {
		return "", errKeinProgramm
	}
	return i.p.Ordner(b, tresor.Schreiben, name)
}

// msgvaultSchluessel is msgvault's key for a request that reads (r
// Lesen) or changes (Schreiben) messages; pfad names it in the protocol.
func (i Innen) msgvaultSchluessel(r tresor.Recht, pfad string) (string, error) {
	if i.p == nil {
		return "", errKeinProgramm
	}
	return i.p.MsgvaultSchluessel(r, pfad)
}

// msgvaultRecht is what a request to msgvault does: GET and HEAD read,
// and so does a query (msgvault runs /api/v1/query read only); every
// other request changes messages.
func msgvaultRecht(method, pfad string) tresor.Recht {
	if method == "" || method == http.MethodGet || method == http.MethodHead || pfad == "/api/v1/query" {
		return tresor.Lesen
	}
	return tresor.Schreiben
}

// offen is the program's way in, and whether it leads anywhere now.
func (i Innen) offen() (Innen, bool) { return i, i.auf() }

// jsonLesen fills v from d; false when d is missing, unreadable or not granted.
func (i Innen) jsonLesen(d Fach, v any) bool {
	b, err := i.lesen(d)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

// jsonSchreiben writes v as d.
func (i Innen) jsonSchreiben(d Fach, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return i.schreiben(d, b)
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

// ablageLesen reads a store's file d into v, whose format and version
// fields are format and version, and refuses a format it does not know.
func ablageLesen(innen Innen, d Fach, v any, format *string, version *int, wantF string, wantV int) error {
	b, err := innen.lesen(d)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path.Base(d.name), err)
	}
	if *format != wantF || *version != wantV {
		return fmt.Errorf("%s: Format %q Version %d kennt dieses Archiv nicht", path.Base(d.name), *format, *version)
	}
	return nil
}

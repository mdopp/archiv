package tresor

import (
	"path"
	"strings"
)

// Bereich is one area of the vault (archiv ADR 0001 §1). A program is
// granted areas, read and write separately; every name in the vault
// belongs to exactly one area, and a name in none is the vault's own.
type Bereich string

const (
	Nachrichten Bereich = "nachrichten" // msgvault and what the imports need for it: the messengers' keys, the work folder
	Kontakte    Bereich = "kontakte"    // the phone books, the calendars, the friends of a social export
	Beziehungen Bereich = "beziehungen" // the net: who stands close to whom, the answers, groups, the Stammbaum
	Fotos       Bereich = "fotos"       // the way to the photos and their faces: the person's Immich key
	Notizen     Bereich = "notizen"     // what the person noted on her data: the Datenpfleger's „passt so“
)

// Bereiche in the order the page shows them.
var Bereiche = []Bereich{Nachrichten, Kontakte, Beziehungen, Fotos, Notizen}

// Recht is read, write, or both.
type Recht uint8

const (
	Lesen Recht = 1 << iota
	Schreiben
)

// Hat: r includes all of w.
func (r Recht) Hat(w Recht) bool { return w != 0 && r&w == w }

func (r Recht) String() string {
	switch r {
	case Lesen:
		return "lesen"
	case Schreiben:
		return "schreiben"
	case Lesen | Schreiben:
		return "lesen und schreiben"
	}
	return "nichts"
}

// Rechte are one program's areas.
type Rechte map[Bereich]Recht

// Freigaben are what the person grants for one session, per program.
type Freigaben map[string]Rechte

// eintrag is a name in the vault: a file, or a folder and all below it.
type eintrag struct {
	name   string
	b      Bereich
	ordner bool
}

// eintraege are every name a key may reach. Inside a folder entry a
// key reads and writes single files, except where nurAlsOrdner: msgvault's
// home (its database, its API key) is handed out only whole, as a folder
// for a msgvault process, and only with Schreiben.
var eintraege = []eintrag{
	{"msgvault", Nachrichten, true},
	{"arbeit", Nachrichten, true},
	{"grosse-anhaenge", Nachrichten, true},
	{"age-identitaet", Nachrichten, false},
	{"whatsapp-schluessel", Nachrichten, false},
	{"signal-passphrase", Nachrichten, false},
	{"whatsapp-stand", Nachrichten, false},
	{"mail-aufraeumen.json", Nachrichten, false},
	{"kalender", Kontakte, true},
	{"kontakte", Kontakte, true},
	{"kontakte-relais", Kontakte, false},
	{"kontakte-verbunden", Kontakte, false},
	{"umfeld/sozial.json", Kontakte, false},
	{"immich-schluessel", Fotos, false},
	{"umfeld", Beziehungen, true},
	{"pflege-passt.json", Notizen, false},
}

var nurAlsOrdner = map[string]bool{"msgvault": true}

// sauber is name as a relative path inside the vault, or "".
func sauber(name string) string {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return ""
	}
	c := path.Clean(name)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return ""
	}
	return c
}

// bereichVon is the area of a name, the entry it falls under, and false
// for a name in no area (the vault's own, or none at all). The longest
// entry decides: umfeld/sozial.json is Kontakte, the rest of umfeld/ is
// Beziehungen.
func bereichVon(name string) (eintrag, bool) {
	c := sauber(name)
	if c == "" {
		return eintrag{}, false
	}
	var best eintrag
	gefunden := false
	for _, e := range eintraege {
		passt := c == e.name || e.ordner && strings.HasPrefix(c, e.name+"/")
		if passt && len(e.name) > len(best.name) {
			best, gefunden = e, true
		}
	}
	return best, gefunden
}

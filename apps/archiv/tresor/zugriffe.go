package tresor

import (
	"encoding/json"
	"log"
	"os"
	"sort"
	"sync"
	"time"
)

// The vault's protocol (ADR 0001 §1): every access of a key, allowed or
// refused, is one line in zugriffe.jsonl inside the vault — what it names
// is the area and the file, never what was read or written. Closed, the
// protocol is as unreadable as the rest. A refusal while the vault is
// closed has nowhere to go inside; the supervisor's log gets the program
// and the area, nothing more.

const (
	protokollName  = "zugriffe.jsonl"
	protokollAlt   = "zugriffe.1.jsonl"
	protokollGross = 8 << 20 // then the older half moves to protokollAlt
	letzteZugriffe = 50
)

// Zugriff is one line of the protocol.
type Zugriff struct {
	Am         time.Time `json:"am"`
	Sitzung    string    `json:"sitzung,omitempty"` // the first characters of the session's id
	Programm   string    `json:"programm"`
	Bereich    Bereich   `json:"bereich,omitempty"`
	Recht      string    `json:"recht,omitempty"`
	Was        string    `json:"was,omitempty"`
	Verweigert string    `json:"verweigert,omitempty"` // why it was refused; empty: allowed
}

// Zaehler counts one program's accesses to one area in this session.
type Zaehler struct {
	Programm   string
	Bereich    Bereich
	Recht      string
	Erlaubt    int
	Verweigert int
}

type zugriffe struct {
	mu     sync.Mutex
	datei  string // "" while closed
	f      *os.File
	zahlen map[[3]string]*Zaehler
	letzte []Zugriff
}

func (z *zugriffe) beginnen(datei string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.datei, z.zahlen, z.letzte = datei, map[[3]string]*Zaehler{}, nil
	z.oeffnen()
}

func (z *zugriffe) oeffnen() {
	f, err := os.OpenFile(z.datei, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("archiv: Protokoll des Tresors: %v", err)
		return
	}
	z.f = f
}

func (z *zugriffe) enden() {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.f != nil {
		z.f.Close()
	}
	z.f, z.datei, z.zahlen, z.letzte = nil, "", nil, nil
}

func (z *zugriffe) notieren(e Zugriff) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.datei == "" {
		if e.Verweigert != "" {
			log.Printf("archiv: Zugriff verweigert, der Tresor ist zu: %s, %s %s", e.Programm, e.Bereich, e.Recht)
		}
		return
	}
	if e.Bereich != "" {
		k := [3]string{e.Programm, string(e.Bereich), e.Recht}
		c := z.zahlen[k]
		if c == nil {
			c = &Zaehler{Programm: e.Programm, Bereich: e.Bereich, Recht: e.Recht}
			z.zahlen[k] = c
		}
		if e.Verweigert == "" {
			c.Erlaubt++
		} else {
			c.Verweigert++
		}
	}
	z.letzte = append(z.letzte, e)
	if len(z.letzte) > letzteZugriffe {
		z.letzte = z.letzte[len(z.letzte)-letzteZugriffe:]
	}
	if z.f == nil {
		return
	}
	b, _ := json.Marshal(e)
	if _, err := z.f.Write(append(b, '\n')); err != nil {
		log.Printf("archiv: Protokoll des Tresors: %v", err)
	}
	if fi, err := z.f.Stat(); err == nil && fi.Size() > protokollGross {
		z.f.Close()
		z.f = nil
		alt := z.datei[:len(z.datei)-len(protokollName)] + protokollAlt
		if err := os.Rename(z.datei, alt); err != nil {
			log.Printf("archiv: Protokoll des Tresors: %v", err)
		}
		z.oeffnen()
	}
}

// Uebersicht is this session's protocol as the page shows it: the counts
// per program and area, and the last accesses, newest first.
type Uebersicht struct {
	Zaehler []Zaehler
	Letzte  []Zugriff
}

// Zugriffe is the open session's overview; empty while closed.
func (t *Tresor) Zugriffe() Uebersicht {
	z := &t.prot
	z.mu.Lock()
	defer z.mu.Unlock()
	var u Uebersicht
	for _, c := range z.zahlen {
		u.Zaehler = append(u.Zaehler, *c)
	}
	sort.Slice(u.Zaehler, func(i, j int) bool {
		a, b := u.Zaehler[i], u.Zaehler[j]
		return a.Programm+"\x00"+string(a.Bereich)+"\x00"+a.Recht < b.Programm+"\x00"+string(b.Bereich)+"\x00"+b.Recht
	})
	for i := len(z.letzte) - 1; i >= 0; i-- {
		u.Letzte = append(u.Letzte, z.letzte[i])
	}
	return u
}

// sitzungProtokoll notes the session's beginning and end in the protocol.
func (t *Tresor) sitzungProtokoll(was string) {
	t.mu.Lock()
	id := ""
	if t.sitzung != nil {
		id = t.sitzung.id
	}
	jetzt := t.now()
	t.mu.Unlock()
	t.prot.notieren(Zugriff{Am: jetzt, Sitzung: kurz(id), Programm: "tresor", Was: was})
}

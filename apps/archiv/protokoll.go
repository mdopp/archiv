package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// Protokoll is what the archive keeps outside the vault (no secret, no
// message): when which backup became a packet, when which packet went
// into msgvault, the last error as a sentence, since when it is closed.
// It feeds the page while the vault is closed and the status file.

// Zahlen are what one import into msgvault brought.
type Zahlen struct {
	SMS      int `json:"sms"`
	MMS      int `json:"mms"`
	Anrufe   int `json:"anrufe"`
	Anhaenge int `json:"anhaenge"`
}

func (z Zahlen) Nachrichten() int { return z.SMS + z.MMS }

// Eingelesen is the last packet of a source that went into msgvault.
type Eingelesen struct {
	Am     time.Time `json:"am"`
	Datei  string    `json:"datei"` // the packet
	Zahlen Zahlen    `json:"zahlen"`
	Anzahl int       `json:"anzahl"` // packets imported since the vault was made
}

// Gepackt is the last backup of a source that became a packet.
type Gepackt struct {
	Am          time.Time `json:"am"`
	Sicherung   string    `json:"sicherung"`
	Paket       string    `json:"paket,omitempty"` // empty: nothing new in it
	Nachrichten int       `json:"nachrichten"`
	Anhaenge    int       `json:"anhaenge"`
}

// Fehler is the last thing that went wrong, as the person reads it.
type Fehler struct {
	Am     time.Time `json:"am"`
	Quelle string    `json:"quelle"`
	Datei  string    `json:"datei,omitempty"`
	Satz   string    `json:"satz"`
}

type Protokoll struct {
	Datei string

	mu            sync.Mutex
	LetzterImport map[string]*Eingelesen `json:"letzter_import"`
	LetztesPaket  map[string]*Gepackt    `json:"letztes_paket"`
	LetzterFehler *Fehler                `json:"letzter_fehler,omitempty"`
	ZuSeit        *time.Time             `json:"zu_seit,omitempty"`
}

func LadeProtokoll(datei string) *Protokoll {
	p := &Protokoll{Datei: datei}
	if b, err := os.ReadFile(datei); err == nil {
		if err := json.Unmarshal(b, p); err != nil {
			log.Printf("archiv: %s: %v (beginne neu)", datei, err)
		}
	}
	if p.LetzterImport == nil {
		p.LetzterImport = map[string]*Eingelesen{}
	}
	if p.LetztesPaket == nil {
		p.LetztesPaket = map[string]*Gepackt{}
	}
	return p
}

func (p *Protokoll) speichern() {
	b, _ := json.MarshalIndent(p, "", "  ")
	if err := schreibenAtomar(p.Datei, b, 0o644); err != nil {
		log.Printf("archiv: %s: %v", p.Datei, err)
	}
}

func (p *Protokoll) fehlerLoesen(quelle, datei string) {
	if p.LetzterFehler != nil && p.LetzterFehler.Quelle == quelle && p.LetzterFehler.Datei == datei {
		p.LetzterFehler = nil
	}
}

func (p *Protokoll) Eingelesen(quelle, datei string, z Zahlen, am time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	if alt := p.LetzterImport[quelle]; alt != nil {
		n = alt.Anzahl
	}
	p.LetzterImport[quelle] = &Eingelesen{Am: am, Datei: datei, Zahlen: z, Anzahl: n + 1}
	p.fehlerLoesen(quelle, datei)
	p.speichern()
}

func (p *Protokoll) Gepackt(quelle string, g Gepackt) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.LetztesPaket[quelle] = &g
	p.fehlerLoesen(quelle, g.Sicherung)
	p.speichern()
}

func (p *Protokoll) Fehlschlag(f Fehler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.LetzterFehler = &f
	p.speichern()
}

// FehlerWeg forgets the last error of a source (a new key was given).
func (p *Protokoll) FehlerWeg(quelle string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.LetzterFehler != nil && p.LetzterFehler.Quelle == quelle {
		p.LetzterFehler = nil
		p.speichern()
	}
}

func (p *Protokoll) Zu(am time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ZuSeit = &am
	p.speichern()
}

// Kopie is a snapshot for readers.
func (p *Protokoll) Kopie() (map[string]Eingelesen, *Fehler, *time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := map[string]Eingelesen{}
	for k, v := range p.LetzterImport {
		m[k] = *v
	}
	var f *Fehler
	if p.LetzterFehler != nil {
		x := *p.LetzterFehler
		f = &x
	}
	var zu *time.Time
	if p.ZuSeit != nil {
		x := *p.ZuSeit
		zu = &x
	}
	return m, f, zu
}

func (p *Protokoll) Pakete() map[string]Gepackt {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := map[string]Gepackt{}
	for k, v := range p.LetztesPaket {
		m[k] = *v
	}
	return m
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"filippo.io/age"
)

// The Postbote brings the Briefkasten's packets into msgvault while the
// vault is open: oldest first, each decrypted with the private key from
// the vault, its attachments unpacked into the vault's work folder,
// written as an SMS Backup & Restore XML there, imported, and deleted.
// Additive only: msgvault upserts every message by a stable id, and
// import-synctech-sms has no option that deletes; nothing here runs
// anything else. The work folder is emptied after every packet.

const (
	saysPaketKaputt  = "Das Paket %s ließ sich nicht öffnen. Es bleibt im Briefkasten; sag dem Admin Bescheid."
	saysUebernahme   = "Das Archiv konnte die Nachrichten aus %s nicht übernehmen. Das Paket bleibt im Briefkasten; beim nächsten Öffnen versucht es das Archiv noch einmal."
	saysKeineNummer  = "Pakete warten, aber deine Handynummer fehlt. Gib sie unter „Schlüssel“ ein."
	saysAnderesPaket = "Das Paket %s hat ein Format, das dieses Archiv noch nicht kennt. Es bleibt im Briefkasten."
)

var importiert = regexp.MustCompile(`Imported (\d+) SMS, (\d+) MMS, (\d+) calls, (\d+) attachments`)

type Postbote struct {
	Post       string     // /data/briefkasten
	Schluessel Schluessel // outside: the number
	Protokoll  *Protokoll
	Run        Ausfuehrer
	Msgvault   string
	Takt       time.Duration
	Arbeit     func() func() // Tresor.Arbeit: no idle close while one is delivered
	Geaendert  func()
	Now        func() time.Time

	mu      sync.Mutex
	laeuft  string
	zurueck map[string]bool
}

func (p *Postbote) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Postbote) Laeuft() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.laeuft
}

func (p *Postbote) geaendert() {
	if p.Geaendert != nil {
		p.Geaendert()
	}
}

// Lauf delivers until ctx ends (the vault closes): at once, then every Takt.
func (p *Postbote) Lauf(ctx context.Context, innen Innen) {
	p.mu.Lock()
	p.zurueck = nil
	p.mu.Unlock()
	takt := p.Takt
	if takt <= 0 {
		takt = 20 * time.Second
	}
	for {
		p.Runde(ctx, innen)
		select {
		case <-ctx.Done():
			return
		case <-time.After(takt):
		}
	}
}

func (p *Postbote) Runde(ctx context.Context, innen Innen) {
	pakete := Pakete(p.Post)
	if len(pakete) == 0 {
		return
	}
	tel, err := p.Schluessel.Lesen(SchluesselTelefon)
	if err != nil {
		return // the page says it: saysKeineNummer
	}
	id, err := identitaetLesen(innen.Identitaet())
	if err != nil {
		log.Printf("archiv: der private Schlüssel fehlt im Tresor: %v", err)
		return
	}
	for _, name := range pakete {
		if ctx.Err() != nil {
			return
		}
		p.mu.Lock()
		weg := p.zurueck[name]
		p.mu.Unlock()
		if weg {
			continue
		}
		if err := p.Zustellen(ctx, innen, id, name, string(tel)); err != nil && ctx.Err() == nil {
			p.mu.Lock()
			if p.zurueck == nil {
				p.zurueck = map[string]bool{}
			}
			p.zurueck[name] = true
			p.mu.Unlock()
		}
	}
}

// Zustellen imports one packet and deletes it. Its plaintext exists only
// inside the vault's work folder, and only for this call.
func (p *Postbote) Zustellen(ctx context.Context, innen Innen, id age.Identity, name, telefon string) (err error) {
	if p.Arbeit != nil {
		defer p.Arbeit()()
	}
	p.mu.Lock()
	p.laeuft = name
	p.mu.Unlock()
	p.geaendert()
	defer func() {
		p.mu.Lock()
		p.laeuft = ""
		p.mu.Unlock()
		p.geaendert()
	}()
	quelle := "signal"
	fehler := func(satz string, grund error) error {
		log.Printf("archiv: Paket %s: %v", name, grund)
		if ctx.Err() == nil {
			p.Protokoll.Fehlschlag(Fehler{Am: p.now(), Quelle: quelle, Datei: name, Satz: satz})
		}
		return grund
	}

	arbeit := filepath.Join(innen.Arbeit(), "paket-"+zufall())
	defer os.RemoveAll(arbeit)
	xml := arbeit + ".xml"
	defer os.Remove(xml)
	if err := os.MkdirAll(filepath.Join(arbeit, "anhaenge"), 0o700); err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}

	f, err := os.Open(filepath.Join(p.Post, name))
	if err != nil {
		return fehler(fmt.Sprintf(saysPaketKaputt, name), err)
	}
	var nachrichten []Nachricht
	m, err := PaketLesen(f, id,
		func(datei string, r io.Reader) error {
			out, err := os.OpenFile(filepath.Join(arbeit, "anhaenge", datei), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, r)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			return err
		},
		func(n Nachricht) error { nachrichten = append(nachrichten, n); return nil })
	f.Close()
	if m.Quelle != "" {
		quelle = m.Quelle
	}
	if errors.Is(err, ErrPaketFormat) {
		return fehler(fmt.Sprintf(saysAnderesPaket, name), err)
	}
	if err != nil {
		return fehler(fmt.Sprintf(saysPaketKaputt, name), err)
	}

	out, err := os.OpenFile(xml, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}
	x, err := NeuesXML(out, telefon)
	for i := 0; err == nil && i < len(nachrichten); i++ {
		err = x.Nachricht(nachrichten[i], func(a Anhang) (io.ReadCloser, error) {
			return os.Open(filepath.Join(arbeit, "anhaenge", filepath.Base(a.Datei)))
		})
	}
	if err == nil {
		err = x.Schliessen()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}

	a, err := p.Run.Run(ctx, msgvaultImportSMS(p.Msgvault, innen.Home(), xml, telefon))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && a.Code != 0 {
		err = fmt.Errorf("msgvault import-synctech-sms Code %d: %s", a.Code, fehlerzeilen(a.Text))
	}
	if err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}
	var z Zahlen
	if m := importiert.FindStringSubmatch(a.Text); m != nil {
		z.SMS, _ = strconv.Atoi(m[1])
		z.MMS, _ = strconv.Atoi(m[2])
		z.Anrufe, _ = strconv.Atoi(m[3])
		z.Anhaenge, _ = strconv.Atoi(m[4])
	}
	if err := os.Remove(filepath.Join(p.Post, name)); err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}
	log.Printf("archiv: Paket %s eingelesen: %d SMS, %d MMS, %d Anhänge", name, z.SMS, z.MMS, z.Anhaenge)
	p.Protokoll.Eingelesen(quelle, name, z, p.now())
	return nil
}

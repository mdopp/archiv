package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Abgleich (owner, 2026-11-02: contacts and calendars go from the
// phone into the archive) runs while the vault is open: at the opening
// and then every few hours (KONTAKTE_TAKT). It reads, never writes:
//
//   - the person's calendars google-* and privat-* from Radicale into the
//     vault's calendar store (kalender.go);
//   - the address book handy-kontakte into msgvault: once connected
//     (add-carddav to the relay, relais.go; the book subscribed and a
//     lookup source, never a write target), sync-carddav brings its
//     changes, and msgvault turns contacts into persons, so numbers and
//     addresses across WhatsApp, Signal, SMS and mail resolve to names.
//
// Without RADICALE_URL/RADICALE_USER it does nothing and the page says
// the box has not set it up.

const (
	saysAdressbuchLeer      = "Dein Adressbuch „Handy-Kontakte“ ist auf der Box noch leer. Übertrage deine Kontakte in der Homes-App: Mehr → Nachrichten sichern → „Kontakte übertragen“."
	saysKontakteVerbinden   = "Das Archiv konnte dein Adressbuch nicht anbinden. Es versucht es beim nächsten Abgleich wieder; hilft das nicht, sag dem Admin Bescheid."
	saysKontakteAbgleich    = "Der Abgleich deiner Kontakte hat nicht geklappt. Es versucht es beim nächsten Abgleich wieder; hilft das nicht, sag dem Admin Bescheid."
	saysKalenderLesen       = "Ein Kalender ließ sich nicht lesen (%s). Was schon im Archiv ist, bleibt; beim nächsten Abgleich versucht es das Archiv wieder."
	saysRelaisFehlt         = "Das Archiv kann dein Adressbuch gerade nicht an die Suche weitergeben. Sag dem Admin Bescheid."
	kontakteSyncZeileMuster = `CardDAV sync: (\d+) books, (\d+) created, (\d+) updated, (\d+) removed`
)

var kontakteSyncZeile = regexp.MustCompile(kontakteSyncZeileMuster)

type KontakteStand struct {
	Am        time.Time // the last sync that went through
	Verbunden bool
	Leer      bool        // handy-kontakte does not exist yet, and the archive has no card
	Karten    int         // cards in the archive's store
	Weg       []Geloescht // of them, deleted on the phone (newest first)
	Neu       int
	Geaendert int
	Entfernt  int
	Fehler    string
}

// Geloescht is a contact deleted on the phone, kept in the archive.
type Geloescht struct {
	Name string
	Am   time.Time
}

type AbgleichStand struct {
	Laeuft         bool
	Fehler         string // Radicale itself: not set up, login refused, out of reach
	Kontakte       KontakteStand
	Kalender       []KalenderZahl
	KalenderAm     time.Time
	KalenderFehler string
	Personen       int // persons in msgvault after the last sync
}

type Abgleich struct {
	Radicale  *Radicale // nil: not set up
	Konfig    error     // why it is not set up, if the environment is wrong
	Relais    *Relais
	API       *MsgvaultAPI
	Run       Ausfuehrer
	Msgvault  string
	Takt      time.Duration
	Arbeit    func() func() // Tresor.Arbeit: the vault does not close by idleness while it runs
	Geaendert func()
	Now       func() time.Time

	mu    sync.Mutex
	stand AbgleichStand
	jetzt chan struct{}
}

func (a *Abgleich) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Abgleich) geaendert() {
	if a.Geaendert != nil {
		a.Geaendert()
	}
}

// Stand is a copy for the page.
func (a *Abgleich) Stand() AbgleichStand {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.stand
	s.Kalender = append([]KalenderZahl(nil), a.stand.Kalender...)
	return s
}

func (a *Abgleich) setzen(f func(*AbgleichStand)) {
	a.mu.Lock()
	f(&a.stand)
	a.mu.Unlock()
	a.geaendert()
}

// Eingerichtet: the box gave the archive its Radicale login.
func (a *Abgleich) Eingerichtet() bool { return a != nil && a.Radicale != nil }

// Vorbereiten is Tresor.Vorbereiten: the relay's part before msgvault starts.
func (a *Abgleich) Vorbereiten(innen Innen) error {
	if !a.Eingerichtet() || a.Relais == nil {
		return nil
	}
	return a.Relais.Vorbereiten(innen)
}

// Jetzt asks for a round at once (the button on the page).
func (a *Abgleich) Jetzt() {
	a.mu.Lock()
	if a.jetzt == nil {
		a.jetzt = make(chan struct{}, 1)
	}
	c := a.jetzt
	a.mu.Unlock()
	select {
	case c <- struct{}{}:
	default:
	}
}

// Lauf runs while the vault is open: the relay, a round at once, then
// one every Takt.
func (a *Abgleich) Lauf(ctx context.Context, innen Innen) {
	if !a.Eingerichtet() {
		if a.Konfig != nil {
			log.Printf("archiv: Kontakte und Kalender: %v", a.Konfig)
		}
		return
	}
	a.mu.Lock()
	if a.jetzt == nil {
		a.jetzt = make(chan struct{}, 1)
	}
	jetzt := a.jetzt
	a.mu.Unlock()
	if zahlen, err := kalenderZahlen(innen); err == nil {
		a.setzen(func(s *AbgleichStand) { s.Kalender = zahlen })
	}
	var wg sync.WaitGroup
	if a.Relais != nil {
		// listening before the first round: add-carddav dials it at once
		ln, err := a.Relais.Lauschen()
		if err != nil {
			log.Printf("archiv: Relais für die Kontakte: %v", err)
			a.setzen(func(s *AbgleichStand) { s.Kontakte.Fehler = saysRelaisFehlt })
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := a.Relais.Dienen(ctx, ln); err != nil {
					log.Printf("archiv: Relais für die Kontakte: %v", err)
					a.setzen(func(s *AbgleichStand) { s.Kontakte.Fehler = saysRelaisFehlt })
				}
			}()
		}
	}
	defer wg.Wait()
	takt := a.Takt
	if takt <= 0 {
		takt = 3 * time.Hour
	}
	for {
		a.Runde(ctx, innen)
		select {
		case <-ctx.Done():
			return
		case <-jetzt:
		case <-time.After(takt):
		}
	}
}

// kontakteZaehlen: how many cards the store holds, and which are gone on the phone.
func (a *Abgleich) kontakteZaehlen(k *Kontaktablage) {
	var weg []Geloescht
	for _, c := range k.Karten {
		if c.GeloeschtAm != nil {
			weg = append(weg, Geloescht{Name: c.Name, Am: *c.GeloeschtAm})
		}
	}
	sort.Slice(weg, func(i, j int) bool { return weg[i].Am.After(weg[j].Am) })
	a.setzen(func(s *AbgleichStand) { s.Kontakte.Leer, s.Kontakte.Karten, s.Kontakte.Weg = false, len(k.Karten), weg })
}

// GeloeschtAm says when the card with this UID was deleted on the phone
// (for the page's persons); ok false while it is there or unknown.
func GeloeschtNachUID(innen Innen) map[string]time.Time {
	out := map[string]time.Time{}
	k, err := kontakteLaden(kontakteDatei(innen))
	if err != nil {
		return out
	}
	for _, c := range k.Karten {
		if c.GeloeschtAm != nil && c.UID != "" {
			out[c.UID] = *c.GeloeschtAm
		}
	}
	return out
}

// Runde: the calendars, then the contacts.
func (a *Abgleich) Runde(ctx context.Context, innen Innen) {
	if a.Arbeit != nil {
		defer a.Arbeit()()
	}
	a.setzen(func(s *AbgleichStand) { s.Laeuft = true })
	defer a.setzen(func(s *AbgleichStand) { s.Laeuft = false })
	samml, err := a.Radicale.Sammlungen(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		satz := saysRadicaleWeg
		switch {
		case errors.Is(err, errRadicaleAnmeldung):
			satz = saysRadicaleAnmeldung
		case errors.Is(err, errPasswortFehlt):
			satz = saysRadicaleFehlt
		}
		log.Printf("archiv: Radicale: %v", err)
		a.setzen(func(s *AbgleichStand) { s.Fehler = satz })
		return
	}
	a.setzen(func(s *AbgleichStand) { s.Fehler = "" })
	a.kalender(ctx, innen, samml)
	if ctx.Err() == nil {
		a.kontakte(ctx, innen, samml)
	}
}

func (a *Abgleich) kalender(ctx context.Context, innen Innen, samml []Sammlung) {
	jetzt := a.now()
	da := map[string]bool{}
	var fehler []string
	for _, s := range samml {
		if !s.KalenderZumLesen() || ctx.Err() != nil {
			continue
		}
		da[s.Name] = true
		ics, err := a.Radicale.KalenderHolen(ctx, s.Name)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("archiv: Kalender %s: %v", s.Name, err)
				fehler = append(fehler, s.Name)
			}
			continue
		}
		datei := kalenderDatei(innen, s.Name)
		k, err := kalenderLaden(datei)
		if errors.Is(err, os.ErrNotExist) {
			k, err = &Kalenderablage{Format: kalenderFormat, Version: kalenderVersion, Name: s.Name, Termine: map[string]*Termin{}}, nil
		}
		if err != nil {
			log.Printf("archiv: Kalender %s im Tresor: %v (bleibt, wie er ist)", s.Name, err)
			fehler = append(fehler, s.Name)
			continue
		}
		if s.Anzeige != "" {
			k.Anzeige = s.Anzeige
		}
		neu, geaendert, weg := k.Einmischen(ics, jetzt)
		if err := k.speichern(datei); err != nil {
			log.Printf("archiv: Kalender %s speichern: %v", s.Name, err)
			fehler = append(fehler, s.Name)
			continue
		}
		if neu+geaendert+weg > 0 {
			log.Printf("archiv: Kalender %s: %d neu, %d geändert, %d an der Quelle gelöscht", s.Name, neu, geaendert, weg)
		}
	}
	// a calendar gone from Radicale: its events stay, the calendar is marked
	if ctx.Err() == nil {
		eintraege, _ := os.ReadDir(innen.Kalender())
		for _, e := range eintraege {
			name := strings.TrimSuffix(e.Name(), ".json")
			if e.IsDir() || name == e.Name() || strings.HasPrefix(name, ".") || da[name] {
				continue
			}
			datei := filepath.Join(innen.Kalender(), e.Name())
			k, err := kalenderLaden(datei)
			if err != nil || k.QuelleWegSeit != nil {
				continue
			}
			g := jetzt
			k.QuelleWegSeit = &g
			if err := k.speichern(datei); err == nil {
				log.Printf("archiv: Kalender %s ist in Radicale nicht mehr da; seine Termine bleiben im Archiv", name)
			}
		}
	}
	zahlen, zerr := kalenderZahlen(innen)
	if zerr != nil {
		log.Printf("archiv: Kalender im Tresor: %v", zerr)
	}
	satz := ""
	if len(fehler) > 0 {
		satz = fmt.Sprintf(saysKalenderLesen, strings.Join(fehler, ", "))
	}
	a.setzen(func(s *AbgleichStand) {
		s.Kalender, s.KalenderFehler = zahlen, satz
		if len(fehler) == 0 {
			s.KalenderAm = jetzt
		}
	})
}

func (a *Abgleich) kontakte(ctx context.Context, innen Innen, samml []Sammlung) {
	da := false
	for _, s := range samml {
		if s.Name == adressbuchName && s.Adressbuch {
			da = true
		}
	}
	// first into the archive's own store: every card ever seen stays
	datei := kontakteDatei(innen)
	ablage, err := kontakteLaden(datei)
	if errors.Is(err, os.ErrNotExist) {
		ablage, err = nil, nil
	}
	if err != nil {
		log.Printf("archiv: Kontakte im Tresor: %v (bleiben, wie sie sind)", err)
		a.setzen(func(s *AbgleichStand) { s.Kontakte.Fehler = saysKontakteAbgleich })
		return
	}
	switch {
	case da:
		karten, err := a.Radicale.AdressbuchHolen(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("archiv: Adressbuch lesen: %v", err)
				a.setzen(func(s *AbgleichStand) { s.Kontakte.Fehler = saysKontakteAbgleich })
			}
			return
		}
		if ablage == nil {
			ablage = neueKontaktablage()
		}
		neu, geaendert, weg := ablage.Einmischen(karten, a.now())
		if err := ablage.speichern(datei); err != nil {
			log.Printf("archiv: Kontakte speichern: %v", err)
			a.setzen(func(s *AbgleichStand) { s.Kontakte.Fehler = saysKontakteAbgleich })
			return
		}
		if neu+geaendert+weg > 0 {
			log.Printf("archiv: Kontakte: %d neu, %d geändert, %d auf dem Handy gelöscht (bleiben im Archiv)", neu, geaendert, weg)
		}
	case ablage != nil && ablage.QuelleWegSeit == nil: // the book is gone: its cards stay as they were
		g := a.now()
		ablage.QuelleWegSeit = &g
		ablage.speichern(datei)
		log.Printf("archiv: das Adressbuch %s ist in Radicale nicht mehr da; die Kontakte bleiben im Archiv", adressbuchName)
	}
	if ablage == nil {
		a.setzen(func(s *AbgleichStand) { s.Kontakte.Leer, s.Kontakte.Fehler = true, "" })
		return
	}
	a.kontakteZaehlen(ablage)
	if a.Relais == nil {
		return
	}
	home := innen.Home()
	pw := a.Relais.Passwort()
	defer nullen(pw)
	if len(pw) == 0 {
		a.setzen(func(s *AbgleichStand) { s.Kontakte.Fehler = saysRelaisFehlt })
		return
	}
	scheitern := func(satz, was string, aus Ausgabe, err error) {
		if ctx.Err() != nil {
			return
		}
		log.Printf("archiv: Kontakte %s: %v Code %d: %s", was, err, aus.Code, schwaerzen(strings.TrimSpace(aus.Text), pw))
		a.setzen(func(s *AbgleichStand) { s.Kontakte.Fehler = satz })
	}
	marke, _ := os.ReadFile(innen.KontakteVerbunden())
	if string(trimNL(marke)) != a.Relais.BasisURL() {
		aus, err := a.Run.Run(ctx, msgvaultAddCardDAV(a.Msgvault, home, a.Relais.BasisURL(), relaisBenutzer, pw))
		if err != nil || aus.Code != 0 {
			scheitern(saysKontakteVerbinden, "anbinden", aus, err)
			return
		}
		if err := schreibenAtomar(innen.KontakteVerbunden(), []byte(a.Relais.BasisURL()+"\n"), 0o600); err != nil {
			log.Printf("archiv: Kontakte: %v", err)
		}
		log.Printf("archiv: Kontakte: msgvault ist mit dem Adressbuch verbunden (über das Relais, nur lesend)")
	}
	a.setzen(func(s *AbgleichStand) { s.Kontakte.Verbunden = true })
	// the roles, every round: subscribed and lookup source, never the write target
	if a.API != nil {
		buecher, err := a.API.Buecher(ctx)
		if err != nil {
			scheitern(saysKontakteAbgleich, "Adressbücher lesen", Ausgabe{}, err)
			return
		}
		for _, b := range buecher {
			if !strings.HasSuffix(strings.TrimSuffix(b.URL, "/"), a.Radicale.Home()+adressbuchName) {
				continue
			}
			if b.WriteTarget || !b.Subscribed || !b.LookupSource {
				aus, err := a.Run.Run(ctx, msgvaultBuchRollen(a.Msgvault, home, b.ID))
				if err != nil || aus.Code != 0 {
					scheitern(saysKontakteAbgleich, "Rollen setzen", aus, err)
					return
				}
			}
		}
	}
	aus, err := a.Run.Run(ctx, msgvaultSyncCardDAV(a.Msgvault, home))
	if err != nil || aus.Code != 0 {
		scheitern(saysKontakteAbgleich, "abgleichen", aus, err)
		return
	}
	alt := a.Stand().Kontakte
	k := KontakteStand{Am: a.now(), Verbunden: true, Karten: alt.Karten, Weg: alt.Weg}
	if m := kontakteSyncZeile.FindStringSubmatch(aus.Text); m != nil {
		k.Neu, _ = strconv.Atoi(m[2])
		k.Geaendert, _ = strconv.Atoi(m[3])
		k.Entfernt, _ = strconv.Atoi(m[4])
	}
	personen := -1
	if a.API != nil {
		if p, err := a.API.Personen(ctx); err == nil {
			personen = len(p)
		}
	}
	a.setzen(func(s *AbgleichStand) {
		s.Kontakte = k
		if personen >= 0 {
			s.Personen = personen
		}
	})
}

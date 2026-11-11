package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Umfeld (archiv#27, step 1; owner 2026-11-10, homes ADR 0002): the
// person's contact net moves from Homes into the archive. It is computed
// and kept only inside the open vault (Innen.Umfeld): the photos read
// from Immich with the person's own key, the phone books, the net and
// who is hidden. Nothing derived lies in /data; the status file carries
// only when it was computed. A round runs at the opening, then every
// UMFELD_TAKT (default an hour), and when the key is entered; a closed
// vault drops the net from memory.
//
// It reads, never writes: Immich (search/metadata, api-keys/me,
// users/me), the archive's own contact store (handy-kontakte), through
// the archive's Radicale login the household book homes/haushalt-kontakte,
// and msgvault's group chats and mail threads (gespraeche.go) and the
// messages with a relation word, which it asks the box's model about
// (hinweise.go).

const (
	netzFormat             = "archiv-umfeld"
	netzVersion            = 1
	haushaltBuch           = "/homes/haushalt-kontakte/"
	saysHaushaltNichtLesen = "Das Haushalts-Adressbuch darf das Archiv noch nicht lesen; Adressen, Firmen und Beziehungen von dort fehlen deshalb im Umfeld. Das Leserecht richtet der Admin der Box ein."
	saysHaushaltWeg        = "Das Haushalts-Adressbuch war gerade nicht zu erreichen; das Umfeld nimmt, was es zuletzt davon gelesen hat."
	saysUmfeldSpeichern    = "Das Umfeld ließ sich im Tresor nicht speichern. Sag dem Admin Bescheid."
)

var errUmfeldZu = errors.New("Umfeld: kein Netz, der Tresor ist zu oder es ist noch nichts gerechnet")

func (i Innen) ImmichSchluessel() string { return filepath.Join(i.Wurzel, "immich-schluessel") } // the person's Immich API key, only inside
func (i Innen) Umfeld() string           { return filepath.Join(i.Wurzel, "umfeld") }            // the photos, the phone books' ties, who is hidden
func (i Innen) umfeldTabelle() string    { return filepath.Join(i.Umfeld(), "netz.json") }
func (i Innen) umfeldVerborgen() string  { return filepath.Join(i.Umfeld(), "verborgen.json") }
func (i Innen) umfeldAntworten() string  { return filepath.Join(i.Umfeld(), "antworten.json") } // „Wie steht X zu Y?“, fragen.go

// UmfeldStand is what the page shows of the rounds.
type UmfeldStand struct {
	Laeuft  bool
	Seit    time.Time // the round running now began
	Gelesen int       // photos read so far in it
	Am      time.Time // the last round that read Immich through
	Fotos   int       // photos with people in the table
	Fehler  string    // Immich, as a sentence
	Hinweis string    // the household book, as a sentence
	// what the box's model read (hinweise.go): messages with a relation
	// word read in all, the candidates still waiting, and whether a round
	// counted them since the opening
	ModellGelesen, ModellOffen int
	ModellGezaehlt             bool
}

// Umfeld runs the rounds and holds the net while the vault is open.
type Umfeld struct {
	URL string // IMMICH_URL; "" not set up
	// Oeffentlich is IMMICH_PUBLIC_URL, Immich as the person's browser
	// reaches it: the edge page's photos open there; "" no link.
	Oeffentlich string
	Radicale    *Radicale    // for the household book; nil: not set up
	Msgvault    *MsgvaultAPI // the group chats and mail threads (gespraeche.go); nil: not read
	Modell      *Modell      // the box's model, for the relation hints (hinweise.go); nil: none
	Schluessel  Schluessel   // the person's own number (outside), to find their card
	// Takt is UMFELD_TAKT. A round is not Tresor.Arbeit: hourly rounds
	// would keep the vault from ever closing by idleness (LEERLAUF, 2 h);
	// a round cut short by the closing ends with its context.
	Takt      time.Duration
	Geaendert func()
	Loc       *time.Location
	Now       func() time.Time

	mu     sync.Mutex
	halt   sync.Mutex // an answer or a hiding kept and a round's new net, one at a time: neither loses the other
	stand  UmfeldStand
	netz   *Netz // nil: closed, or nothing computed yet
	jetzt  chan struct{}
	besuch time.Time // when the person's visit began, for its portion of questions (Netz.Portion)
}

// Besuch is when the person's visit began: a new one with neu (she came
// to the page from outside), and the first page after the vault opened.
func (u *Umfeld) Besuch(neu bool) time.Time {
	if u == nil {
		return time.Time{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if neu || u.besuch.IsZero() {
		u.besuch = u.now()
	}
	return u.besuch
}

func (u *Umfeld) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Umfeld) geaendert() {
	if u.Geaendert != nil {
		u.Geaendert()
	}
}

// Eingerichtet: the box gave the archive Immich's address.
func (u *Umfeld) Eingerichtet() bool { return u != nil && u.URL != "" }

// Stand is a copy for the page and the status.
func (u *Umfeld) Stand() UmfeldStand {
	if u == nil {
		return UmfeldStand{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.stand
}

// Netz is the net while the vault is open; nil else.
func (u *Umfeld) Netz() *Netz {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.netz
}

func (u *Umfeld) setzen(f func(*UmfeldStand)) {
	u.mu.Lock()
	f(&u.stand)
	u.mu.Unlock()
	u.geaendert()
}

// Jetzt asks for a round at once (a new key).
func (u *Umfeld) Jetzt() {
	u.mu.Lock()
	if u.jetzt == nil {
		u.jetzt = make(chan struct{}, 1)
	}
	c := u.jetzt
	u.mu.Unlock()
	select {
	case c <- struct{}{}:
	default:
	}
}

// HatImmichSchluessel says when the key was stored in the open vault.
func HatImmichSchluessel(innen Innen) (time.Time, bool) {
	fi, err := os.Stat(innen.ImmichSchluessel())
	if err != nil || fi.Size() == 0 {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// Lauf runs while the vault is open: the kept net at once, a round, then
// one every Takt or when asked. It ends with the vault and forgets the net.
func (u *Umfeld) Lauf(ctx context.Context, innen Innen) {
	if !u.Eingerichtet() {
		return
	}
	defer u.vergessen()
	u.mu.Lock()
	if u.jetzt == nil {
		u.jetzt = make(chan struct{}, 1)
	}
	jetzt := u.jetzt
	u.mu.Unlock()
	u.laden(innen)
	takt := cmp.Or(u.Takt, time.Hour)
	for {
		if _, ok := HatImmichSchluessel(innen); ok {
			u.Runde(ctx, innen)
		}
		select {
		case <-ctx.Done():
			return
		case <-jetzt:
		case <-time.After(takt):
		}
	}
}

// vergessen: the vault closes, the net leaves memory (the time stays, for the status).
func (u *Umfeld) vergessen() {
	u.mu.Lock()
	u.netz, u.besuch = nil, time.Time{}
	u.stand = UmfeldStand{Am: u.stand.Am}
	u.mu.Unlock()
	u.geaendert()
}

// laden builds the net kept in the vault, if there is one.
func (u *Umfeld) laden(innen Innen) {
	tab, err := tabelleLaden(innen)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("archiv: Umfeld im Tresor: %v (wird neu gerechnet)", err)
		}
		return
	}
	u.halt.Lock()
	n := u.netzBauen(innen, tab)
	u.mu.Lock()
	u.netz = n
	u.stand.Am, u.stand.Fotos = tab.Geholt, len(tab.Fotos)
	u.mu.Unlock()
	u.halt.Unlock()
	u.geaendert()
}

// netzBauen is the net of tab with what the vault keeps beside it: who is
// hidden, the answers, the groups' categories and what the model read.
func (u *Umfeld) netzBauen(innen Innen, tab netzTabelle) *Netz {
	n := neuesNetz(tab, verborgenLaden(innen), antwortenLaden(innen), u.Loc, u.Now)
	n.gruppen = gruppenLaden(innen)
	n.gruppenModell = gruppenModellLaden(innen)
	n.gebSetzen(gebHinweiseLaden(innen), geburtstageLaden(innen))
	n.hinweiseSetzen(hinweiseLaden(innen))
	n.herkunftSetzen(herkunftLaden(innen), herkunftAntwortenLaden(innen))
	n.paareSetzen(paareLaden(innen), paareWegLaden(innen))
	n.sozialSetzen(sozialLaden(innen))
	n.angelegtSetzen(angelegtLaden(innen))
	return n
}

func tabelleLaden(innen Innen) (netzTabelle, error) {
	var tab netzTabelle
	b, err := os.ReadFile(innen.umfeldTabelle())
	if err != nil {
		return tab, err
	}
	if err := json.Unmarshal(b, &tab); err != nil {
		return tab, err
	}
	if tab.Format != netzFormat || tab.Version != netzVersion {
		return tab, fmt.Errorf("Format %q Version %d kennt dieses Archiv nicht", tab.Format, tab.Version)
	}
	return tab, nil
}

func verborgenLaden(innen Innen) map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(innen.umfeldVerborgen()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func antwortenLaden(innen Innen) map[string]antwort {
	out := map[string]antwort{}
	if b, err := os.ReadFile(innen.umfeldAntworten()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func jsonImTresor(pfad string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pfad), 0o700); err != nil {
		return err
	}
	return schreibenAtomar(pfad, b, 0o600)
}

// Runde reads Immich and the phone books again whole and rebuilds the
// net from them (never changed in place: a merge, a rename or a deletion
// in Immich cannot drift). Immich out of reach: its photos as before.
func (u *Umfeld) Runde(ctx context.Context, innen Innen) {
	key, err := os.ReadFile(innen.ImmichSchluessel())
	if err != nil || len(trimNL(key)) == 0 {
		return
	}
	defer nullen(key)
	u.setzen(func(s *UmfeldStand) { s.Laeuft, s.Seit, s.Gelesen = true, u.now(), 0 })
	defer u.setzen(func(s *UmfeldStand) { s.Laeuft = false })

	alt, _ := tabelleLaden(innen)
	tab := netzTabelle{Format: netzFormat, Version: netzVersion, Geholt: alt.Geholt, Fotos: alt.Fotos, Namen: alt.Namen, Haushalt: alt.Haushalt, Geb: alt.Geb, Gespraeche: alt.Gespraeche}
	im := &Immich{URL: u.URL, Key: string(trimNL(key))}
	as, err := im.Aufnahmen(ctx, func(n int) { u.setzen(func(s *UmfeldStand) { s.Gelesen = n }) })
	if ctx.Err() != nil {
		return
	}
	fehler := ""
	if err != nil {
		var fehlt immichFehltRecht
		switch {
		case errors.Is(err, errImmichSchluessel):
			fehler = saysImmichNichtMehr
		case errors.As(err, &fehlt):
			fehler = saysRechteFehlen([]string{fehlt.Recht})
		default:
			fehler = saysImmichLesen
		}
		log.Printf("archiv: Umfeld: Immich: %v", err)
	} else {
		tab.Geholt, tab.Fotos, tab.Namen, tab.Geb = u.now(), nil, map[string]string{}, map[string]string{}
		for _, a := range as {
			f := netzFoto{ID: a.ID, Lat: a.Exif.Lat, Lng: a.Exif.Lng, Stadt: strings.TrimSpace(a.Exif.Stadt)}
			f.T, _ = time.Parse(time.RFC3339Nano, a.Exif.Zeit)
			for _, p := range a.Personen {
				if p.ID == "" || p.Versteckt {
					continue
				}
				f.Wer = append(f.Wer, p.ID)
				if name := strings.Join(strings.Fields(p.Name), " "); name != "" {
					tab.Namen[p.ID] = name
					tab.Geb[p.ID] = cmp.Or(gebLesen(p.Geb), tab.Geb[p.ID])
				}
			}
			if len(f.Wer) > 0 && !f.T.IsZero() {
				tab.Fotos = append(tab.Fotos, f)
			}
		}
	}
	email := ""
	if err == nil {
		email = im.Wessen(ctx)
	}
	hinweis := u.haushalt(ctx, &tab)
	if ctx.Err() != nil {
		return
	}
	ks := u.telefonbuch(innen, &tab, email)
	u.gespraeche(ctx, &tab, ks)
	if ctx.Err() != nil {
		return
	}
	if tab.Ich == "" && err != nil { // Immich out of reach: its e-mail found the person's card last time
		tab.Ich = alt.Ich
	}
	if tab.Namen == nil {
		tab.Namen = map[string]string{}
	}
	if serr := jsonImTresor(innen.umfeldTabelle(), tab); serr != nil {
		log.Printf("archiv: Umfeld speichern: %v", serr)
		fehler = cmp.Or(fehler, saysUmfeldSpeichern)
	}
	u.halt.Lock()
	n := u.netzBauen(innen, tab)
	u.mu.Lock()
	u.netz = n
	u.halt.Unlock()
	u.stand.Fehler, u.stand.Hinweis, u.stand.Fotos = fehler, hinweis, len(tab.Fotos)
	if err == nil {
		u.stand.Am = tab.Geholt
	}
	u.mu.Unlock()
	u.geaendert()
	if err == nil {
		personen, kanten := n.Groesse()
		log.Printf("archiv: Umfeld gerechnet: %d Fotos mit Personen, %d Personen, %d Verbindungen", len(tab.Fotos), personen, kanten)
	}
	u.geburtstageRunde(ctx, innen, n, ks)
	u.gruppenRunde(ctx, innen, n)
	u.paareRunde(ctx, innen, n, ks)
	u.hinweiseRunde(ctx, innen, n, ks)
	u.herkunftRunde(ctx, innen, n, ks)
}

// haushalt reads the household book into tab.Haushalt; on a refusal or
// an error the cards last read stay, and the sentence says why.
func (u *Umfeld) haushalt(ctx context.Context, tab *netzTabelle) string {
	if u.Radicale == nil {
		return ""
	}
	karten, err := u.Radicale.BuchHolen(ctx, haushaltBuch)
	switch {
	case err == nil:
		tab.Haushalt = nil
		for _, k := range karten {
			tab.Haushalt = append(tab.Haushalt, k.Daten)
		}
		return ""
	case errors.Is(err, errBuchFehlt):
		tab.Haushalt = nil
		return ""
	case errors.Is(err, errBuchVerboten), errors.Is(err, errRadicaleAnmeldung):
		log.Printf("archiv: Umfeld: Haushalts-Adressbuch: %v", err)
		return saysHaushaltNichtLesen
	default:
		if ctx.Err() == nil {
			log.Printf("archiv: Umfeld: Haushalts-Adressbuch: %v", err)
		}
		return saysHaushaltWeg
	}
}

// telefonbuch makes the contacts and the ties of both books, each contact
// with its Immich person (the one of the same name on the most photos;
// Homes gives each contact the person of its name), and finds the
// person's own card: the one with the Immich account's e-mail, else the
// one with their number; the household's first. It answers the cards read,
// for matching the chats' and mails' participants.
func (u *Umfeld) telefonbuch(innen Innen, tab *netzTabelle, email string) []buchKarte {
	var ks []buchKarte
	for _, text := range tab.Haushalt {
		ks = append(ks, kartenLesen("haushalt/", text)...)
	}
	if store, err := kontakteLaden(kontakteDatei(innen)); err == nil {
		for _, c := range store.Sortiert() {
			if c.GeloeschtAm == nil { // the phone's book as it is now
				ks = append(ks, kartenLesen("privat/", c.Daten)...)
			}
		}
	}
	je := map[string]int{}
	for _, f := range tab.Fotos {
		for _, id := range f.Wer {
			je[id]++
		}
	}
	nachName := map[string]string{}
	for id, name := range tab.Namen {
		k := klar(name)
		if alt, da := nachName[k]; !da || je[id] > je[alt] || je[id] == je[alt] && id < alt {
			nachName[k] = id
		}
	}
	tab.Kontakte = nil
	for _, k := range ks {
		tab.Kontakte = append(tab.Kontakte, netzKontakt{Key: k.key, Name: k.name, Person: nachName[klar(k.name)], Family: k.family, Geb: k.geb, Strassen: k.strassen, Mails: k.emails, Firma: k.firma, Spitz: k.spitz})
	}
	nummer, _ := u.Schluessel.Lesen(SchluesselTelefon)
	tab.Ich = eigeneKarte(ks, func(k buchKarte) bool {
		return email != "" && slices.ContainsFunc(k.emails, func(m string) bool { return strings.EqualFold(m, email) })
	})
	if tab.Ich == "" && len(nummer) > 0 {
		tab.Ich = eigeneKarte(ks, func(k buchKarte) bool {
			return slices.ContainsFunc(k.tels, func(t string) bool { return gleicheEndziffern(t, string(nummer)) })
		})
	}
	tab.IchAuch = selbstKarten(ks, tab.Ich)
	tab.Fest = append(gruppen(ks), beziehungen(ks)...)
	return ks
}

// selbstKarten are the keys of the other cards that are the person's too
// (owner 2026-11-11: she was „Steht nahe“'s first): the same name
// (umlauts alike), or a number or an address of her own card.
func selbstKarten(ks []buchKarte, ich string) (out []string) {
	i := slices.IndexFunc(ks, func(k buchKarte) bool { return k.key == ich })
	if i < 0 {
		return nil
	}
	eigen := ks[i]
	for _, k := range ks {
		if k.key == ich {
			continue
		}
		gleich := klar(k.name) == klar(eigen.name) ||
			slices.ContainsFunc(k.tels, func(t string) bool {
				return slices.ContainsFunc(eigen.tels, func(e string) bool { return gleicheEndziffern(t, e) })
			}) ||
			slices.ContainsFunc(k.emails, func(m string) bool {
				return slices.ContainsFunc(eigen.emails, func(e string) bool { return strings.EqualFold(strings.TrimSpace(m), strings.TrimSpace(e)) })
			})
		if gleich {
			out = append(out, k.key)
		}
	}
	return out
}

// eigeneKarte is the first card that is the person's, a household card
// over a phone's.
func eigeneKarte(ks []buchKarte, ist func(buchKarte) bool) string {
	ich := ""
	for _, k := range ks {
		if ist(k) && (ich == "" || strings.HasPrefix(k.key, "haushalt/") && !strings.HasPrefix(ich, "haushalt/")) {
			ich = k.key
		}
	}
	return ich
}

// gleicheEndziffern compares two numbers by their last nine digits: a
// card writes 0151 …, the archive keeps +49 151 ….
func gleicheEndziffern(a, b string) bool {
	x, y := ziffern(a), ziffern(b)
	if len(x) < 7 || len(y) < 7 {
		return false
	}
	n := min(9, len(x), len(y))
	return x[len(x)-n:] == y[len(y)-n:]
}

// Verbergen hides id in the open vault (Aus, Nicht) or shows it again ("").
func (u *Umfeld) Verbergen(innen Innen, id, art string) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	return jsonImTresor(innen.umfeldVerborgen(), n.Verbergen(id, art))
}

// Beantworten keeps the answer about x and y in the open vault (fragen.go).
func (u *Umfeld) Beantworten(innen Innen, x, y, art string) (Frage, error) {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return Frage{}, errUmfeldZu
	}
	f, alle, err := n.Beantworten(x, y, art)
	if err != nil {
		return f, err
	}
	return f, jsonImTresor(innen.umfeldAntworten(), alle)
}

// Eintragen keeps in the open vault what Homes wrote of the answer key.
func (u *Umfeld) Eintragen(innen Innen, key string, e *Eingetragen) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	alle, err := n.Eintragen(key, e)
	if err != nil {
		return err
	}
	return jsonImTresor(innen.umfeldAntworten(), alle)
}

// Vergessen drops the answer about the pair key from the open vault.
func (u *Umfeld) Vergessen(innen Innen, key string) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	alle, err := n.Vergessen(key)
	if err != nil {
		return err
	}
	return jsonImTresor(innen.umfeldAntworten(), alle)
}

// Einordnen keeps the person's category of the group id in the open vault (gruppen.go).
func (u *Umfeld) Einordnen(innen Innen, id, kat string) (Frage, error) {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return Frage{}, errUmfeldZu
	}
	f, alle, err := n.Einordnen(id, kat)
	if err != nil {
		return f, err
	}
	return f, jsonImTresor(innen.umfeldGruppen(), alle)
}

// Ausordnen drops the category of the group id from the open vault.
func (u *Umfeld) Ausordnen(innen Innen, id string) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	alle, err := n.Ausordnen(id)
	if err != nil {
		return err
	}
	return jsonImTresor(innen.umfeldGruppen(), alle)
}

// Angelegt keeps in the open vault what f did to the persons made by hand
// and to the answers (angelegt.go).
func (u *Umfeld) Angelegt(innen Innen, f func(n *Netz) (map[string]antwort, map[string]handPerson, error)) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	alle, ps, err := f(n)
	if err != nil {
		return err
	}
	if err := jsonImTresor(innen.umfeldAntworten(), alle); err != nil {
		return err
	}
	return jsonImTresor(innen.umfeldAngelegt(), ps)
}

// Festlegen keeps the relation of x and y the edge page set, asked or not (kante.go).
func (u *Umfeld) Festlegen(innen Innen, x, y, art string) (Frage, error) {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return Frage{}, errUmfeldZu
	}
	f, alle, err := n.Festlegen(x, y, art)
	if err != nil {
		return f, err
	}
	return f, jsonImTresor(innen.umfeldAntworten(), alle)
}

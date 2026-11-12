package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The Datenpfleger (archiv#58, owner 2026-11-12): while the vault is
// open, the archive finds what keeps the person's data from being clean
// and complete, with the access it already has — the contacts and
// calendars it reads from Radicale, the photos it reads with the person's
// Immich key, the birthdays the person confirmed from mail. Rules only, no
// model. Each finding is one screen with the step in the app that owns the
// data (the archive writes no book, AGENTS.md; no writer for these fields
// exists on the archive's way to Homes, which takes relations only) and
// „Passt so“, which the vault remembers: that finding never returns. An
// aggregate (the photos, the doubled numbers) remembers each of its parts,
// so only a new part brings it back.
//
// Not here: documents (the archive has no Paperless access, and none is
// added without the owner's card), duplicate cards (Google's job, owner
// 2026-11-11), unnamed faces (Homes' „Wer ist das?“), subscribed calendars
// that stopped (Radicale does not say a calendar is a subscription).

const (
	bereichKontakte = "Kontakte"
	bereichKalender = "Kalender"
	bereichFotos    = "Fotos"

	pflegeFrisch = 5 * time.Minute // the count for Homes is this old at most; the page counts anew

	saysNummerDoppeltSchritt = "Das räumt Homes für alle Karten auf einmal auf: Bitte den Admin, „homes nummern-bereinigen“ laufen zu lassen. Beim nächsten Abgleich ist es dann auch hier weg."
	saysOhneNameSchritt      = "Öffne den Kontakt in den Kontakten deines Handys und gib ihm einen Namen, oder lösch ihn dort, wenn du ihn nicht mehr brauchst."
	saysVorwahlSchritt       = "Öffne den Kontakt in den Kontakten deines Handys und setz die Vorwahl vor die Nummer (oder +49 …). Sonst erreicht sie dich nur aus demselben Ortsnetz."
	saysTerminSchritt        = "Lösch einen der Termine in dem Kalender, aus dem sie kommen (Google Kalender oder dein Handy). Beim nächsten Abgleich ist es hier weg."
	saysOhneDatumSchritt     = "Öffne jedes Foto in Immich, tipp auf ⓘ und beim Datum auf den Stift. Ohne Datum kann dein Archiv die Fotos keinem Tag zuordnen."
	saysOhneOrtSchritt       = "Öffne jedes Foto in Immich, tipp auf ⓘ und bei „Ort hinzufügen“ auf den Stift. Ohne Ort weiß dein Archiv nicht, wo ihr zusammen wart."
	saysGebKarteSchritt      = "Trag den Geburtstag in deinem Handy bei diesem Kontakt ein. Im Archiv steht er schon; ins Telefonbuch schreibt das Archiv selbst nichts."
	saysGebImmichSchritt     = "Diese Person hat keinen Kontakt im Telefonbuch: Trag den Geburtstag in Immich bei ihr ein (Person öffnen, ⋮, Geburtsdatum)."
)

// Befund is one finding: one screen.
type Befund struct {
	ID      string
	Bereich string
	Titel   string
	Zeilen  []string // what it is about, one line each
	Schritt string   // the step in the app that owns the data
	Teile   []string // an aggregate's parts; „Passt so“ remembers each
	Bilder  bool     // the parts are Immich photos
}

// --- Kontakte

// telWert is a vCard TEL's number, without a vCard 4 „tel:“.
func telWert(s string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "tel:"))
}

// nurNummer: a name that is only a number („+49 171 …“).
func nurNummer(name string) bool {
	return !strings.ContainsFunc(name, unicode.IsLetter) && len(ziffern(name)) >= 3
}

// ohneVorwahl: a number of six to eight digits that starts with neither
// 0 nor + (a local number); shorter ones are service numbers, longer ones
// a country code written without its +.
func ohneVorwahl(tel string) bool {
	s := telWert(tel)
	d := ziffern(s)
	return s != "" && s[0] >= '1' && s[0] <= '9' && len(d) >= 6 && len(d) <= 8
}

// doppelteNummer: the card carries one number twice, in any spelling.
func doppelteNummer(tels []string) bool {
	for i, a := range tels {
		if slices.ContainsFunc(tels[i+1:], func(b string) bool { return ziffern(a) == ziffern(b) || gleicheEndziffern(a, b) }) {
			return true
		}
	}
	return false
}

// kontaktBefunde are the phone book's findings: a card without a name,
// a number without an area code (one each), and every card with a number
// twice as one (Homes fixes them all at once).
func kontaktBefunde(k *Kontaktablage) (out []Befund) {
	var doppelt Befund
	for _, c := range k.Sortiert() {
		if c.GeloeschtAm != nil {
			continue
		}
		tels := vcardWerte(c.Daten, "TEL")
		name := strings.TrimSpace(c.Name)
		wer := cmp.Or(name, "Ein Kontakt ohne Namen")
		if name == "" || nurNummer(name) {
			out = append(out, Befund{ID: "ohne-name:" + c.Href, Bereich: bereichKontakte, Titel: "Ein Kontakt ohne Namen",
				Zeilen: append(slices.Clone(tels), vcardWerte(c.Daten, "EMAIL")...), Schritt: saysOhneNameSchritt})
		}
		if ohne := slices.DeleteFunc(slices.Clone(tels), func(t string) bool { return !ohneVorwahl(t) }); len(ohne) > 0 {
			out = append(out, Befund{ID: "vorwahl:" + c.Href, Bereich: bereichKontakte, Titel: wer + ": Nummer ohne Vorwahl", Zeilen: ohne, Schritt: saysVorwahlSchritt})
		}
		if doppelteNummer(tels) {
			doppelt.Teile = append(doppelt.Teile, c.Href)
			doppelt.Zeilen = append(doppelt.Zeilen, wer)
		}
	}
	if len(doppelt.Teile) > 0 {
		doppelt.ID, doppelt.Bereich, doppelt.Titel, doppelt.Schritt = "nummer-doppelt", bereichKontakte, "Kontakte mit einer Nummer doppelt", saysNummerDoppeltSchritt
		out = append(out, doppelt)
	}
	return out
}

// --- Kalender

// kalenderAlle are the vault's calendars, by name.
func kalenderAlle(innen Innen) (out []*Kalenderablage, err error) {
	eintraege, err := os.ReadDir(innen.Kalender())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var fehler []string
	for _, e := range eintraege {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		k, err := kalenderLaden(filepath.Join(innen.Kalender(), e.Name()))
		if err != nil {
			fehler = append(fehler, err.Error())
			continue
		}
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b *Kalenderablage) int { return strings.Compare(a.Name, b.Name) })
	if len(fehler) > 0 {
		return out, errors.New(strings.Join(fehler, "; "))
	}
	return out, nil
}

// beginnText is a DTSTART as the page says it: „12.11.2026, 10:00“.
func beginnText(beginn string) string {
	_, v := wert(beginn)
	v = cmp.Or(v, beginn)
	for _, f := range [][2]string{{"20060102T150405Z", "02.01.2006, 15:04"}, {"20060102T150405", "02.01.2006, 15:04"}, {"20060102", "02.01.2006"}} {
		if t, err := time.Parse(f[0], v); err == nil {
			return t.Format(f[1])
		}
	}
	return v
}

// kalenderBefunde: in one calendar, events of the same title at the same
// start under different UIDs (a copy imported twice), each group one.
func kalenderBefunde(ks []*Kalenderablage) (out []Befund) {
	for _, k := range ks {
		if k.QuelleWegSeit != nil {
			continue
		}
		gruppen := map[string][]string{}
		for _, t := range k.Termine {
			titel := strings.Join(strings.Fields(t.Titel), " ")
			if t.GeloeschtAm != nil || t.RecurrenceID != "" || titel == "" || t.Beginn == "" {
				continue
			}
			g := strings.ToLower(titel) + "\x00" + t.Beginn
			gruppen[g] = append(gruppen[g], t.UID)
		}
		for _, g := range slices.Sorted(maps.Keys(gruppen)) {
			uids := gruppen[g]
			if len(uids) < 2 {
				continue
			}
			slices.Sort(uids)
			titel, beginn, _ := strings.Cut(g, "\x00")
			erster := k.Termine[(Termin{UID: uids[0]}).schluessel()]
			if erster != nil {
				titel = strings.Join(strings.Fields(erster.Titel), " ")
			}
			name := kalenderName(KalenderZahl{Name: k.Name, Anzeige: k.Anzeige})
			out = append(out, Befund{ID: "termin-doppelt:" + k.Name + ":" + strings.Join(uids, "|"), Bereich: bereichKalender,
				Titel: fmt.Sprintf("„%s“ steht %d-mal im Kalender", titel, len(uids)), Zeilen: []string{beginnText(beginn), name}, Schritt: saysTerminSchritt})
		}
	}
	return out
}

// --- Fotos and birthdays, from the net

// fotoBefunde: the photos with people the net cannot use fully — without
// a date (it skips them) or without a place — each kind one finding.
func fotoBefunde(n *Netz) (out []Befund) {
	ohneDatum, ohneOrt := n.FotoLuecken()
	if len(ohneDatum) > 0 {
		out = append(out, Befund{ID: "fotos-ohne-datum", Bereich: bereichFotos, Titel: "Fotos ohne Aufnahmedatum", Teile: ohneDatum, Bilder: true, Schritt: saysOhneDatumSchritt})
	}
	if len(ohneOrt) > 0 {
		out = append(out, Befund{ID: "fotos-ohne-ort", Bereich: bereichFotos, Titel: "Fotos ohne Ort", Teile: ohneOrt, Bilder: true, Schritt: saysOhneOrtSchritt})
	}
	return out
}

// FotoLuecken are the photos with people without a date, and those with
// a date but without a place (no coordinates, no town).
func (n *Netz) FotoLuecken() (ohneDatum, ohneOrt []string) {
	if n == nil {
		return nil, nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, f := range n.st.Fotos {
		if f.Lat == nil && f.Lng == nil && f.Stadt == "" {
			ohneOrt = append(ohneOrt, f.ID)
		}
	}
	return slices.Clone(n.st.OhneDatum), ohneOrt
}

// gebBefunde: a birthday the person confirmed from the wishes in their
// mail and messages that no card and no Immich person carries.
func gebBefunde(n *Netz) (out []Befund) {
	for _, g := range n.GeburtstageOhneBuch() {
		schritt := saysGebKarteSchritt
		if g.Wer.Kontakt == "" {
			schritt = saysGebImmichSchritt
		}
		out = append(out, Befund{ID: "geburtstag:" + g.Wer.ID, Bereich: bereichKontakte, Titel: "Geburtstag von " + g.Wer.Name + " fehlt im Telefonbuch",
			Zeilen: []string{"Du hast bestätigt: " + tagText(g.Tag)}, Schritt: schritt})
	}
	return out
}

// GeburtstageOhneBuch are the confirmed birthdays no card or Immich has.
func (n *Netz) GeburtstageOhneBuch() (out []GebBestaetigt) {
	for _, g := range n.Geburtstage() {
		n.mu.Lock()
		leer := n.merkmale[g.Wer.ID].geb.IsZero()
		n.mu.Unlock()
		if leer {
			out = append(out, g)
		}
	}
	return out
}

// --- „Passt so“, and all findings together

func (i Innen) pflegePasst() string { return filepath.Join(i.Wurzel, "pflege-passt.json") }

func passtLaden(innen Innen) map[string]time.Time {
	m := map[string]time.Time{}
	if b, err := os.ReadFile(innen.pflegePasst()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// passtTeil is the key „Passt so“ keeps for one part of an aggregate.
func passtTeil(id, teil string) string { return id + "#" + teil }

// PasstSo remembers a finding as the person left it: its id, or each of
// an aggregate's parts.
func PasstSo(innen Innen, b Befund, jetzt time.Time) error {
	m := passtLaden(innen)
	if len(b.Teile) == 0 {
		m[b.ID] = jetzt
	}
	for _, t := range b.Teile {
		m[passtTeil(b.ID, t)] = jetzt
	}
	roh, _ := json.Marshal(m)
	return schreibenAtomar(innen.pflegePasst(), roh, 0o600)
}

// ohnePasst drops what the person left as it is; an aggregate keeps only
// its new parts (and its lines with them, where it has one per part).
func ohnePasst(bs []Befund, passt map[string]time.Time) (out []Befund) {
	for _, b := range bs {
		if _, da := passt[b.ID]; da {
			continue
		}
		if len(b.Teile) > 0 {
			var teile, zeilen []string
			for i, t := range b.Teile {
				if _, da := passt[passtTeil(b.ID, t)]; !da {
					teile = append(teile, t)
					if len(b.Zeilen) == len(b.Teile) {
						zeilen = append(zeilen, b.Zeilen[i])
					}
				}
			}
			if len(teile) == 0 {
				continue
			}
			if len(b.Zeilen) == len(b.Teile) {
				b.Zeilen = zeilen
			}
			b.Teile = teile
		}
		out = append(out, b)
	}
	return out
}

// Befunde are all open findings of the open vault, in the order the page
// goes through them: Kontakte, Kalender, Fotos.
func Befunde(innen Innen, n *Netz) []Befund {
	var bs []Befund
	if k, err := kontakteLaden(kontakteDatei(innen)); err == nil {
		bs = append(bs, kontaktBefunde(k)...)
	}
	bs = append(bs, gebBefunde(n)...)
	ks, _ := kalenderAlle(innen)
	bs = append(bs, kalenderBefunde(ks)...)
	bs = append(bs, fotoBefunde(n)...)
	return ohnePasst(bs, passtLaden(innen))
}

// pflegeZahl is the count for the start and for Homes, at most
// pflegeFrisch old; frisch counts anew.
type pflegeZahl struct {
	mu   sync.Mutex
	am   time.Time
	zahl int
}

func (p *pflegeZahl) zaehlen(innen Innen, n *Netz, jetzt time.Time, frisch bool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if frisch || p.am.IsZero() || jetzt.Sub(p.am) >= pflegeFrisch || jetzt.Before(p.am) {
		p.zahl, p.am = len(Befunde(innen, n)), jetzt
	}
	return p.zahl
}

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The contact store (owner, 2026-11-02: „Warum sollte der Kontakt auch
// gelöscht werden? Warum nicht nur mit gelöscht im Archiv lassen?“). The
// archive keeps every card it has ever seen in the person's address book
// handy-kontakte, inside the vault (kontakte/handy-kontakte.json), keyed
// by the card's UID: while the card exists at the source its newest
// version wins (earlier versions are kept); a card gone at the source
// stays, marked deleted, and comes back unmarked if it reappears.
// msgvault's sync-carddav mirrors the book it reads, so the relay
// (relais.go) serves msgvault this store, never Radicale's book: a card
// deleted on the phone keeps being served, with an extra
// CATEGORIES:Auf dem Handy gelöscht am <Datum>, and its person in msgvault
// never disappears.

const (
	kontakteFormat     = "archiv-kontakte"
	kontakteVersion    = 1
	geloeschtKategorie = "Auf dem Handy gelöscht am "
)

// Karte is one contact card as the archive keeps it.
type Karte struct {
	UID            string     `json:"uid,omitempty"`
	Name           string     `json:"name,omitempty"` // FN, for the page
	Href           string     `json:"href"`           // the file name the relay serves it under, fixed at first sight
	Daten          string     `json:"daten"`          // the vCard as the source last had it
	ErstGesehen    time.Time  `json:"erst_gesehen"`
	ZuletztGesehen time.Time  `json:"zuletzt_gesehen"`
	Geaendert      *time.Time `json:"geaendert,omitempty"`
	Frueher        []string   `json:"frueher,omitempty"`
	GeloeschtAm    *time.Time `json:"auf_dem_handy_geloescht_am,omitempty"`
}

// Kontaktablage is the store's file in the vault.
type Kontaktablage struct {
	Format         string            `json:"format"`
	Version        int               `json:"version"`
	ZuletztGelesen time.Time         `json:"zuletzt_gelesen"`
	QuelleWegSeit  *time.Time        `json:"quelle_weg_seit,omitempty"` // the whole book is gone from Radicale
	Karten         map[string]*Karte `json:"karten"`
}

func kontakteDatei(innen Innen) string {
	return filepath.Join(innen.Wurzel, "kontakte", adressbuchName+".json")
}

func kontakteLaden(datei string) (*Kontaktablage, error) {
	b, err := os.ReadFile(datei)
	if err != nil {
		return nil, err
	}
	var k Kontaktablage
	if err := json.Unmarshal(b, &k); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(datei), err)
	}
	if k.Format != kontakteFormat || k.Version != kontakteVersion {
		return nil, fmt.Errorf("%s: Format %q Version %d kennt dieses Archiv nicht", filepath.Base(datei), k.Format, k.Version)
	}
	if k.Karten == nil {
		k.Karten = map[string]*Karte{}
	}
	return &k, nil
}

func neueKontaktablage() *Kontaktablage {
	return &Kontaktablage{Format: kontakteFormat, Version: kontakteVersion, Karten: map[string]*Karte{}}
}

func (k *Kontaktablage) speichern(datei string) error {
	b, err := json.Marshal(k)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(datei), 0o700); err != nil {
		return err
	}
	return schreibenAtomar(datei, b, 0o600)
}

// QuellKarte is a card as Radicale has it now.
type QuellKarte struct {
	Href  string // the member's file name in the book
	Daten string
}

var hrefSicher = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+=-]{0,200}$`)

// Einmischen merges the source's cards. Nothing is removed: new cards
// come in, a changed card keeps its earlier version, a card gone at the
// source is marked deleted (and unmarked when it comes back).
func (k *Kontaktablage) Einmischen(quelle []QuellKarte, jetzt time.Time) (neu, geaendert, weg int) {
	if k.Karten == nil {
		k.Karten = map[string]*Karte{}
	}
	hrefs := map[string]bool{}
	for _, c := range k.Karten {
		hrefs[c.Href] = true
	}
	da := map[string]bool{}
	for _, q := range quelle {
		daten := vcardNormal(q.Daten)
		uid, name := vcardFeld(daten, "UID"), vcardFeld(daten, "FN")
		key := "uid:" + uid
		if uid == "" {
			key = "href:" + q.Href
		}
		if da[key] {
			continue // the same UID twice in one book: the first wins
		}
		da[key] = true
		alt, ok := k.Karten[key]
		if !ok {
			href := q.Href
			if !hrefSicher.MatchString(href) || hrefs[href] {
				h := sha256.Sum256([]byte(key))
				href = hex.EncodeToString(h[:12]) + ".vcf"
			}
			hrefs[href] = true
			k.Karten[key] = &Karte{UID: uid, Name: name, Href: href, Daten: daten, ErstGesehen: jetzt, ZuletztGesehen: jetzt}
			neu++
			continue
		}
		alt.ZuletztGesehen = jetzt
		alt.GeloeschtAm = nil
		if vcardOhne(alt.Daten, "REV", "PRODID") != vcardOhne(daten, "REV", "PRODID") {
			alt.Frueher = append(alt.Frueher, alt.Daten)
			if len(alt.Frueher) > fruehereVersions {
				alt.Frueher = alt.Frueher[len(alt.Frueher)-fruehereVersions:]
			}
			g := jetzt
			alt.Geaendert = &g
			geaendert++
		}
		alt.Daten, alt.Name = daten, name
	}
	for key, c := range k.Karten {
		if !da[key] && c.GeloeschtAm == nil {
			g := jetzt
			c.GeloeschtAm = &g
			weg++
		}
	}
	k.ZuletztGelesen = jetzt
	k.QuelleWegSeit = nil
	return neu, geaendert, weg
}

// Ausgabe is the card as the relay serves it to msgvault: as the source
// had it, and for a card gone at the source one more CATEGORIES line, so
// the person in msgvault carries „Auf dem Handy gelöscht am …“.
func (c *Karte) Ausgabe() string {
	if c.GeloeschtAm == nil {
		return c.Daten
	}
	zeile := "CATEGORIES:" + geloeschtKategorie + c.GeloeschtAm.Local().Format("02.01.2006") + "\r\n"
	i := strings.LastIndex(strings.ToUpper(c.Daten), "END:VCARD")
	if i < 0 {
		return c.Daten + zeile
	}
	return c.Daten[:i] + zeile + c.Daten[i:]
}

// ETag of the served card: it changes when the card or its mark does.
func (c *Karte) ETag() string {
	h := sha256.Sum256([]byte(c.Ausgabe()))
	return `"` + hex.EncodeToString(h[:12]) + `"`
}

// Sortiert: the cards by served href.
func (k *Kontaktablage) Sortiert() []*Karte {
	out := make([]*Karte, 0, len(k.Karten))
	for _, c := range k.Karten {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Href < out[j].Href })
	return out
}

// vcardNormal: lines end in CRLF, the card ends with one.
func vcardNormal(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	s = strings.TrimSpace(s)
	return s + "\r\n"
}

// vcardFeld is a property's value from the card's own level (unfolded).
func vcardFeld(daten, name string) string {
	for _, z := range entfalten([]byte(daten)) {
		if eigenschaft(z) == name {
			_, w := wert(z)
			return strings.TrimSpace(icsText(w))
		}
	}
	return ""
}

// vcardOhne drops properties that change without the contact changing.
func vcardOhne(daten string, namen ...string) string {
	var b strings.Builder
	for _, z := range entfalten([]byte(daten)) {
		weg := false
		for _, n := range namen {
			if eigenschaft(z) == n {
				weg = true
			}
		}
		if !weg {
			b.WriteString(z + "\n")
		}
	}
	return b.String()
}

// --- reading the book from Radicale

const reportAdressbuch = `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/><C:address-data/></D:prop></C:addressbook-query>`

// AdressbuchHolen reads every card of handy-kontakte (one REPORT).
func (r *Radicale) AdressbuchHolen(ctx context.Context) ([]QuellKarte, error) {
	karten, err := r.BuchHolen(ctx, r.Home()+adressbuchName+"/")
	if errors.Is(err, errBuchVerboten) {
		return nil, errRadicaleAnmeldung
	}
	return karten, err
}

var (
	errBuchVerboten = errors.New("Radicale: kein Leserecht für dieses Adressbuch")
	errBuchFehlt    = errors.New("Radicale: dieses Adressbuch gibt es nicht")
)

// BuchHolen reads every card of the address book at pfad (one REPORT):
// the person's handy-kontakte, or the household's book, which the
// archive's account may only read once Radicale's rights let it
// (errBuchVerboten until then; errBuchFehlt when there is none).
func (r *Radicale) BuchHolen(ctx context.Context, pfad string) ([]QuellKarte, error) {
	name := path.Base(strings.TrimSuffix(pfad, "/"))
	resp, err := r.anfrage(ctx, "REPORT", pfad, "1", []byte(reportAdressbuch), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusMultiStatus:
	case http.StatusForbidden:
		return nil, fmt.Errorf("%w (%s)", errBuchVerboten, name)
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w (%s)", errBuchFehlt, name)
	default:
		return nil, fmt.Errorf("Radicale REPORT %s: %s", name, resp.Status)
	}
	var ms struct {
		Responses []struct {
			Href      string `xml:"DAV: href"`
			Propstats []struct {
				Status string `xml:"DAV: status"`
				Prop   struct {
					Daten string `xml:"urn:ietf:params:xml:ns:carddav address-data"`
				} `xml:"DAV: prop"`
			} `xml:"DAV: propstat"`
		} `xml:"DAV: response"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("Radicale REPORT %s: %w", name, err)
	}
	var out []QuellKarte
	for _, a := range ms.Responses {
		p, err := url.PathUnescape(a.Href)
		if err != nil {
			continue
		}
		for _, ps := range a.Propstats {
			if strings.Contains(ps.Status, " 200") && strings.TrimSpace(ps.Prop.Daten) != "" {
				out = append(out, QuellKarte{Href: path.Base(p), Daten: ps.Prop.Daten})
			}
		}
	}
	return out, nil
}

var errKeineKontakte = errors.New("noch keine Kontakte im Archiv")

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Calendars in the archive (owner, 2026-11-02): while the vault is open
// the archive reads the person's calendars from Radicale (google-*,
// mirrored there by laib's kalender; privat-*, made in Haus) and keeps
// every event it has ever seen, inside the vault: one file per calendar,
// kalender/<name>.json. msgvault 0.22.0 has no CalDAV import, so this
// store is the archive's own. It only grows: an event is keyed by UID and
// RECURRENCE-ID; a changed event keeps its earlier versions; an event
// gone at the source stays, marked „an der Quelle gelöscht am …“.
// Searching events is a later step.

const (
	kalenderFormat   = "archiv-kalender"
	kalenderVersion  = 1
	fruehereVersions = 20 // earlier versions kept per event; older ones go
)

// Termin is one event (one occurrence override has its own RECURRENCE-ID).
type Termin struct {
	UID            string     `json:"uid"`
	RecurrenceID   string     `json:"recurrence_id,omitempty"`
	Titel          string     `json:"titel,omitempty"`
	Beginn         string     `json:"beginn,omitempty"` // DTSTART as written, with its TZID
	Daten          string     `json:"daten"`            // the VEVENT as the source last had it, lines unfolded, CRLF
	ErstGesehen    time.Time  `json:"erst_gesehen"`
	ZuletztGesehen time.Time  `json:"zuletzt_gesehen"`
	Geaendert      *time.Time `json:"geaendert,omitempty"`
	Frueher        []string   `json:"frueher,omitempty"` // earlier versions, oldest first
	GeloeschtAm    *time.Time `json:"an_der_quelle_geloescht_am,omitempty"`
}

// Kalenderablage is one calendar's file in the vault.
type Kalenderablage struct {
	Format         string             `json:"format"`
	Version        int                `json:"version"`
	Name           string             `json:"name"`
	Anzeige        string             `json:"anzeige,omitempty"`
	ZuletztGelesen time.Time          `json:"zuletzt_gelesen"`
	QuelleWegSeit  *time.Time         `json:"quelle_weg_seit,omitempty"` // the calendar itself is gone from Radicale
	Zeitzonen      map[string]string  `json:"zeitzonen,omitempty"`       // TZID -> VTIMEZONE, as last seen
	Termine        map[string]*Termin `json:"termine"`
}

// KalenderZahl is what the page shows per calendar.
type KalenderZahl struct {
	Name          string     `json:"name"`
	Anzeige       string     `json:"anzeige,omitempty"`
	Termine       int        `json:"termine"`
	Geloescht     int        `json:"geloescht"`
	Zuletzt       time.Time  `json:"zuletzt"`
	QuelleWegSeit *time.Time `json:"quelle_weg_seit,omitempty"`
}

func kalenderDatei(innen Innen, name string) string {
	return filepath.Join(innen.Kalender(), name+".json")
}

func kalenderLaden(datei string) (*Kalenderablage, error) {
	b, err := os.ReadFile(datei)
	if errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	var k Kalenderablage
	if err := json.Unmarshal(b, &k); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(datei), err)
	}
	if k.Format != kalenderFormat || k.Version != kalenderVersion {
		return nil, fmt.Errorf("%s: Format %q Version %d kennt dieses Archiv nicht", filepath.Base(datei), k.Format, k.Version)
	}
	if k.Termine == nil {
		k.Termine = map[string]*Termin{}
	}
	return &k, nil
}

func (k *Kalenderablage) speichern(datei string) error {
	b, err := json.Marshal(k)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(datei), 0o700); err != nil {
		return err
	}
	return schreibenAtomar(datei, b, 0o600)
}

// Zahl counts the calendar's events, and those gone at the source.
func (k *Kalenderablage) Zahl() KalenderZahl {
	z := KalenderZahl{Name: k.Name, Anzeige: k.Anzeige, Termine: len(k.Termine), Zuletzt: k.ZuletztGelesen, QuelleWegSeit: k.QuelleWegSeit}
	for _, t := range k.Termine {
		if t.GeloeschtAm != nil {
			z.Geloescht++
		}
	}
	return z
}

// Einmischen merges what the source has now into the store. Nothing is
// ever removed: new events come in, a changed one keeps its earlier
// version, one missing at the source is marked deleted (and unmarked if
// it comes back). It returns how many came in new, changed and went.
func (k *Kalenderablage) Einmischen(ics []byte, jetzt time.Time) (neu, geaendert, weg int) {
	termine, zonen := icsLesen(ics)
	if k.Termine == nil {
		k.Termine = map[string]*Termin{}
	}
	if len(zonen) > 0 {
		if k.Zeitzonen == nil {
			k.Zeitzonen = map[string]string{}
		}
		for id, z := range zonen {
			k.Zeitzonen[id] = z
		}
	}
	da := map[string]bool{}
	for _, t := range termine {
		key := t.schluessel()
		da[key] = true
		alt, ok := k.Termine[key]
		if !ok {
			t.ErstGesehen, t.ZuletztGesehen = jetzt, jetzt
			k.Termine[key] = &t
			neu++
			continue
		}
		alt.ZuletztGesehen = jetzt
		alt.GeloeschtAm = nil
		if ohneStempel(alt.Daten) != ohneStempel(t.Daten) {
			alt.Frueher = append(alt.Frueher, alt.Daten)
			if len(alt.Frueher) > fruehereVersions {
				alt.Frueher = alt.Frueher[len(alt.Frueher)-fruehereVersions:]
			}
			alt.Titel, alt.Beginn = t.Titel, t.Beginn
			g := jetzt
			alt.Geaendert = &g
			geaendert++
		}
		alt.Daten = t.Daten // the newest DTSTAMP too
	}
	for key, t := range k.Termine {
		if !da[key] && t.GeloeschtAm == nil {
			g := jetzt
			t.GeloeschtAm = &g
			weg++
		}
	}
	k.ZuletztGelesen = jetzt
	k.QuelleWegSeit = nil
	return neu, geaendert, weg
}

func (t Termin) schluessel() string {
	uid := t.UID
	if uid == "" { // an event without UID: its content is its identity
		h := sha256.Sum256([]byte(ohneStempel(t.Daten)))
		uid = "ohne-uid:" + hex.EncodeToString(h[:16])
	}
	return uid + "|" + t.RecurrenceID
}

// ohneStempel drops DTSTAMP: a feed (Google's) sets it to the time of the
// export, so it changes on every read without the event changing.
func ohneStempel(daten string) string {
	var b strings.Builder
	for _, z := range strings.Split(daten, "\r\n") {
		if eigenschaft(z) == "DTSTAMP" {
			continue
		}
		b.WriteString(z)
		b.WriteString("\r\n")
	}
	return b.String()
}

// entfalten undoes RFC 5545 line folding (CRLF or LF, then a space or tab).
func entfalten(ics []byte) []string {
	s := strings.ReplaceAll(string(ics), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n ", "")
	s = strings.ReplaceAll(s, "\n\t", "")
	var out []string
	for _, z := range strings.Split(s, "\n") {
		z = strings.TrimRight(z, "\r")
		if z != "" {
			out = append(out, z)
		}
	}
	return out
}

// eigenschaft is a content line's property name, upper case.
func eigenschaft(z string) string {
	i := strings.IndexAny(z, ";:")
	if i < 0 {
		return strings.ToUpper(z)
	}
	return strings.ToUpper(z[:i])
}

// wert splits a content line into its parameters and value at the first
// colon outside a quoted parameter value.
func wert(z string) (param, w string) {
	quote := false
	for i, r := range z {
		switch {
		case r == '"':
			quote = !quote
		case r == ':' && !quote:
			name := strings.IndexByte(z[:i], ';')
			if name < 0 {
				return "", z[i+1:]
			}
			return z[name+1 : i], z[i+1:]
		}
	}
	return "", ""
}

// icsLesen takes every VEVENT (with what is nested in it, VALARM) and
// every VTIMEZONE out of an iCalendar stream (one or several VCALENDARs).
func icsLesen(ics []byte) ([]Termin, map[string]string) {
	var termine []Termin
	zonen := map[string]string{}
	var block []string
	art, tiefe := "", 0
	for _, z := range entfalten(ics) {
		name := eigenschaft(z)
		_, v := wert(z)
		komp := strings.ToUpper(strings.TrimSpace(v))
		if art == "" {
			if name == "BEGIN" && (komp == "VEVENT" || komp == "VTIMEZONE") {
				art, tiefe, block = komp, 1, []string{z}
			}
			continue
		}
		block = append(block, z)
		switch {
		case name == "BEGIN":
			tiefe++
		case name == "END":
			tiefe--
		}
		if tiefe > 0 {
			continue
		}
		daten := strings.Join(block, "\r\n") + "\r\n"
		if art == "VTIMEZONE" {
			for _, b := range block {
				if eigenschaft(b) == "TZID" {
					_, id := wert(b)
					zonen[id] = daten
					break
				}
			}
		} else {
			termine = append(termine, terminAus(block, daten))
		}
		art, block = "", nil
	}
	return termine, zonen
}

// terminAus reads UID, RECURRENCE-ID, SUMMARY and DTSTART from the
// event's own level (not from a VALARM inside it).
func terminAus(block []string, daten string) Termin {
	t := Termin{Daten: daten}
	tiefe := 0
	for _, z := range block {
		name := eigenschaft(z)
		param, v := wert(z)
		switch name {
		case "BEGIN":
			tiefe++
			continue
		case "END":
			tiefe--
			continue
		}
		if tiefe != 1 {
			continue
		}
		switch name {
		case "UID":
			t.UID = strings.TrimSpace(v)
		case "RECURRENCE-ID":
			t.RecurrenceID = zeitMitZone(param, v)
		case "SUMMARY":
			t.Titel = icsText(v)
		case "DTSTART":
			t.Beginn = zeitMitZone(param, v)
		}
	}
	return t
}

// zeitMitZone keeps a time's TZID with it: 20261102T090000 in Berlin is
// not the same instant as in UTC.
func zeitMitZone(param, v string) string {
	v = strings.TrimSpace(v)
	for _, p := range strings.Split(param, ";") {
		if k, w, ok := strings.Cut(p, "="); ok && strings.EqualFold(k, "TZID") {
			return strings.Trim(w, `"`) + ":" + v
		}
	}
	return v
}

// icsText undoes TEXT escaping (\n, \, \; \\).
func icsText(v string) string {
	r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return r.Replace(v)
}

// kalenderZahlen reads every calendar file in the vault: the page's counts.
func kalenderZahlen(innen Innen) ([]KalenderZahl, error) {
	eintraege, err := os.ReadDir(innen.Kalender())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []KalenderZahl
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
		out = append(out, k.Zahl())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(fehler) > 0 {
		return out, errors.New(strings.Join(fehler, "; "))
	}
	return out, nil
}

// kalenderName is how the page names a calendar: its display name, else
// the collection's name without the prefix.
func kalenderName(z KalenderZahl) string {
	art := "Kalender"
	switch {
	case strings.HasPrefix(z.Name, "google-"):
		art = "Google-Kalender"
	case strings.HasPrefix(z.Name, "privat-"):
		art = "Eigener Kalender"
	}
	name := z.Anzeige
	if name == "" {
		name = strings.TrimPrefix(strings.TrimPrefix(z.Name, "google-"), "privat-")
	}
	return art + " „" + name + "“"
}

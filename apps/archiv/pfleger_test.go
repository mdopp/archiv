package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func befundIDs(bs []Befund) (out []string) {
	for _, b := range bs {
		out = append(out, b.ID)
	}
	return out
}

func befundMit(t *testing.T, bs []Befund, id string) Befund {
	t.Helper()
	for _, b := range bs {
		if b.ID == id {
			return b
		}
	}
	t.Fatalf("no finding %s in %v", id, befundIDs(bs))
	return Befund{}
}

// The phone book's checks: a card without a name or with only a number
// as its name, a local number without its area code (not a service
// number, not a country code without its +), a number twice on a card in
// any spelling (all such cards one finding: Homes cleans them at once). A
// card deleted on the phone and a clean card give nothing.
func TestKontaktBefunde(t *testing.T) {
	k := neueKontaktablage()
	jetzt := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	k.Einmischen([]QuellKarte{
		{Href: "leer.vcf", Daten: karteMit("leer", "", "TEL:0171 1111111\r\nEMAIL:x@y.test\r\n")},
		{Href: "zahl.vcf", Daten: karteMit("zahl", "+49 171 2222222", "TEL:+49 171 2222222\r\n")},
		{Href: "ort.vcf", Daten: karteMit("ort", "Oma Gerda", "TEL;TYPE=HOME:681234\r\nTEL:110\r\nTEL:11880\r\nTEL:4917133333333\r\n")},
		{Href: "doppelt.vcf", Daten: karteMit("doppelt", "Ben Berg", "TEL:0171 4444444\r\nTEL:+49 (171) 4444444\r\n")},
		{Href: "auch.vcf", Daten: karteMit("auch", "Carla Wolf", "TEL:tel:+4917155555\r\nTEL:tel:+4917155555\r\n")},
		{Href: "gut.vcf", Daten: karteMit("gut", "Dora Klar", "TEL:0681 123456\r\nTEL:+49 171 6666666\r\n")},
		{Href: "weg.vcf", Daten: karteMit("weg", "", "TEL:123456\r\n")},
	}, jetzt)
	k.Einmischen([]QuellKarte{
		{Href: "leer.vcf", Daten: karteMit("leer", "", "TEL:0171 1111111\r\nEMAIL:x@y.test\r\n")},
		{Href: "zahl.vcf", Daten: karteMit("zahl", "+49 171 2222222", "TEL:+49 171 2222222\r\n")},
		{Href: "ort.vcf", Daten: karteMit("ort", "Oma Gerda", "TEL;TYPE=HOME:681234\r\nTEL:110\r\nTEL:11880\r\nTEL:4917133333333\r\n")},
		{Href: "doppelt.vcf", Daten: karteMit("doppelt", "Ben Berg", "TEL:0171 4444444\r\nTEL:+49 (171) 4444444\r\n")},
		{Href: "auch.vcf", Daten: karteMit("auch", "Carla Wolf", "TEL:tel:+4917155555\r\nTEL:tel:+4917155555\r\n")},
		{Href: "gut.vcf", Daten: karteMit("gut", "Dora Klar", "TEL:0681 123456\r\nTEL:+49 171 6666666\r\n")},
	}, jetzt.Add(time.Hour)) // weg.vcf is gone on the phone
	bs := kontaktBefunde(k)
	want := []string{"ohne-name:leer.vcf", "vorwahl:ort.vcf", "ohne-name:zahl.vcf", "nummer-doppelt"}
	if got := befundIDs(bs); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("findings: %v, want %v", got, want)
	}
	if b := befundMit(t, bs, "ohne-name:leer.vcf"); !slices.Equal(b.Zeilen, []string{"0171 1111111", "x@y.test"}) || b.Titel != "Ein Kontakt ohne Namen" {
		t.Fatalf("without a name: %+v", b)
	}
	if b := befundMit(t, bs, "vorwahl:ort.vcf"); !slices.Equal(b.Zeilen, []string{"681234"}) || !strings.HasPrefix(b.Titel, "Oma Gerda") {
		t.Fatalf("without an area code: %+v", b)
	}
	if b := befundMit(t, bs, "nummer-doppelt"); !slices.Equal(b.Teile, []string{"auch.vcf", "doppelt.vcf"}) || !slices.Equal(b.Zeilen, []string{"Carla Wolf", "Ben Berg"}) {
		t.Fatalf("a number twice: %+v", b)
	}
}

func termin(uid, titel, beginn string) *Termin {
	return &Termin{UID: uid, Titel: titel, Beginn: beginn}
}

// The calendar's check: one event imported twice (the same title, case
// and spaces aside, at the same start, two UIDs) is one finding, a group
// of three too. Another start, another calendar, a deleted copy, an
// occurrence's override and a calendar gone from Radicale give nothing.
func TestKalenderBefunde(t *testing.T) {
	geloescht := time.Now()
	k := &Kalenderablage{Name: "privat-familie", Anzeige: "Familie", Termine: map[string]*Termin{}}
	for _, t := range []*Termin{
		termin("a", "Zahnarzt", "DTSTART;TZID=Europe/Berlin:20261112T100000"),
		termin("b", "zahnarzt ", "DTSTART;TZID=Europe/Berlin:20261112T100000"),
		termin("c", "Zahnarzt", "DTSTART;TZID=Europe/Berlin:20261113T100000"),
		termin("d", "Sommerfest", "DTSTART;VALUE=DATE:20260701"),
		termin("e", "Sommerfest", "DTSTART;VALUE=DATE:20260701"),
		termin("f", "Sommerfest", "DTSTART;VALUE=DATE:20260701"),
		{UID: "g", Titel: "Zahnarzt", Beginn: "DTSTART;TZID=Europe/Berlin:20261112T100000", GeloeschtAm: &geloescht},
		{UID: "a", RecurrenceID: "20261119T100000", Titel: "Zahnarzt", Beginn: "DTSTART;TZID=Europe/Berlin:20261112T100000"},
	} {
		k.Termine[t.schluessel()] = t
	}
	anders := &Kalenderablage{Name: "google-arbeit", Termine: map[string]*Termin{}}
	for _, t := range []*Termin{termin("x", "Zahnarzt", "DTSTART;TZID=Europe/Berlin:20261112T100000")} {
		anders.Termine[t.schluessel()] = t
	}
	weg := &Kalenderablage{Name: "privat-alt", QuelleWegSeit: &geloescht, Termine: map[string]*Termin{}}
	for _, t := range []*Termin{termin("p", "Kurs", "DTSTART:20250101T100000Z"), termin("q", "Kurs", "DTSTART:20250101T100000Z")} {
		weg.Termine[t.schluessel()] = t
	}
	bs := kalenderBefunde([]*Kalenderablage{anders, k, weg})
	want := []string{"termin-doppelt:privat-familie:d|e|f", "termin-doppelt:privat-familie:a|b"}
	if got := befundIDs(bs); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("findings: %v, want %v", got, want)
	}
	b := befundMit(t, bs, "termin-doppelt:privat-familie:a|b")
	if b.Titel != "„Zahnarzt“ steht 2-mal im Kalender" || !slices.Equal(b.Zeilen, []string{"12.11.2026, 10:00", "Eigener Kalender „Familie“"}) {
		t.Fatalf("the pair: %+v", b)
	}
	if b := befundMit(t, bs, "termin-doppelt:privat-familie:d|e|f"); b.Zeilen[0] != "01.07.2026" {
		t.Fatalf("an all-day event: %+v", b)
	}
}

// The photos' checks, from the net's round: a photo with people but no
// date (the net skips it) and one with a date but no place, each kind
// one finding with its photos; the thumbnail route serves the dateless
// one too. A birthday the person confirmed that no card and no Immich
// person has is a finding, its step in the phone book for a person with
// a card, in Immich for one without.
func TestFotoUndGeburtstagBefunde(t *testing.T) {
	ut := netzBox(t)
	n := ut.u.Netz()
	bs := fotoBefunde(n)
	if b := befundMit(t, bs, "fotos-ohne-datum"); !slices.Equal(b.Teile, []string{"a10"}) || !b.Bilder {
		t.Fatalf("without a date: %+v", b)
	}
	if b := befundMit(t, bs, "fotos-ohne-ort"); !slices.Equal(b.Teile, []string{"a4"}) {
		t.Fatalf("without a place: %+v", b)
	}
	if !n.HatFoto("a10") || n.HatFoto("a99") {
		t.Fatal("the thumbnail route's photos")
	}
	if bs := gebBefunde(n); len(bs) != 0 {
		t.Fatalf("nothing confirmed yet: %v", befundIDs(bs))
	}
	ben, kim := ut.knoten(t, "Ben Berg"), ut.knoten(t, "Kim Berg")
	n.gebSetzen(nil, map[string]gebAntwort{ben.ID: {Tag: "03-14", Ja: true}, kim.ID: {Tag: "07-01", Ja: true}, "p-gerd": {Tag: "01-01", Ja: false}})
	bs = gebBefunde(n)
	if got := befundIDs(bs); !slices.Equal(slices.Sorted(slices.Values(got)), []string{"geburtstag:" + ben.ID, "geburtstag:" + kim.ID}) {
		t.Fatalf("birthdays: %v", got)
	}
	if b := befundMit(t, bs, "geburtstag:"+ben.ID); b.Schritt != saysGebKarteSchritt || b.Zeilen[0] != "Du hast bestätigt: 14. März" {
		t.Fatalf("Ben has a card: %+v", b)
	}
	if b := befundMit(t, bs, "geburtstag:"+kim.ID); b.Schritt != saysGebImmichSchritt {
		t.Fatalf("Kim only on photos: %+v", b)
	}
}

// knoten is the net's person of that name.
func (ut *umfeldTest) knoten(t *testing.T, name string) Knoten {
	t.Helper()
	ts, _ := ut.u.Netz().Suchen(name, 1)
	if len(ts) == 0 {
		t.Fatalf("nobody named %s", name)
	}
	return ts[0].Knoten
}

// „Passt so“ (owner 2026-11-12): a finding the person left never returns;
// an aggregate comes back only with a new part, and then only with it.
func TestPasstSoKommtNichtWieder(t *testing.T) {
	innen := Innen{t.TempDir()}
	jetzt := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	eins := Befund{ID: "ohne-name:x.vcf", Titel: "Ein Kontakt ohne Namen"}
	fotos := Befund{ID: "fotos-ohne-ort", Teile: []string{"a1", "a2"}, Bilder: true}
	karten := Befund{ID: "nummer-doppelt", Teile: []string{"a.vcf", "b.vcf"}, Zeilen: []string{"Anna", "Ben"}}
	for _, b := range []Befund{eins, fotos, karten} {
		if err := PasstSo(innen, b, jetzt); err != nil {
			t.Fatal(err)
		}
	}
	if fi, err := os.Stat(innen.pflegePasst()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("kept in the vault: %v %v", fi, err)
	}
	fotos.Teile = []string{"a1", "a2", "a3"}
	karten.Teile, karten.Zeilen = []string{"a.vcf", "c.vcf", "b.vcf"}, []string{"Anna", "Carla", "Ben"}
	got := ohnePasst([]Befund{eins, fotos, karten, {ID: "vorwahl:y.vcf"}}, passtLaden(innen))
	if ids := befundIDs(got); !slices.Equal(ids, []string{"fotos-ohne-ort", "nummer-doppelt", "vorwahl:y.vcf"}) {
		t.Fatalf("after Passt so: %v", ids)
	}
	if !slices.Equal(got[0].Teile, []string{"a3"}) || !slices.Equal(got[1].Teile, []string{"c.vcf"}) || !slices.Equal(got[1].Zeilen, []string{"Carla"}) {
		t.Fatalf("only the new parts: %+v", got[:2])
	}
}

// The person's way (archiv#58): the open archive's start says „N Dinge
// zum Aufräumen ›“; the page shows one finding with its step, „Passt so“
// and „Nächstes“; „Passt so“ shows the next in its place and lowers the
// count, Homes' count too. Closed, there is no line and the page leads to
// the door.
func TestAufraeumenSeite(t *testing.T) {
	wt, ut, _, innen := eintragenBox(t)
	wt.w.Aufgaben = &Aufgaben{Datei: filepath.Join(wt.data, "aufgaben.json")}
	k, _ := kontakteLaden(kontakteDatei(innen))
	k.Einmischen([]QuellKarte{{Href: "leer.vcf", Daten: karteMit("leer", "", "TEL:0171 1111111\r\n")}}, *ut.jetzt)
	k.speichern(kontakteDatei(innen))
	alle := Befunde(innen, ut.u.Netz())
	if len(alle) < 3 || alle[0].ID != "ohne-name:leer.vcf" {
		t.Fatalf("findings: %v", befundIDs(alle))
	}
	start := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if !strings.Contains(start, anzahl(len(alle), "Ding", "Dinge")+" zum Aufräumen ›") {
		t.Fatalf("the start has no line: %s", start)
	}
	seite := wt.rufe("GET", pflegePfad, anna, nil).Body.String()
	for _, s := range []string{"Ein Kontakt ohne Namen", "0171 1111111", esc(saysOhneNameSchritt), "Passt so", "Nächstes ›", "1 von " + fmt.Sprint(len(alle))} {
		if !strings.Contains(seite, s) {
			t.Fatalf("the page lacks %q:\n%s", s, seite)
		}
	}
	ziel, seite := wt.folgen(t, wt.rufe("POST", pflegePfad+"/passt", anna, url.Values{"id": {"ohne-name:leer.vcf"}, "n": {"0"}}))
	if !strings.HasPrefix(ziel, pflegePfad) || !strings.Contains(seite, esc(saysPflegePasst)) || strings.Contains(seite, "0171 1111111") || !strings.Contains(seite, "1 von "+fmt.Sprint(len(alle)-1)) {
		t.Fatalf("after Passt so (%s):\n%s", ziel, seite)
	}
	wt.w.aufgabenZaehlen(wt.tt.Stand())
	if s := aufgabenHolen(t, wt); s.Aufraeumen != len(alle)-1 {
		t.Fatalf("Homes' count: %+v, want %d", s, len(alle)-1)
	}
	// a finding gone meanwhile: said, and the next one shown
	if _, seite := wt.folgen(t, wt.rufe("POST", pflegePfad+"/passt", anna, url.Values{"id": {"ohne-name:leer.vcf"}, "n": {"0"}})); !strings.Contains(seite, esc(saysPflegeWeg)) {
		t.Fatalf("a finding gone:\n%s", seite)
	}
	wt.tt.Abschliessen(t.Context(), "t")
	if rec := wt.rufe("GET", pflegePfad, anna, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("closed: %d", rec.Code)
	}
	if rec := wt.rufe("POST", pflegePfad+"/passt", anna, url.Values{"id": {"x"}}); rec.Code != http.StatusLocked {
		t.Fatalf("closed, Passt so: %d", rec.Code)
	}
}

// With nothing to tidy, the start has no line and the page says so.
func TestAufraeumenLeer(t *testing.T) {
	wt := neuesWeb(t)
	wt.oeffnen(t)
	if start := wt.rufe("GET", "/tresor", anna, nil).Body.String(); strings.Contains(start, "zum Aufräumen") {
		t.Fatalf("a line with nothing to tidy")
	}
	if seite := wt.rufe("GET", pflegePfad, anna, nil).Body.String(); !strings.Contains(seite, esc(saysPflegeLeer)) {
		t.Fatalf("empty page:\n%s", seite)
	}
}

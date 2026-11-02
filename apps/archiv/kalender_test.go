package main

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

func neueAblage() *Kalenderablage {
	return &Kalenderablage{Format: kalenderFormat, Version: kalenderVersion, Name: "google-arbeit", Termine: map[string]*Termin{}}
}

func TestIcsReadsEventsFoldedLinesAndTimezones(t *testing.T) {
	roh := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:lang-\r\n uid\r\nSUMMARY:Zahnarzt\\, Kontrolle\r\nDTSTART;TZID=\"Europe/Berlin\":20261103T090000\r\n" +
		"BEGIN:VALARM\r\nUID:alarm\r\nSUMMARY:nicht der Titel\r\nEND:VALARM\r\nEND:VEVENT\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Europe/Berlin\r\nEND:VTIMEZONE\r\nEND:VCALENDAR\r\n"
	termine, zonen := icsLesen([]byte(roh))
	if len(termine) != 1 {
		t.Fatalf("%+v", termine)
	}
	e := termine[0]
	if e.UID != "lang-uid" || e.Titel != "Zahnarzt, Kontrolle" || e.Beginn != "Europe/Berlin:20261103T090000" {
		t.Fatalf("the event's own level, unfolded: %+v", e)
	}
	if !strings.Contains(e.Daten, "BEGIN:VALARM") || !strings.HasSuffix(e.Daten, "END:VEVENT\r\n") {
		t.Fatalf("the whole VEVENT with its alarm: %q", e.Daten)
	}
	if !strings.Contains(zonen["Europe/Berlin"], "TZID:Europe/Berlin") {
		t.Fatalf("zones: %v", zonen)
	}
}

// The store only grows: new events come in, a feed's new DTSTAMP is no
// change, a real change keeps the old version, an event gone at the
// source stays and is marked, and unmarked when it comes back. An
// occurrence override (RECURRENCE-ID) is its own event.
func TestCalendarStoreOnlyGrows(t *testing.T) {
	k := neueAblage()
	neu, geaendert, weg := k.Einmischen([]byte(ics(
		vevent("serie", "", "Training", "20261101T000000Z"),
		vevent("serie", "20261110T090000", "Training fällt aus", "20261101T000000Z"),
		vevent("einmal", "", "Zahnarzt", "20261101T000000Z"))), t0)
	if neu != 3 || geaendert != 0 || weg != 0 || len(k.Termine) != 3 {
		t.Fatalf("first read: %d %d %d %v", neu, geaendert, weg, k.Termine)
	}
	if k.Termine["serie|Europe/Berlin:20261110T090000"].Titel != "Training fällt aus" {
		t.Fatalf("the override is keyed by its RECURRENCE-ID with zone: %v", k.Termine)
	}

	// the same events again, the feed stamped them anew: nothing changed
	neu, geaendert, weg = k.Einmischen([]byte(ics(
		vevent("serie", "", "Training", "20261102T000000Z"),
		vevent("serie", "20261110T090000", "Training fällt aus", "20261102T000000Z"),
		vevent("einmal", "", "Zahnarzt", "20261102T000000Z"))), t0.Add(time.Hour))
	if neu+geaendert+weg != 0 {
		t.Fatalf("a new DTSTAMP counted as a change: %d %d %d", neu, geaendert, weg)
	}

	// the dentist moved to another title, the override is gone at the source
	neu, geaendert, weg = k.Einmischen([]byte(ics(
		vevent("serie", "", "Training", "20261103T000000Z"),
		vevent("einmal", "", "Zahnarzt (verschoben)", "20261103T000000Z"))), t0.Add(2*time.Hour))
	if neu != 0 || geaendert != 1 || weg != 1 || len(k.Termine) != 3 {
		t.Fatalf("change and delete: %d %d %d, %d stored", neu, geaendert, weg, len(k.Termine))
	}
	z := k.Termine["einmal|"]
	if z.Titel != "Zahnarzt (verschoben)" || len(z.Frueher) != 1 || !strings.Contains(z.Frueher[0], "SUMMARY:Zahnarzt\r\n") || z.Geaendert == nil {
		t.Fatalf("the earlier version is kept: %+v", z)
	}
	ausfall := k.Termine["serie|Europe/Berlin:20261110T090000"]
	if ausfall.GeloeschtAm == nil || !ausfall.GeloeschtAm.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("deleted at the source stays, marked: %+v", ausfall)
	}
	if zahl := k.Zahl(); zahl.Termine != 3 || zahl.Geloescht != 1 {
		t.Fatalf("count: %+v", zahl)
	}
	// a second read without it keeps the first date of deletion
	k.Einmischen([]byte(ics(vevent("serie", "", "Training", "x"), vevent("einmal", "", "Zahnarzt (verschoben)", "x"))), t0.Add(3*time.Hour))
	if !ausfall.GeloeschtAm.Equal(t0.Add(2 * time.Hour)) {
		t.Fatalf("the deletion date moved: %v", ausfall.GeloeschtAm)
	}
	// it comes back: unmarked
	k.Einmischen([]byte(ics(vevent("serie", "20261110T090000", "Training fällt aus", "x"))), t0.Add(4*time.Hour))
	if ausfall.GeloeschtAm != nil {
		t.Fatal("an event back at the source is still marked deleted")
	}
	if len(k.Termine) != 3 {
		t.Fatalf("an empty or short read removed events: %d", len(k.Termine))
	}
}

func TestCalendarFileRoundTripAndNames(t *testing.T) {
	innen := Innen{Wurzel: t.TempDir()}
	k := neueAblage()
	k.Anzeige = "Arbeit"
	k.Einmischen([]byte(ics(vevent("a", "", "A", "x"))), t0)
	if err := k.speichern(kalenderDatei(innen, k.Name)); err != nil {
		t.Fatal(err)
	}
	zahlen, err := kalenderZahlen(innen)
	if err != nil || len(zahlen) != 1 || zahlen[0].Termine != 1 {
		t.Fatalf("%+v %v", zahlen, err)
	}
	if n := kalenderName(zahlen[0]); n != "Google-Kalender „Arbeit“" {
		t.Fatal(n)
	}
	if n := kalenderName(KalenderZahl{Name: "privat-sport"}); n != "Eigener Kalender „sport“" {
		t.Fatal(n)
	}
}

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The phone sent its contacts under new UIDs (2026-11-10 on the box: 6
// contacts reported deleted on the phone and 26 new, although all were
// still there). A card gone in a sync and one first seen in it with the
// same name and a number or an e-mail in common are one contact: it keeps
// its history, its first sight and the href msgvault knows it by, and it
// is neither deleted nor new. Only a card nothing matches is deleted — a
// namesake with another number is someone else.
func TestKontakteUnterNeuerUID(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	t1 := t0.Add(6 * time.Hour)
	karte := func(uid, name string, zeilen ...string) string {
		return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + name + "\r\n" + strings.Join(zeilen, "") + "END:VCARD\r\n"
	}
	namen := []string{"Anna Berg", "Ben Berg", "Carla Wolf", "Dora Öztürk", "Emil Kurz", "Fritz Lang"}
	var vorher, nachher []QuellKarte
	for i, n := range namen {
		vorher = append(vorher, QuellKarte{Href: fmt.Sprintf("alt-%d.vcf", i), Daten: karte(fmt.Sprintf("alt-%d", i), n, fmt.Sprintf("TEL;TYPE=cell:+49 151 100000%d\r\n", i))})
		zeile := fmt.Sprintf("TEL:0151 100000%d\r\n", i) // the phone writes the number its own way now
		if i == 5 {
			zeile = "item1.EMAIL:fritz@example.org\r\n" // Fritz shares only his e-mail with the old card
			vorher[i].Daten = karte("alt-5", n, "TEL:+49 160 5555555\r\nEMAIL:Fritz@Example.org\r\n")
		}
		nachher = append(nachher, QuellKarte{Href: fmt.Sprintf("neu-%d.vcf", i), Daten: karte(fmt.Sprintf("neu-%d", i), n, zeile)})
	}
	vorher = append(vorher,
		QuellKarte{Href: "gerd.vcf", Daten: karte("gerd", "Gerd Weg", "TEL:+4917012345678\r\n")},   // really deleted on the phone
		QuellKarte{Href: "max.vcf", Daten: karte("max-1", "Max Muster", "TEL:+4917099999999\r\n")}, // gone; a namesake with another number comes
		QuellKarte{Href: "ida.vcf", Daten: karte("ida", "Ida Bleibt", "TEL:+4917011111111\r\n")})   // stays as it was
	nachher = append(nachher,
		QuellKarte{Href: "max2.vcf", Daten: karte("max-2", "Max Muster", "TEL:+4917088888888\r\n")},
		QuellKarte{Href: "ida.vcf", Daten: karte("ida", "Ida Bleibt", "TEL:+4917011111111\r\n")})
	for i := range 19 {
		nachher = append(nachher, QuellKarte{Href: fmt.Sprintf("n%d.vcf", i), Daten: karte(fmt.Sprintf("n%d", i), fmt.Sprintf("Neu %d", i), fmt.Sprintf("TEL:+49172000%04d\r\n", i))})
	}

	k := neueKontaktablage()
	if neu, _, weg := k.Einmischen(vorher, t0); neu != 9 || weg != 0 {
		t.Fatalf("first sync: %d new, %d gone", neu, weg)
	}
	neu, _, weg := k.Einmischen(nachher, t1)
	if neu != 20 || weg != 2 {
		t.Fatalf("second sync: %d new (want the 19 and Max's namesake), %d gone (want Gerd and the first Max)", neu, weg)
	}
	if len(k.Karten) != 9+20 {
		t.Fatalf("%d cards", len(k.Karten))
	}
	for i, n := range namen {
		c := k.Karten["uid:"+fmt.Sprintf("neu-%d", i)]
		if c == nil || c.Name != n || c.GeloeschtAm != nil || !c.ErstGesehen.Equal(t0) || c.Href != fmt.Sprintf("alt-%d.vcf", i) ||
			!strings.Contains(c.Daten, "UID:neu-") || len(c.Frueher) != 1 || !strings.Contains(c.Frueher[0], "UID:alt-") {
			t.Fatalf("%s: not carried over: %+v", n, c)
		}
		if k.Karten["uid:"+fmt.Sprintf("alt-%d", i)] != nil {
			t.Fatalf("%s: still under the old UID", n)
		}
	}
	for key, geloescht := range map[string]bool{"uid:gerd": true, "uid:max-1": true, "uid:max-2": false, "uid:ida": false} {
		if c := k.Karten[key]; c == nil || (c.GeloeschtAm != nil) != geloescht {
			t.Errorf("%s: %+v", key, c)
		}
	}
	if neu, geaendert, weg := k.Einmischen(nachher, t1.Add(time.Hour)); neu+geaendert+weg != 0 {
		t.Fatalf("the same again: %d %d %d", neu, geaendert, weg)
	}
}

// What an earlier archive left (the card marked deleted in the very sync
// that first saw its new UID) is put right on the next sync.
func TestKontakteUnterNeuerUIDNachtraeglich(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	t1, t2 := t0.Add(6*time.Hour), t0.Add(12*time.Hour)
	alt := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:alt\r\nFN:Anna Berg\r\nTEL:+49 151 1000001\r\nEND:VCARD\r\n"
	neu := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:neu\r\nFN:Anna Berg\r\nTEL:01511000001\r\nEND:VCARD\r\n"
	k := neueKontaktablage()
	k.Karten["uid:alt"] = &Karte{UID: "alt", Name: "Anna Berg", Href: "alt.vcf", Daten: alt, ErstGesehen: t0, ZuletztGesehen: t0, GeloeschtAm: &t1}
	k.Karten["uid:neu"] = &Karte{UID: "neu", Name: "Anna Berg", Href: "neu.vcf", Daten: neu, ErstGesehen: t1, ZuletztGesehen: t1}
	if n, _, w := k.Einmischen([]QuellKarte{{Href: "neu.vcf", Daten: neu}}, t2); n != 0 || w != 0 {
		t.Fatalf("%d new, %d gone", n, w)
	}
	c := k.Karten["uid:neu"]
	if len(k.Karten) != 1 || c.Href != "alt.vcf" || !c.ErstGesehen.Equal(t0) || c.GeloeschtAm != nil || !c.ZuletztGesehen.Equal(t2) {
		t.Fatalf("not put right: %d cards, %+v", len(k.Karten), c)
	}
}

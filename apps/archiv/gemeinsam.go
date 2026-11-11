package main

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// What two people share, counted, and finding a person by name (owner
// 2026-11-11: „Ich kann nicht nach einer Person suchen. Die
// Gemeinsamkeiten sind unendliche Ketten von gleich klingenden
// Meldungen... Das geht aussagekräftiger und Einfachheit und weniger mit
// mehr.“). The pages say one sentence per pair — „37 gemeinsame Fotos
// (2014–2025, zuletzt im Mai) · 3 Gruppen · 1.204 Nachrichten“ — and show
// a few examples; the rest is folded („alle zeigen“). Everything is read
// from the open vault's net; nothing new is kept, nothing leaves.

const (
	gemeinsamFrisch = 330 * 24 * time.Hour // a newer last photo is said by its month
	sucheHoechstens = 20                   // persons a search lists
)

// Gemeinsam counts what a pair shares: the photos both are on and when
// the first and the last was taken, the group chats and mail threads both
// are in, the messages in those (everybody's, not only the two's), and
// Zuletzt, the newest of the last photo and the last message.
type Gemeinsam struct {
	Fotos           int
	Erstes, Letztes time.Time
	Gruppen, Faeden int
	Nachrichten     int
	Zuletzt         time.Time
}

// gemeinsam counts a and b's photos and gs, the conversations both are
// in; n.mu is held.
func (n *Netz) gemeinsam(a, b string, gs []netzGespraech) (g Gemeinsam) {
	for _, f := range n.st.Fotos {
		if !slices.Contains(f.Wer, a) || !slices.Contains(f.Wer, b) {
			continue
		}
		g.Fotos++
		if g.Erstes.IsZero() || f.T.Before(g.Erstes) {
			g.Erstes = f.T
		}
		if f.T.After(g.Letztes) {
			g.Letztes = f.T
		}
	}
	g.Zuletzt = g.Letztes
	for _, c := range gs {
		switch c.Art {
		case gespraechMail:
			g.Faeden++
		case gespraechChat: // a chat of two adds only its messages
			g.Gruppen++
		}
		g.Nachrichten += c.Nachrichten
		if c.Zuletzt.After(g.Zuletzt) {
			g.Zuletzt = c.Zuletzt
		}
	}
	return g
}

// Satz is the pair in one sentence, each kind once: the photos with
// their years (and the last one's month while it is fresh), the groups,
// the mail threads, the messages; "" when the two share nothing.
func (g Gemeinsam) Satz(jetzt time.Time, loc *time.Location) string {
	var out []string
	if g.Fotos > 0 {
		out = append(out, anzahlT(g.Fotos, "gemeinsames Foto", "gemeinsame Fotos")+" ("+zeitraum(g.Erstes.In(loc), g.Letztes.In(loc), jetzt)+")")
	}
	for _, z := range []struct {
		n          int
		eins, mehr string
	}{{g.Gruppen, "Gruppe", "Gruppen"}, {g.Faeden, "Mail-Verlauf", "Mail-Verläufe"}, {g.Nachrichten, "Nachricht", "Nachrichten"}} {
		if z.n > 0 {
			out = append(out, anzahlT(z.n, z.eins, z.mehr))
		}
	}
	return strings.Join(out, " · ")
}

// zeitraum is „2014–2025, zuletzt im Mai“: the years from the first to
// the last, the last one's month while it is no older than gemeinsamFrisch.
func zeitraum(von, bis, jetzt time.Time) string {
	s := strconv.Itoa(von.Year())
	if bis.Year() != von.Year() {
		s += "–" + strconv.Itoa(bis.Year())
	}
	if alt := jetzt.Sub(bis); alt >= 0 && alt < gemeinsamFrisch {
		s += ", zuletzt im " + monate[bis.Month()-1]
	}
	return s
}

// suchName is a name a node is found by: as written, and for comparing
// in both spellings (klar: „Jürgen“ is „juergen“; flach: it is „jurgen“,
// as the page's script compares while typing).
type suchName struct {
	name, klar, flach string
	spitz             bool        // a phone book's NICKNAME
	woerter           [][2]string // each word, klar and flach
}

// beides is s for comparing, klar and flach: who types „Jurgen“ or
// „Muller“ means Jürgen Müller, and so does who types „Juergen“.
func beides(s string) [2]string { return [2]string{klar(s), klar(flach(s))} }

// suchBauen collects the names every node is found by: its own and each
// of its cards' (a phone book's „Papa“ or „Ben Arbeit“ for the Immich
// person Ben Berg) and their nicknames („Hasi“, owner 2026-11-11); the
// person's other selves count as her. n.mu is held
// or n is new.
func (n *Netz) suchBauen() {
	n.such = map[string][]suchName{}
	dazu := func(id, name string, spitz bool) {
		if n.selbst[id] {
			id = n.ich
		}
		k := beides(name)
		if k[0] == "" || slices.ContainsFunc(n.such[id], func(s suchName) bool { return s.klar == k[0] }) {
			return
		}
		s := suchName{name: name, klar: k[0], flach: k[1], spitz: spitz}
		for _, w := range strings.Fields(name) {
			if w := beides(w); w[0] != "" {
				s.woerter = append(s.woerter, w)
			}
		}
		n.such[id] = append(n.such[id], s)
	}
	for id, k := range n.knoten {
		dazu(id, k.Name, false)
	}
	for _, k := range n.st.Kontakte {
		if id := n.von[k.Key]; id != "" {
			dazu(id, k.Name, false)
		}
	}
	for _, k := range n.st.Kontakte {
		if id := n.von[k.Key]; id != "" {
			for _, s := range k.Spitz {
				dazu(id, s, true)
			}
		}
	}
}

// Treffer is a person a search found: Als the name it was found by when
// that is not the one shown (a phone book's, Spitz: a nickname), Genau
// when a name is exactly what was typed.
type Treffer struct {
	Knoten
	Als   string
	Spitz bool
	Genau bool
}

// Suchen finds the shown persons by name: every word typed is in one of
// a person's names (umlauts, spelled out, plain or as they are, and case
// alike; part of a word is enough; „du“ and „ich“ the person). First whose name is
// exactly that, then whose name has a word starting so, then the rest;
// among equals the closer to the person, then by name. At most
// hoechstens; mehr says there were more.
func (n *Netz) Suchen(q string, hoechstens int) (out []Treffer, mehr bool) {
	if n == nil {
		return nil, false
	}
	var ws [][2]string
	for _, w := range strings.Fields(q) {
		if w := beides(w); w[0] != "" {
			ws = append(ws, w)
		}
	}
	ganz := beides(q)
	if ganz[0] == "" {
		return nil, false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if (ganz[0] == "du" || ganz[0] == "ich") && n.ich != "" && n.fragbar(n.ich) {
		return []Treffer{{Knoten: n.knoten[n.ich], Genau: true}}, false
	}
	nah := map[string]float64{}
	for _, e := range n.kanten[n.ich] {
		nah[andererVon(e, n.ich)] = e.Rang
	}
	type fund struct {
		Treffer
		rang int
	}
	var fs []fund
	for id, namen := range n.such {
		if !n.fragbar(id) {
			continue
		}
		f := fund{rang: 3}
		for _, s := range namen {
			if r := s.rang(ws, ganz); r < f.rang {
				f.rang, f.Als, f.Spitz = r, s.name, s.spitz
			}
		}
		if f.rang == 3 {
			continue
		}
		f.Knoten, f.Genau = n.knoten[id], f.rang == 0
		if f.Als == f.Name {
			f.Als, f.Spitz = "", false
		}
		fs = append(fs, f)
	}
	slices.SortFunc(fs, func(a, b fund) int {
		return cmp.Or(cmp.Compare(a.rang, b.rang), cmp.Compare(nah[b.ID], nah[a.ID]), strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	for _, f := range fs[:min(hoechstens, len(fs))] {
		out = append(out, f.Treffer)
	}
	return out, len(fs) > hoechstens
}

// rang is how well the name answers the words ws (ganz: them as one): 0
// it is exactly that, 1 every word is in it and one of its words starts
// with the first, 2 every word is in it, 3 not found.
func (s suchName) rang(ws [][2]string, ganz [2]string) int {
	if s.klar == ganz[0] || s.flach == ganz[1] {
		return 0
	}
	for _, w := range ws {
		if !strings.Contains(s.klar, w[0]) && !strings.Contains(s.flach, w[1]) {
			return 3
		}
	}
	if slices.ContainsFunc(s.woerter, func(w [2]string) bool { return strings.HasPrefix(w[0], ws[0][0]) || strings.HasPrefix(w[1], ws[0][1]) }) {
		return 1
	}
	return 2
}

// Platz is where id stands among the person's ties, the closest first
// (1 …), and how many she has; 0 when the two are not tied.
func (n *Netz) Platz(id string) (platz, von int) {
	vs := n.Nahe(n.Ich(), 1<<30)
	return 1 + slices.IndexFunc(vs, func(v Verbindung) bool { return v.Mit.ID == id }), len(vs)
}

// KanteMit is the tie of a and b, empty when there is none.
func (n *Netz) KanteMit(a, b string) Kante {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.kanteZwischen(a, b)
}

// Spitznamen are the nicknames id's cards give („Hasi“), each once and
// none that is its name; the person's other selves' count as hers.
func (n *Netz) Spitznamen(id string) (out []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, s := range n.such[id] {
		if s.spitz && s.klar != klar(n.knoten[id].Name) {
			out = append(out, s.name)
		}
	}
	return out
}

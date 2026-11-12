package main

import (
	"cmp"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// How close, and what the net is for (owner 2026-11-11: „weniger mit
// mehr“; what the net says should be true, explained and useful).
//
// What makes a tie (Kante.belegt): a phone book or the person says so,
// the two write each other alone, or at least two occasions that weigh
// belegMin together, each occasion 1/(n-1) of its weight among n people.
// Two photo days among five are a tie; two weddings, two class chats of
// thirty or two mails with eight in copy are not: those stay what the
// pair's page counts, never a line on „Steht nahe“.
//
// How close (stufe), in points anybody can check, each channel at most
// two: photo days (2 days one, nahTage two), the chat of two (direktMin
// messages one, direktViel two), shared group chats and mail threads
// (one), what a phone book or the person says (a partner, parents,
// children, siblings, grandparents three; another relation or a card's
// name one) and one address (two). Five points are „sehr nah“, three
// „nah“, less „bekannt“: by photos or by messages alone nobody is close,
// the closest family is, and very close takes it and more (a partner at
// her address; a mother often on her photos; a friend she writes much,
// meets often and shares a group with). The order within a step is the
// tie's Rang, where old counts less (netzHalbwert). At the owner's scale
// (384 people, 6,000 ties before) that is about a dozen „sehr nah“.
//
// What it is for: „Lange nichts gehört“ (Still): who is close by all the
// archive holds and was last seen or heard stillNach ago.

const (
	belegMin       = 0.5 // what two occasions must weigh together
	direktGroesse  = 2   // a chat of the person and one other
	direktMin      = 10  // messages of it from which it is a tie
	direktViel     = 300 // messages of it that say close by themselves
	direktDeckel   = 3.0 // what it adds to a tie at most, as a phone book's relation
	kontaktGroesse = 6   // a conversation of at most that many is contact with each of them
	nahTage        = 12  // photo days that count two points

	stufeBekannt = 0
	stufeNah     = 1
	stufeSehrNah = 2

	stillNach      = 2 * 365 * 24 * time.Hour // without a photo day or a message for that long
	stillHoechsten = 3                        // rows of „Lange nichts gehört“
)

var stufenWorte = [...]string{"bekannt", "nah", "sehr nah"}

// engeArten are the kinds that count three points by themselves.
var engeArten = []string{"partner", "eltern", "kind", "geschwister", "grosseltern", "enkel"}

// direktGewicht is what a chat of two with n messages adds before the
// decay: 10 messages one, 100 two, 1000 and more three.
func direktGewicht(n int) float64 {
	return direktDeckel * min(1, math.Log10(1+float64(n))/3)
}

// punkte are the tie's points (see above).
func (k *Kante) punkte() (p int) {
	switch {
	case k.Tage >= nahTage:
		p += 2
	case k.Tage >= netzMinTage:
		p++
	}
	switch {
	case k.Direkt >= direktViel:
		p += 2
	case k.Direkt >= direktMin:
		p++
	}
	if k.Chats+k.Faeden >= netzMinTage {
		p++
	}
	buch, adresse := 0, 0
	for _, f := range k.Fest {
		switch {
		case f.Art == bandBeziehung && slices.Contains(engeArten, cmp.Or(buchArt(f.Wie), f.Wie)):
			buch = 3
		case f.Art == bandAdresse:
			adresse = 2
		case f.Art == bandBeziehung, f.Art == bandName:
			buch = max(buch, 1)
		}
	}
	return p + buch + adresse
}

// stufe is how close the tie is: sehr nah, nah or bekannt.
func (k *Kante) stufe() int {
	switch p := k.punkte(); {
	case p >= 5:
		return stufeSehrNah
	case p >= 3:
		return stufeNah
	}
	return stufeBekannt
}

// Stufe is the tie's step as the pages say it.
func (k Kante) Stufe() string { return stufenWorte[k.stufe()] }

// Still is one the person was close to and has not seen or heard for
// long: who, the tie, and when last.
type Still struct {
	Mit     Knoten
	Kante   Kante
	Zuletzt time.Time
}

// Stille are „Lange nichts gehört“: the person's ties that are nah or
// sehr nah, with a last photo day or message stillNach ago or more, the
// closest first, at most stillHoechsten; nobody of her address (she sees
// them daily), nobody she put aside (ruhen) or marked deceased. None while the archive has
// read no chat of two: without them it cannot tell silence.
func (n *Netz) Stille() (out []Still) {
	if n == nil {
		return nil
	}
	ich := n.Ich()
	n.mu.Lock()
	liest := slices.ContainsFunc(n.st.Gespraeche, func(g netzGespraech) bool { return g.Art == gespraechDirekt })
	ruhen := n.ruhen
	n.mu.Unlock()
	if ich == "" || !liest {
		return nil
	}
	jetzt := n.now()
	for _, v := range n.Nahe(ich, 1<<30) {
		k := v.Kante
		if k.stufe() == stufeBekannt || k.Kontakt.IsZero() || jetzt.Sub(k.Kontakt) < stillNach || !ruhen[v.Mit.ID].IsZero() || v.Mit.Tot ||
			slices.ContainsFunc(k.Fest, func(f festBand) bool { return f.Art == bandAdresse }) {
			continue
		}
		if out = append(out, Still{Mit: v.Mit, Kante: k, Zuletzt: k.Kontakt}); len(out) == stillHoechsten {
			break
		}
	}
	return out
}

// stillGrund is why a row stands under „Lange nichts gehört“: when the
// archive last saw the two together, and what there was before.
func stillGrund(s Still, loc *time.Location) string {
	z := s.Zuletzt.In(cmp.Or(loc, time.Local))
	teile := []string{"zuletzt im " + monate[z.Month()-1] + " " + z.Format("2006")}
	return strings.Join(append(teile, gespraechUndFotos(s.Kante)...), " · ")
}

// gespraechUndFotos are a tie's counts in a row's words, two at most.
func gespraechUndFotos(k Kante) (out []string) {
	if k.Tage > 0 {
		out = append(out, anzahlT(k.Tage, "Fototag", "Fototage"))
	}
	out = append(out, gespraechGruende(k)...)
	return out[:min(2, len(out))]
}

func (i Innen) umfeldRuhen() string { return filepath.Join(i.Umfeld(), "ruhen.json") }

func ruhenLaden(innen Innen) map[string]time.Time { return markeLaden(innen.umfeldRuhen()) }

// Ruhen keeps in the open vault that „Lange nichts gehört“ no longer
// names id (an), or names it again.
func (u *Umfeld) Ruhen(innen Innen, id string, an bool) error {
	return u.marke(innen.umfeldRuhen(), id, an, func(n *Netz) *map[string]time.Time { return &n.ruhen })
}

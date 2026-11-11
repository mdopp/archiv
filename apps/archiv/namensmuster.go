package main

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
)

// Name patterns (archiv#42, owner 2026-11-11): a phone book's name like
// „Mama von Lena“, „Papa von …“, „Eltern von …“ says the contact is a
// parent of the one named after it, who is often a child with no card of
// its own: that one still becomes a person of the net, named from the
// pattern, or is the person of that name if there is one (umlauts alike;
// a single given name matches the one person whose first name it is).
// „Freundin von …“, „Ex-Freund von …“ give that relation; „Peter via
// Lena“ only says Peter is known through Lena: a tie „kennt über“, never
// a relation. The words are beziehungswoerter.yml's (namensmuster).
//
// What it does: a tie between the two with the card's name as its reason
// („„Mama von Lena“ im Telefonbuch“), and for a relation a question with
// that kind as the suggestion, asked with or without a tie, a family kind
// or not. Nothing is set without the person's tap; nothing is written
// back to the card. Deterministic: no model reads a name.

const (
	bandName  = "name"  // a relation a card's name says („Mama von Lena“)
	bandUeber = "ueber" // known through („Peter via Lena“)
)

// namensMuster is what a card's name says: the contact is Art of each of
// Ziele; Ueber, known through them (no kind).
type namensMuster struct {
	Art   string
	Ziele []string
	Ueber bool
}

// musterTrenner are the marks a pattern may stand between: „Sabine (Mama von Lena)“.
var musterTrenner = strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ", ",", " ", ";", " ", "/", " ", "|", " ")

// buchstabenWorte are the letter runs of s: „Ex-Freund“ is Ex, Freund.
func buchstabenWorte(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
}

func (w *woerterbuch) ist(menge map[string]bool, wort string) bool {
	return slices.ContainsFunc(vergleiche(wort), func(v string) bool { return menge[v] })
}

// musterArtenVon are the kinds a word before „von“ gives: namensmuster's
// own words, else beziehung's; a word that is also a name („Anne“) gives
// none when nothing stands before it.
func (w *woerterbuch) musterArtenVon(wort string, zuerst bool) []string {
	for _, v := range vergleiche(wort) {
		if a := w.musterArten[v]; len(a) > 0 {
			return a
		}
	}
	if zuerst && w.ist(w.nichtName, wort) {
		return nil
	}
	return w.wortArten(wort)
}

// imMuster is the pattern of a card's name, if it has one: a relation
// word right before a „von“ word, or an „über“ word, then the names.
func (w *woerterbuch) imMuster(name string) (namensMuster, bool) {
	felder := strings.Fields(musterTrenner.Replace(name))
	for i := 1; i < len(felder)-1; i++ {
		ziele := w.zielNamen(felder[i+1:])
		if len(ziele) == 0 {
			continue
		}
		if w.ist(w.ueber, felder[i]) {
			return namensMuster{Ziele: ziele, Ueber: true}, true
		}
		if !w.ist(w.von, felder[i]) {
			continue
		}
		vor := buchstabenWorte(felder[i-1])
		if len(vor) == 0 {
			continue
		}
		wort := vor[len(vor)-1]
		as := w.musterArtenVon(wort, i == 1 && len(vor) == 1)
		if len(as) == 0 {
			continue
		}
		art := as[0]
		ex := len(vor) > 1 && w.ist(w.ex, vor[len(vor)-2]) || len(vor) == 1 && i >= 2 && w.ist(w.ex, felder[i-2])
		if ex && (art == "freund" || art == "partner") {
			art = "expartner"
		}
		return namensMuster{Art: art, Ziele: ziele}, true
	}
	return namensMuster{}, false
}

// zielNamen are the names after „von“, split at „und“ („Lena und Tom“),
// each trimmed of what is no letter or digit at its ends („Lena 🎂“).
func (w *woerterbuch) zielNamen(felder []string) (out []string) {
	var jetzt []string
	fertig := func() {
		s := strings.TrimFunc(strings.Join(jetzt, " "), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if klar(s) != "" {
			out = append(out, s)
		}
		jetzt = nil
	}
	for _, f := range felder {
		if f == "&" || f == "+" || w.ist(w.und, f) {
			fertig()
			continue
		}
		jetzt = append(jetzt, f)
	}
	fertig()
	return out
}

// namensBand is a pattern of a card as the net holds it: Von, the
// contact's node, is Art of Zu ("" known through Zu); Name the card's.
type namensBand struct {
	Von, Zu, Art, Name string
}

// musterBauen reads the cards' names for patterns and finds or makes the
// node of each one named; n.mu is held or n is new. A name two persons
// have is none of them: no band.
func (n *Netz) musterBauen() {
	n.musterBaender = nil
	perName, perVorname := map[string][]string{}, map[string][]string{}
	for _, id := range sortiertKnoten(n.knoten) {
		k := klar(n.knoten[id].Name)
		perName[k] = append(perName[k], id)
		if ws := strings.Fields(n.knoten[id].Name); len(ws) > 1 {
			perVorname[klar(ws[0])] = append(perVorname[klar(ws[0])], id)
		}
	}
	for _, k := range n.st.Kontakte {
		m, ok := woerter.imMuster(k.Name)
		von := n.von[k.Key]
		if !ok || von == "" {
			continue
		}
		for _, ziel := range m.Ziele {
			z, id := klar(ziel), ""
			switch {
			case len(perName[z]) == 1:
				id = perName[z][0]
			case len(perName[z]) > 1:
				continue
			case !strings.Contains(ziel, " ") && len(perVorname[z]) == 1:
				id = perVorname[z][0]
			case !strings.Contains(ziel, " ") && len(perVorname[z]) > 1:
				continue
			default:
				id = "name:" + z
				n.knoten[id] = Knoten{ID: id, Name: ziel}
				perName[z] = []string{id}
			}
			if id != von {
				n.musterBaender = append(n.musterBaender, namensBand{Von: von, Zu: id, Art: m.Art, Name: k.Name})
			}
		}
	}
}

func sortiertKnoten(m map[string]Knoten) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// musterEnden are a band's two nodes, the person's other selves as her; n.mu is held.
func (n *Netz) musterEnden(b namensBand) (string, string) {
	a, z := b.Von, b.Zu
	if n.selbst[a] {
		a = n.ich
	}
	if n.selbst[z] {
		z = n.ich
	}
	return a, z
}

// musterDazu adds the bands' ties to alle, those in nicht left out; n.mu is held or n is new.
func (n *Netz) musterDazu(alle map[[2]string]*Kante, nicht map[string]bool) {
	for _, b := range n.musterBaender {
		a, z := n.musterEnden(b)
		if a == z || nicht[a] || nicht[z] {
			continue
		}
		key := [2]string{min(a, z), max(a, z)}
		k := alle[key]
		if k == nil {
			k = &Kante{A: key[0], B: key[1]}
			alle[key] = k
		}
		art := cmp.Or(map[bool]string{true: bandName}[b.Art != ""], bandUeber)
		if slices.ContainsFunc(k.Fest, func(f festBand) bool { return f.Art == art && f.Wie == b.Name }) {
			continue
		}
		if !slices.ContainsFunc(k.Fest, func(f festBand) bool { return f.Art == art }) {
			k.Staerke, k.Rang = k.Staerke+festGewicht[art], k.Rang+festGewicht[art]
		}
		k.Fest = append(k.Fest, festBand{A: a, B: z, Art: art, Wie: b.Name})
	}
}

// musterFuer is the kind a card's name says x is of y, and why; "" none.
// n.mu is held.
func (n *Netz) musterFuer(x, y string) (string, string) {
	for _, b := range n.musterBaender {
		a, z := n.musterEnden(b)
		switch {
		case b.Art == "":
		case a == x && z == y:
			return b.Art, "„" + b.Name + "“ im Telefonbuch"
		case a == y && z == x:
			return cmp.Or(gegen[b.Art], b.Art), "„" + b.Name + "“ im Telefonbuch"
		}
	}
	return "", ""
}

// musterVon: a card's name says x is a kind of y (x is the card); n.mu is held.
func (n *Netz) musterVon(x, y string) bool {
	return slices.ContainsFunc(n.musterBaender, func(b namensBand) bool {
		a, z := n.musterEnden(b)
		return b.Art != "" && a == x && z == y
	})
}

// ausMuster: f's suggestion is a card's name's; n.mu is held.
func (n *Netz) ausMuster(f Frage) bool {
	_, weil := n.musterFuer(f.X.ID, f.Y.ID)
	return weil != "" && f.Weil == weil
}

// musterVorschlaege are the pairs a card's name gives a kind, for the
// suggestions page (erkannt.go); n.mu is held.
func (n *Netz) musterVorschlaege() (out [][2]string) {
	for _, b := range n.musterBaender {
		if a, z := n.musterEnden(b); b.Art != "" && a != z {
			out = append(out, [2]string{a, z})
		}
	}
	return out
}

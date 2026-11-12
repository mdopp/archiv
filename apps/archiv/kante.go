package main

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"
)

// An edge, one pair at a time (archiv#27 steps 4 and 7; owner 2026-11-11:
// „Ich möchte von einer Kante schnell auf die Nachrichten und Bilder
// kommen können und diese Kante schnell nachbessern.“): what the net
// knows of two people — their relation and where it comes from, their
// shared photo days, the group chats and mail threads both are in, what
// the box's model read in their messages, and all of it counted
// (gemeinsam.go) —
// and a way to set their relation whether or not it is asked
// (Festlegen). Everything here is read from the open vault's net; the
// page (kante_seite.go) adds the thumbnails and the last lines live.

const (
	kanteFotoTage   = 30 // photo days the page lists at most
	kanteFotosJeTag = 4  // photos of one day it shows
)

// FotoTag is a day both are on photos: its date and the photos' ids.
type FotoTag struct {
	Tag string // 2006-01-02, the house's day
	IDs []string
}

// Zitat is a cue the model read in a message of both: the message's id,
// when, the cue as a sentence and its words.
type Zitat struct {
	ID, Satz, Text string
	Am             time.Time
}

// PaarSicht is a pair as the edge page shows it: X is read „X ist für
// Y …“ (the person is Y when she is one end); Art the kind X is of Y now
// ("" none; keine and fremd too), Quelle where it comes from ("gesagt",
// "gefolgert", "buch", ""); Frage the pair's open question, its suggestion
// to confirm with one tap (nil none).
type PaarSicht struct {
	X, Y       Knoten
	Ich        bool
	Kante      Kante
	Art        string
	Quelle     string
	Weil       string // why a kind followed from an answer (Quelle gefolgert)
	Frage      *Frage
	Fotos      []FotoTag
	FotoTage   int // all shared photo days, beyond the listed ones
	Gespraeche []netzGespraech
	Zitate     []Zitat
	Gemeinsam  Gemeinsam
	Satz       string            // Gemeinsam in one sentence ("" nothing shared)
	Kategorie  map[string]string // a shared group's category as the person reads it, by conversation
	Ausserdem  []string          // what neither a count nor the relation says (Netz.ausserdem)
	Bekannte   []Knoten          // tied to both, context only (gemeinsameBekannte)
}

// buchArt is a phone book's relation word as a kind ("" none it knows).
func buchArt(wie string) string {
	if art := relWie[klar(wie)]; art != "" {
		return art
	}
	for _, a := range arten {
		if klar(kurzWort(a)) == klar(wie) || klar(artWort(a)) == klar(wie) {
			return a
		}
	}
	return ""
}

// Paar is what the net knows of a and b; ok false when either is no
// shown node or both are one.
func (n *Netz) Paar(a, b string) (PaarSicht, bool) {
	if n == nil {
		return PaarSicht{}, false
	}
	n.mu.Lock()
	if n.selbst[a] {
		a = n.ich
	}
	if n.selbst[b] {
		b = n.ich
	}
	if a == b || !n.fragbar(a) || !n.fragbar(b) {
		n.mu.Unlock()
		return PaarSicht{}, false
	}
	if a == n.ich {
		a, b = b, a
	}
	key := paarKey(a, b)
	p := PaarSicht{Kante: n.kanteZwischen(a, b)}
	for _, f := range n.offen() {
		if f.Gruppe == nil && f.key() == key {
			f := f
			p.Frage = &f
			a, b = f.X.ID, f.Y.ID
			break
		}
	}
	p.X, p.Y, p.Ich = n.knoten[a], n.knoten[b], b == n.ich
	p.Art, p.Quelle, p.Weil = n.artZwischen(a, b)
	p.Fotos, p.FotoTage = n.fotoTage(a, b)
	p.Gespraeche = n.gemeinsameGespraeche(a, b)
	p.Zitate = n.zitate(a, b)
	p.Gemeinsam = n.gemeinsam(a, b, p.Gespraeche)
	p.Satz = p.Gemeinsam.Satz(n.now(), cmp.Or(n.Loc, time.Local))
	p.Kategorie = map[string]string{}
	for _, g := range p.Gespraeche {
		if kat, _ := n.kategorieVon(g.ID); kat != "" && g.Art == gespraechChat {
			p.Kategorie[g.ID] = kategorieName(kat, n.gruppen[g.ID].Name)
		}
	}
	p.Ausserdem = n.ausserdem(b, p.Kante, p.Art)
	if n.gleicherName(a, b) {
		p.Ausserdem = append(p.Ausserdem, "gleicher Nachname")
	}
	p.Bekannte = n.gemeinsameBekannte(a, b)
	n.mu.Unlock()
	return p, true
}

// gemeinsameBekannte are the shown people tied to both a and b, the
// strongest pair of ties first (owner 2026-11-11: „Kann das auch durch
// gemeinsame Freunde kommen?“). No: they are context the pages name,
// never evidence. bauen never reads them, so they make no tie and
// strengthen none (TestBekannteSindKeinBeleg). n.mu is held.
func (n *Netz) gemeinsameBekannte(a, b string) []Knoten {
	type bekannter struct {
		k Knoten
		s float64
	}
	var out []bekannter
	for _, e := range n.kanten[a] {
		c := andererVon(e, a)
		if c == b || !n.fragbar(c) {
			continue
		}
		for _, f := range n.kanten[b] {
			if andererVon(f, b) == c {
				out = append(out, bekannter{n.knoten[c], e.Staerke + f.Staerke})
			}
		}
	}
	slices.SortFunc(out, func(x, y bekannter) int { return cmp.Or(cmp.Compare(y.s, x.s), strings.Compare(x.k.Name, y.k.Name)) })
	ks := make([]Knoten, len(out))
	for i, x := range out {
		ks[i] = x.k
	}
	return ks
}

// artZwischen is the kind x is of y now, where it comes from and, for a
// kind that followed from an answer, why; n.mu is held.
func (n *Netz) artZwischen(x, y string) (art, quelle, weil string) {
	if a, da := n.antworten[paarKey(x, y)]; da && a.Art != artSpaeter {
		art = a.Art
		if a.X != x {
			art = cmp.Or(gegen[art], art)
		}
		if a.Aus != "" {
			return art, "gefolgert", a.Weil
		}
		return art, "gesagt", ""
	}
	for _, f := range n.st.Fest {
		va, vb := n.von[f.A], n.von[f.B]
		if f.Art != bandBeziehung || !(va == x && vb == y || va == y && vb == x) {
			continue
		}
		if art = buchArt(f.Wie); art == "" {
			continue
		}
		if vb != x { // A's card names B as Wie: B is that of A
			art = cmp.Or(gegen[art], art)
		}
		return art, "buch", ""
	}
	return "", "", ""
}

// fotoTage are the days both are on a photo, the newest first, at most
// kanteFotoTage with kanteFotosJeTag photos each, and how many days in
// all; n.mu is held.
func (n *Netz) fotoTage(a, b string) ([]FotoTag, int) {
	loc := cmp.Or(n.Loc, time.Local)
	fs := slices.Clone(n.st.Fotos)
	slices.SortStableFunc(fs, func(p, q netzFoto) int { return cmp.Or(q.T.Compare(p.T), strings.Compare(p.ID, q.ID)) })
	var out []FotoTag
	tage, letzter := 0, ""
	for _, f := range fs {
		if !slices.Contains(f.Wer, a) || !slices.Contains(f.Wer, b) {
			continue
		}
		tag := f.T.In(loc).Format("2006-01-02")
		if tag != letzter {
			letzter = tag
			tage++
			if len(out) < kanteFotoTage {
				out = append(out, FotoTag{Tag: tag})
			}
		}
		if t := &out[len(out)-1]; t.Tag == tag && len(t.IDs) < kanteFotosJeTag {
			t.IDs = append(t.IDs, f.ID)
		}
	}
	return out, tage
}

// HatFoto: id is a photo of the net's table, or one of its photos without a date
// (the Datenpfleger's); the thumbnail route serves no other.
func (n *Netz) HatFoto(id string) bool {
	if n == nil || id == "" {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.ContainsFunc(n.st.Fotos, func(f netzFoto) bool { return f.ID == id }) || slices.Contains(n.st.OhneDatum, id)
}

// teilnehmerKnoten are the shown nodes of a conversation's cards, the
// person for any of her own; n.mu is held.
func (n *Netz) teilnehmerKnoten(wer []string) map[string]bool {
	out := map[string]bool{}
	for _, key := range wer {
		id := n.von[key]
		if n.selbst[id] {
			id = n.ich
		}
		if id != "" {
			out[id] = true
		}
	}
	return out
}

// gemeinsameGespraeche are the group chats and mail threads both are in,
// the newest first; n.mu is held.
func (n *Netz) gemeinsameGespraeche(a, b string) (out []netzGespraech) {
	for _, g := range n.st.Gespraeche {
		if wer := n.teilnehmerKnoten(g.Wer); wer[a] && wer[b] {
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(p, q netzGespraech) int { return cmp.Or(q.Zuletzt.Compare(p.Zuletzt), strings.Compare(p.ID, q.ID)) })
	return out
}

// zitate are the cues the model read in messages between the two (one
// sent, the other got it), the one behind the pair's suggestion first,
// then the newest; n.mu is held.
func (n *Netz) zitate(a, b string) (out []Zitat) {
	h, hinweis := n.hinweise[paarKey(a, b)]
	for _, id := range slices.Sorted(maps.Keys(n.gelesen)) {
		g := n.gelesen[id]
		dabei := func(x string) bool { return g.Von == x || slices.Contains(g.An, x) }
		if g.Zitat == "" || !dabei(a) || !dabei(b) || g.Von != a && g.Von != b {
			continue
		}
		out = append(out, Zitat{ID: id, Satz: n.schreibt(g, a, b), Text: g.Zitat, Am: g.Am})
	}
	slices.SortStableFunc(out, func(p, q Zitat) int {
		erst := func(z Zitat) int {
			if hinweis && z.Satz == h.Weil {
				return 0
			}
			return 1
		}
		return cmp.Or(cmp.Compare(erst(p), erst(q)), q.Am.Compare(p.Am))
	})
	return out
}

// vorherStand is what Festlegen replaced, for Rückgängig: the pair's
// answer before (nil none) and what had followed from it.
type vorherStand struct {
	Antwort *antwort           `json:"antwort,omitempty"`
	Folgen  map[string]antwort `json:"folgen,omitempty"`
}

// Festlegen sets the relation of x and y from the edge page, asked or
// not: x is art of y (a kind, Keine or Fremd). It keeps what it replaced
// (Vorher), so Rückgängig brings it back; what followed from the old
// answer goes with it, what follows from the new one by definition comes
// (folgern). An open question about the pair is answered as Beantworten
// would. It answers the pair as set and the answers as they now are.
func (n *Netz) Festlegen(x, y, art string) (Frage, map[string]antwort, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.selbst[x] {
		x = n.ich
	}
	if n.selbst[y] {
		y = n.ich
	}
	if x == y || !n.fragbar(x) || !n.fragbar(y) || !gueltig(art) || art == artSpaeter {
		return Frage{}, nil, errFrageWeg
	}
	key := paarKey(x, y)
	f := Frage{X: n.knoten[x], Y: n.knoten[y], Ich: y == n.ich, Kante: n.kanteZwischen(x, y)}
	for _, o := range n.offen() {
		if o.Gruppe == nil && o.key() == key {
			f.Vorschlag, f.Weil, f.Abgeleitet, f.Konflikt = o.Vorschlag, o.Weil, o.Abgeleitet, o.Konflikt
			if o.X.ID != x {
				f.Vorschlag = cmp.Or(gegen[f.Vorschlag], f.Vorschlag)
			}
		}
	}
	vorher := map[string]bool{}
	for _, a := range n.abgeleitete() {
		vorher[paarKey(a.X, a.Y)] = true
	}
	v := &vorherStand{Folgen: map[string]antwort{}}
	if alt, da := n.antworten[key]; da && alt.Art != artSpaeter {
		v.Antwort = &alt
	}
	for k, a := range n.antworten {
		if a.Aus == key {
			v.Folgen[k] = a
			delete(n.antworten, k)
		}
	}
	neu := antwort{X: x, Art: art, Am: n.now()}
	if v.Antwort != nil || len(v.Folgen) > 0 {
		neu.Vorher = v
	}
	if f.Konflikt {
		neu.Trotz = f.Vorschlag
	}
	n.antworten[key] = neu
	if slices.Contains(arten, art) {
		n.folgern(key, vorher)
	}
	n.bauen()
	return f, maps.Clone(n.antworten), nil
}

// Finden are the shown nodes whose name is name (umlauts, spelled out or
// not, and case alike; „du“ the person), else those whose name holds
// every word of it; the person's other selves never, the person once.
func (n *Netz) Finden(name string) []Knoten {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	woerter := strings.Fields(name)
	q := klar(name)
	if q == "" {
		return nil
	}
	if (q == "du" || q == "ich") && n.ich != "" && n.fragbar(n.ich) {
		return []Knoten{n.knoten[n.ich]}
	}
	var genau, enthalten []Knoten
	gesehen := map[string]bool{}
	for _, id := range slices.Sorted(maps.Keys(n.knoten)) {
		k := n.knoten[id]
		if n.selbst[id] {
			id, k = n.ich, n.knoten[n.ich]
		}
		if gesehen[id] || !n.fragbar(id) {
			continue
		}
		gesehen[id] = true
		kn := klar(k.Name)
		alle := kn != ""
		for _, w := range woerter {
			alle = alle && strings.Contains(kn, klar(w))
		}
		switch {
		case kn == q:
			genau = append(genau, k)
		case alle:
			enthalten = append(enthalten, k)
		}
	}
	if len(genau) > 0 {
		return genau
	}
	slices.SortFunc(enthalten, func(a, b Knoten) int { return strings.Compare(a.Name, b.Name) })
	return enthalten
}

// Namen are the shown nodes' names for the page's suggestions (a
// datalist), each once, by name; the person as „Du“.
func (n *Netz) Namen() (out []string) {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, k := range n.knoten {
		if n.fragbar(id) && k.Name != "" && id != n.ich {
			out = append(out, k.Name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

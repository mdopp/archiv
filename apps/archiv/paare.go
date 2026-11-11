package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Couples from „wir“ messages (archiv#44, the rest of #42). Each round,
// while the vault is open and msgvault answers, it reads (one read-only
// statement, as hinweise.go does) the newest messages that say „mein
// Mann Tom“ or end signed by two. No model: the words of
// beziehungswoerter.yml's paare decide.
//
//   - „mein Mann Tom“, „meine Frau Lena“: a mein word, a partner word and
//     a name; one message is enough.
//   - a message with a wir word that ends with „Anna und Ben“ (or „&“,
//     „+“), one of the two the writer's first name: the other is the
//     writer's partner, from paarMinBelege such messages on.
//
// The name must be one known person: by first name, among several the
// one the writer has a tie or a family name with. The result is a
// relation suggestion under „Erkannt“ (kind partner) the person confirms
// like every other one, or discards („Stimmt nicht“, kept in the vault in
// umfeld/paare-weg.json, never suggested again). What a round found is
// kept in umfeld/paare.json: the pair, how many messages, the few words.

const (
	paarMinBelege = 2 // signed-by-two messages a couple needs
	paarZitat     = 40
)

// paarVorschlag is a suggested couple: X wrote, Y is named; Mein: by „mein
// Mann …“, else signed by two; Belege how many messages; Zitat the words.
type paarVorschlag struct {
	X      string `json:"x"`
	Y      string `json:"y"`
	Mein   bool   `json:"mein,omitempty"`
	Belege int    `json:"b"`
	Zitat  string `json:"z"`
}

func (i Innen) umfeldPaare() string    { return filepath.Join(i.Umfeld(), "paare.json") }
func (i Innen) umfeldPaareWeg() string { return filepath.Join(i.Umfeld(), "paare-weg.json") }

func paareLaden(innen Innen) map[string]paarVorschlag {
	out := map[string]paarVorschlag{}
	if b, err := os.ReadFile(innen.umfeldPaare()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func paareWegLaden(innen Innen) map[string]time.Time {
	out := map[string]time.Time{}
	if b, err := os.ReadFile(innen.umfeldPaareWeg()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// paarMuster is msgvault's filter (on lower(snippet)): a mein word and a
// partner word, or two words joined by an und word, „&“ or „+“ at the end.
func (w *woerterbuch) paarMuster() string {
	var und []string
	for u := range w.und {
		und = append(und, regexp.QuoteMeta(u))
	}
	slices.Sort(und)
	return `(^|\PL)(` + strings.Join(w.paarFormen[0], "|") + `)[\s,]+(` + strings.Join(w.paarFormen[1], "|") + `)(\PL|$)` +
		`|\pL\s*(&|\+|` + strings.Join(und, "|") + `)\s*\pL+[^\pL]*$`
}

// wortStelle is a word of a text, or „&“/„+“, with where it stands.
type wortStelle struct {
	s        string
	von, bis int
}

var paarToken = regexp.MustCompile(`\pL+|[&+]`)

func stellen(text string) (out []wortStelle) {
	for _, m := range paarToken.FindAllStringIndex(text, -1) {
		out = append(out, wortStelle{s: text[m[0]:m[1]], von: m[0], bis: m[1]})
	}
	return out
}

// einName: a word written as a name (a capital first), none of the couples' words.
func (w *woerterbuch) einName(s string) bool {
	r := []rune(s)
	return len(r) > 1 && unicode.IsUpper(r[0]) && !w.ist(w.mein, s) && !w.ist(w.partner, s) && !w.ist(w.wir, s) && !w.ist(w.und, s)
}

// meinPartner is the name „mein Mann Tom“ gives, and the words; "" none.
func (w *woerterbuch) meinPartner(text string) (name, zitat string) {
	ws := stellen(text)
	for i := 0; i+2 < len(ws); i++ {
		a, b, c := ws[i], ws[i+1], ws[i+2]
		if !w.ist(w.mein, a.s) || !w.ist(w.partner, b.s) || !w.einName(c.s) ||
			strings.Trim(text[a.bis:b.von], " ") != "" || strings.Trim(text[b.bis:c.von], " ,") != "" {
			continue
		}
		return c.s, text[a.von:c.bis]
	}
	return "", ""
}

// zuZweit are the two names a text with a wir word ends signed with; "" none.
func (w *woerterbuch) zuZweit(text string) (a, b, zitat string) {
	ws := stellen(text)
	if len(ws) < 4 || !slices.ContainsFunc(ws[:len(ws)-3], func(s wortStelle) bool { return w.ist(w.wir, s.s) }) {
		return "", "", ""
	}
	x, u, y := ws[len(ws)-3], ws[len(ws)-2], ws[len(ws)-1]
	if !w.einName(x.s) || !w.einName(y.s) || u.s != "&" && u.s != "+" && !w.ist(w.und, u.s) ||
		strings.ContainsFunc(text[y.bis:], unicode.IsLetter) || strings.ContainsAny(text[x.bis:y.von], ".!?\n") {
		return "", "", ""
	}
	return x.s, y.s, text[x.von:y.bis]
}

// vorname is the first word of a name, klar.
func vorname(name string) string {
	ws := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && r != '-' })
	if len(ws) == 0 {
		return ""
	}
	return klar(ws[0])
}

// genannt is the one person the writer von means by a first name; "" none
// or several. n.mu is held.
func (n *Netz) genannt(von, name string) string {
	var alle []string
	for _, id := range slices.Sorted(maps.Keys(n.knoten)) {
		if id != von && n.fragbar(id) && vorname(n.knoten[id].Name) == klar(name) {
			alle = append(alle, id)
		}
	}
	if len(alle) == 1 {
		return alle[0]
	}
	var nah []string
	for _, id := range alle {
		if n.gleicherName(von, id) || slices.ContainsFunc(n.kanten[von], func(k *Kante) bool { return k.A == id || k.B == id }) {
			nah = append(nah, id)
		}
	}
	if len(nah) == 1 {
		return nah[0]
	}
	return ""
}

// paareFinden reads the couples of roh. n.mu is free.
func (n *Netz) paareFinden(roh []rohNachricht, ks []buchKarte) map[string]paarVorschlag {
	ich := n.Ich()
	out := map[string]paarVorschlag{}
	for _, r := range roh {
		von := ""
		if r.VonMir {
			von = ich
		} else if len(r.Von) > 0 {
			von = n.knotenVon(r.Von[0], ks)
		}
		if von == "" {
			continue
		}
		name, zitat := woerter.meinPartner(r.Text)
		mein := name != ""
		if !mein {
			a, b, z := woerter.zuZweit(r.Text)
			n.mu.Lock()
			selbst := vorname(n.knoten[von].Name)
			n.mu.Unlock()
			switch {
			case a == "" || selbst == "":
			case klar(a) == selbst:
				name, zitat = b, z
			case klar(b) == selbst:
				name, zitat = a, z
			}
		}
		if name == "" {
			continue
		}
		n.mu.Lock()
		y := n.genannt(von, name)
		n.mu.Unlock()
		if y == "" {
			continue
		}
		key := paarKey(von, y)
		p := out[key]
		if p.Belege == 0 || mein && !p.Mein {
			p.X, p.Y, p.Zitat = von, y, kuerzen(zitat, paarZitat)
		}
		p.Mein = p.Mein || mein
		p.Belege++
		out[key] = p
	}
	maps.DeleteFunc(out, func(_ string, p paarVorschlag) bool { return !p.Mein && p.Belege < paarMinBelege })
	return out
}

func kuerzen(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// paareRunde reads the messages from msgvault, keeps the couples in the
// vault and gives them to the net. It logs counts only.
func (u *Umfeld) paareRunde(ctx context.Context, innen Innen, n *Netz, ks []buchKarte) {
	if u.Msgvault == nil || n == nil {
		return
	}
	roh, err := u.Msgvault.Schlagwortnachrichten(ctx, woerter.paarMuster())
	if err != nil {
		if !errors.Is(err, errNichtBereit) && !errors.Is(err, errGespraecheSpaeter) && ctx.Err() == nil {
			log.Printf("archiv: Umfeld: „wir“-Nachrichten aus msgvault: %v", err)
		}
		return
	}
	ps := n.paareFinden(roh, ks)
	if err := jsonImTresor(innen.umfeldPaare(), ps); err != nil {
		log.Printf("archiv: Umfeld: Paare speichern: %v", err)
		return
	}
	n.paareSetzen(ps, nil)
	u.geaendert()
}

// paareSetzen gives the net the couples and (not nil) the discarded ones.
func (n *Netz) paareSetzen(ps map[string]paarVorschlag, weg map[string]time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if ps != nil {
		n.paare = maps.Clone(ps)
	}
	if weg != nil {
		n.paareWeg = maps.Clone(weg)
	}
}

// paarWeil is why a couple is suggested, verb last; n.mu is held.
func (n *Netz) paarWeil(p paarVorschlag) string {
	z := "„" + p.Zitat + "“"
	wer, du := cmp.Or(n.knoten[p.X].Name, "jemand"), p.X == n.ich
	switch {
	case p.Mein && du:
		return "du " + z + " schreibst"
	case p.Mein:
		return wer + " " + z + " schreibt"
	case du:
		return fmt.Sprintf("du %s mit %s unterschreibst", anzahl(p.Belege, "Nachricht", "Nachrichten"), z)
	}
	return fmt.Sprintf("%s %s mit %s unterschreibt", wer, anzahl(p.Belege, "Nachricht", "Nachrichten"), z)
}

// PaarVerwerfen keeps that x and y are not the couple suggested.
func (u *Umfeld) PaarVerwerfen(innen Innen, x, y string) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	n.mu.Lock()
	key := paarKey(x, y)
	if _, da := n.paare[key]; !da || x == y {
		n.mu.Unlock()
		return errFrageWeg
	}
	n.paareWeg[key] = n.now()
	alle := maps.Clone(n.paareWeg)
	n.mu.Unlock()
	return jsonImTresor(innen.umfeldPaareWeg(), alle)
}

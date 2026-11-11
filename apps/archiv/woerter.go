package main

import (
	_ "embed"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// The relation words (archiv#27, owner 2026-11-11: „Sind Oma, Mama,
// Mutter, Onkel, Schatzi und ähnliches in den Nachrichten (Schlagwörter in
// einer Datei, einfach für dich erreichbar, mehrsprachig) …“). One file,
// beziehungswoerter.yml, built into the archive: the kinship and affection
// words of the family's languages with the kinds they hint at, and the
// words of a group chat's title with the category they suggest. Words
// compare as klar does (case, umlauts and accents aside), and with the
// accents dropped as well („Schwagerin“, „kardesim“).
//
// Three readers: a contact's name („Oma Hilde“: weg 1, fragen.go
// vorschlag), a group's title (gruppen.go), and the messages the box's
// model is asked about (hinweise.go): only a message with such a word is
// ever sent to it.

//go:embed beziehungswoerter.yml
var woerterDatei []byte

// woerterbuch is the file as the archive reads it.
type woerterbuch struct {
	arten     map[string][]string // a word (vergleich) → the kinds it hints at, the suggestion first
	gruppen   map[string]string   // a word of a title (vergleich) → its category
	rang      map[string]int      // a category → its place in the file: the first a title has wins
	nichtName map[string]bool     // words that are also names: never a suggestion from a name
	formen    []string            // every word as text may spell it, lower case, for msgvault's filter
}

// woerter is beziehungswoerter.yml; a broken file stops the archive at its
// start, and go test before that (TestWoerterDatei).
var woerter = func() *woerterbuch {
	w, err := woerterLesen(woerterDatei)
	if err != nil {
		panic("beziehungswoerter.yml: " + err.Error())
	}
	return w
}()

// flachErsetzer drops the accents klar keeps: ä is a (Homes' namen.Plain
// writes it so), ş is s, ł is l.
var flachErsetzer = strings.NewReplacer("ä", "a", "ö", "o", "ü", "u", "ß", "ss", "ş", "s", "ç", "c", "ğ", "g", "ı", "i", "ł", "l", "ś", "s",
	"ż", "z", "ź", "z", "ć", "c", "ń", "n", "ą", "a", "ę", "e", "ó", "o", "é", "e", "è", "e", "ê", "e", "á", "a", "à", "a", "í", "i",
	"ñ", "n", "ú", "u", "œ", "oe", "ё", "е")

func flach(s string) string { return flachErsetzer.Replace(strings.ToLower(s)) }

// vergleiche are the forms a word is looked up by: klar, and klar with the
// accents dropped.
func vergleiche(s string) []string {
	a, b := klar(s), klar(flach(s))
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

// nurWort: one word, letters only.
var nurWort = regexp.MustCompile(`^\pL+$`)

func woerterLesen(b []byte) (*woerterbuch, error) {
	var f struct {
		Beziehung    []map[string]any `yaml:"beziehung"`
		Gruppe       []map[string]any `yaml:"gruppe"`
		NichtImNamen []string         `yaml:"nicht_im_namen"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	w := &woerterbuch{arten: map[string][]string{}, gruppen: map[string]string{}, rang: map[string]int{}, nichtName: map[string]bool{}}
	formen := map[string]bool{}
	worte := func(eintrag map[string]any, schluessel string) ([]string, error) {
		var out []string
		for sprache, v := range eintrag {
			if sprache == schluessel {
				continue
			}
			liste, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("%v %s: eine Liste von Wörtern erwartet", eintrag[schluessel], sprache)
			}
			for _, x := range liste {
				s, ok := x.(string)
				if !ok || !nurWort.MatchString(s) {
					return nil, fmt.Errorf("%v %s: „%v“ ist kein einzelnes Wort", eintrag[schluessel], sprache, x)
				}
				out = append(out, s)
				for _, form := range []string{strings.ToLower(s), strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss").Replace(strings.ToLower(s)), flach(s)} {
					formen[form] = true
				}
			}
		}
		return out, nil
	}
	for _, e := range f.Beziehung {
		art, _ := e["art"].(string)
		if !slices.Contains(familienArten, art) {
			return nil, fmt.Errorf("beziehung: „%s“ ist keine Art der Fragen (%s)", art, strings.Join(familienArten, ", "))
		}
		ws, err := worte(e, "art")
		if err != nil {
			return nil, err
		}
		for _, s := range ws {
			for _, v := range vergleiche(s) {
				if !slices.Contains(w.arten[v], art) {
					w.arten[v] = append(w.arten[v], art)
				}
			}
		}
	}
	for i, e := range f.Gruppe {
		kat, _ := e["kategorie"].(string)
		if !slices.Contains(kategorien, kat) {
			return nil, fmt.Errorf("gruppe: „%s“ ist keine Kategorie (%s)", kat, strings.Join(kategorien, ", "))
		}
		w.rang[kat] = i
		ws, err := worte(e, "kategorie")
		if err != nil {
			return nil, err
		}
		for _, s := range ws {
			for _, v := range vergleiche(s) {
				if _, da := w.gruppen[v]; !da {
					w.gruppen[v] = kat
				}
			}
		}
	}
	for _, s := range f.NichtImNamen {
		for _, v := range vergleiche(s) {
			w.nichtName[v] = true
		}
	}
	if len(w.arten) == 0 {
		return nil, fmt.Errorf("keine Beziehungswörter")
	}
	for form := range formen {
		w.formen = append(w.formen, form)
	}
	slices.Sort(w.formen)
	return w, nil
}

// wortArten are the kinds the word hints at; none for another word.
func (w *woerterbuch) wortArten(wort string) []string {
	for _, v := range vergleiche(wort) {
		if a := w.arten[v]; len(a) > 0 {
			return a
		}
	}
	return nil
}

// Worttreffer is a relation word in a text: as it is written there, the
// kinds it hints at, and the few words around it (the cue a reason quotes).
type Worttreffer struct {
	Wort  string
	Arten []string
	Zitat string
}

// zitatWorte is how many words a cue takes before and after the word.
const (
	zitatVor    = 2
	zitatNach   = 2
	zitatLaenge = 60
)

// imText are the relation words of text, each once, in their order.
func (w *woerterbuch) imText(text string) (out []Worttreffer) {
	felder := strings.Fields(text)
	gesehen := map[string]bool{}
	for i, f := range felder {
		for _, tok := range strings.FieldsFunc(f, func(r rune) bool { return !unicode.IsLetter(r) }) {
			arten := w.wortArten(tok)
			if len(arten) == 0 || gesehen[klar(tok)] {
				continue
			}
			gesehen[klar(tok)] = true
			z := strings.Join(felder[max(0, i-zitatVor):min(len(felder), i+zitatNach+1)], " ")
			if r := []rune(z); len(r) > zitatLaenge {
				z = string(r[:zitatLaenge]) + "…"
			}
			out = append(out, Worttreffer{Wort: tok, Arten: arten, Zitat: strings.TrimRight(z, ",;:")})
		}
	}
	return out
}

// imNamen is the relation word a contact's name begins with („Oma Hilde“,
// „Mama ❤️“) and its kinds; none when it is also a name („Anne Berg“).
func (w *woerterbuch) imNamen(name string) (string, []string) {
	ws := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) })
	if len(ws) == 0 {
		return "", nil
	}
	for _, v := range vergleiche(ws[0]) {
		if w.nichtName[v] {
			return "", nil
		}
	}
	if a := w.wortArten(ws[0]); len(a) > 0 {
		return ws[0], a
	}
	return "", nil
}

// imTitel is the category a group's title suggests and the word that says
// so: the first category of the file any of its words has; a family word
// of beziehung („Oma & Opa“) makes Familie. "" none.
func (w *woerterbuch) imTitel(titel string) (wort, kat string) {
	bester := len(kategorien)
	for _, tok := range strings.FieldsFunc(titel, func(r rune) bool { return !unicode.IsLetter(r) }) {
		for _, v := range vergleiche(tok) {
			if k, da := w.gruppen[v]; da && w.rang[k] < bester {
				wort, kat, bester = tok, k, w.rang[k]
			}
		}
	}
	if kat != "" {
		return wort, kat
	}
	for _, tok := range strings.FieldsFunc(titel, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if len(w.wortArten(tok)) > 0 {
			return tok, kategorieFamilie
		}
	}
	return "", ""
}

// muster is the filter msgvault's query uses (RE2, as DuckDB's
// regexp_matches reads it, on lower(snippet)): any form of a word, not
// within another word. It is wider than imText (a word's other case
// forms), never narrower; imText decides.
func (w *woerterbuch) muster() string {
	formen := make([]string, len(w.formen))
	for i, f := range w.formen {
		formen[i] = regexp.QuoteMeta(f)
	}
	return `(^|\PL)(` + strings.Join(formen, "|") + `)(\PL|$)`
}

package main

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// What the phone books say (as Homes' internal/netz/telefonbuch.go): the
// household's book homes/haushalt-kontakte and the person's mirrored
// handy-kontakte (from the archive's own contact store), firm cards
// aside. The same postal address is a tie; the same company or work
// address (ADR TYPE=work, a company's) is none, only a question,
// and a relation a phone wrote a strong one: vCard 4's RELATED, iOS's
// grouped X-ABRELATEDNAMES with its X-ABLabel, Android's X-ANDROID-CUSTOM
// vnd.android.cursor.item/relation. A relation names its other by UID or
// by name.

// Groups larger than these are no tie (a block of flats, a big firm).
const (
	maxAdresse = 12
	maxFirma   = 8 // a company on more cards is no sign two know each other (owner 2026-11-11)
)

// buchKarte is a card read: its key, name, what ties it, its relations
// (UID or name → label), and what says it is the person's own.
type buchKarte struct {
	key      string
	name     string
	firma    string   // ORG's first part, as written
	adressen []string // each ADR: street and code, else the whole line
	adrText  []string // each ADR as a page says it: the street as written, else the whole line
	arbeit   []string // each work ADR (TYPE=work) as adressen, …
	arbText  []string // … and as adrText: a company's address, never a home
	emails   []string
	tels     []string
	rel      [][2]string
	family   string   // N's first part, for the suggestions (fragen.go)
	geb      string   // BDAY as JJJJ-MM-TT; "" none or without a year
	strassen []string // each ADR's street without its number, with its postcode: a neighbour's
	spitz    []string // NICKNAME's names, as written („Hasi“)
}

// vcardZeile is one content line: its group, name (upper case), parameters
// (names upper case) and raw value.
type vcardZeile struct {
	gruppe, name string
	params       map[string]string
	wert         string
}

func vcardZeileLesen(z string) vcardZeile {
	p, w := wert(z)
	name := eigenschaft(z)
	g := ""
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		g, name = name[:i], name[i+1:]
	}
	out := vcardZeile{gruppe: g, name: name, params: map[string]string{}, wert: w}
	for _, teil := range paramTeile(p) {
		k, v, _ := strings.Cut(teil, "=")
		k = strings.ToUpper(strings.TrimSpace(k))
		v = strings.Trim(v, `"`)
		if alt := out.params[k]; alt != "" {
			v = alt + "," + v
		}
		out.params[k] = v
	}
	return out
}

// paramTeile splits parameters at the semicolons outside quotes.
func paramTeile(p string) (out []string) {
	if p == "" {
		return nil
	}
	quote, start := false, 0
	for i, r := range p {
		switch {
		case r == '"':
			quote = !quote
		case r == ';' && !quote:
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return append(out, p[start:])
}

// vText unescapes a TEXT value: \n is a newline, any other escaped
// character itself.
func vText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		if s[i] == 'n' || s[i] == 'N' {
			b.WriteByte('\n')
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// vTeile splits a structured value (ORG, ADR) at its unescaped
// semicolons, empty parts kept in their place; an escaped newline is a
// space, any other escaped character itself.
func vTeile(v string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(v); i++ {
		switch {
		case v[i] == '\\' && i+1 < len(v):
			i++
			if v[i] == 'n' || v[i] == 'N' {
				cur.WriteByte(' ')
			} else {
				cur.WriteByte(v[i])
			}
		case v[i] == ';':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(v[i])
		}
	}
	return append(out, cur.String())
}

// joinSome joins the parts that are not blank.
func joinSome(sep string, ps ...string) string {
	var keep []string
	for _, p := range ps {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, sep)
}

// klar is a name or an address for comparing: lower case, umlauts
// spelled out (owner 2026-11-10: „Jürgen“ is „Juergen“; ä/ae, ö/oe,
// ü/ue, ß/ss count the same), letters and digits only. Homes' namen.Plain
// writes ä as a; nothing compares the two's output.
func klar(s string) string {
	r := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "é", "e", "è", "e", "á", "a", "à", "a", "ó", "o", "í", "i", "&", "und")
	s = r.Replace(strings.ToLower(s))
	var b strings.Builder
	for _, c := range s {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// kartenLesen are the cards of one text (one or several VCARDs), firms
// and nameless ones left out, each keyed buch+UID with its relations.
func kartenLesen(buch, text string) (out []buchKarte) {
	var zeilen []vcardZeile
	tiefe := 0
	for _, z := range entfalten([]byte(text)) {
		zl := vcardZeileLesen(z)
		switch {
		case zl.name == "BEGIN" && strings.EqualFold(strings.TrimSpace(zl.wert), "VCARD"):
			if tiefe == 0 {
				zeilen = nil
			}
			tiefe++
		case zl.name == "END" && strings.EqualFold(strings.TrimSpace(zl.wert), "VCARD"):
			tiefe--
			if tiefe == 0 {
				if k, ok := karteAus(buch, zeilen); ok {
					out = append(out, k)
				}
			}
			tiefe = max(tiefe, 0)
		case tiefe == 1:
			zeilen = append(zeilen, zl)
		}
	}
	return out
}

// karteAus reads one card's lines; ok false for a firm or a card
// without UID or name.
func karteAus(buch string, zeilen []vcardZeile) (buchKarte, bool) {
	var k buchKarte
	uid, firm := "", false
	for _, z := range zeilen {
		v := vText(z.wert)
		switch z.name {
		case "UID":
			uid = v
		case "FN":
			k.name = strings.Join(strings.Fields(v), " ")
		case "ORG":
			k.firma = strings.TrimSpace(vTeile(z.wert)[0])
		case "N":
			k.family = strings.TrimSpace(vTeile(z.wert)[0])
		case "BDAY":
			k.geb = gebLesen(v)
		case "NICKNAME":
			k.spitz = spitznamen(k.spitz, z.wert)
		case "ADR":
			k.adresseDazu(z)
		case "EMAIL":
			if v = strings.TrimSpace(strings.TrimPrefix(v, "mailto:")); v != "" {
				k.emails = append(k.emails, v)
			}
		case "TEL":
			if v = strings.TrimSpace(strings.TrimPrefix(v, "tel:")); v != "" {
				k.tels = append(k.tels, v)
			}
		case "CATEGORIES":
			for _, c := range strings.Split(z.wert, ",") {
				if strings.EqualFold(strings.TrimSpace(vText(c)), "Firma") {
					firm = true
				}
			}
		}
	}
	if uid == "" || k.name == "" || firm {
		return k, false
	}
	k.key = buch + uid
	k.rel = relationen(zeilen)
	return k, true
}

// adresseDazu reads one ADR: a home's street for the neighbours and its
// address, or a work address (TYPE=work; vCard 2.1's ADR;WORK), which is
// a company's: no street, apart from the homes (owner 2026-11-11).
func (k *buchKarte) adresseDazu(z vcardZeile) {
	p := append(vTeile(z.wert), make([]string, 7)...)
	_, alt := z.params["WORK"]
	arbeit := alt || slices.Contains(strings.Split(strings.ToLower(z.params["TYPE"]), ","), "work")
	if s := strasse(p[2]); s != "" && strings.TrimSpace(p[5]) != "" && !arbeit {
		k.strassen = append(k.strassen, s+"|"+strings.TrimSpace(p[5]))
	}
	a := joinSome(", ", p[0], p[1], p[2], joinSome(" ", p[5], p[3]), p[4], p[6])
	if a == "" {
		return
	}
	text := a
	if strings.TrimSpace(p[2]) != "" {
		a, text = strings.TrimSpace(p[2])+strings.TrimSpace(p[5]), joinSome(" ", p[2])
	}
	if arbeit {
		k.arbeit, k.arbText = append(k.arbeit, a), append(k.arbText, text)
	} else {
		k.adressen, k.adrText = append(k.adressen, a), append(k.adrText, text)
	}
}

// spitznamen adds a NICKNAME's names (a list, split at its unescaped
// commas) to have, each once (umlauts alike).
func spitznamen(have []string, wert string) []string {
	var teile []string
	var cur strings.Builder
	for i := 0; i < len(wert); i++ {
		switch {
		case wert[i] == '\\' && i+1 < len(wert):
			cur.WriteString(wert[i : i+2])
			i++
		case wert[i] == ',':
			teile = append(teile, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(wert[i])
		}
	}
	for _, t := range append(teile, cur.String()) {
		t = strings.Join(strings.Fields(vText(t)), " ")
		if klar(t) != "" && !slices.ContainsFunc(have, func(h string) bool { return klar(h) == klar(t) }) {
			have = append(have, t)
		}
	}
	return have
}

// gebLesen is a BDAY as JJJJ-MM-TT; "" for one without a year or unread.
func gebLesen(v string) string {
	d, _, _ := strings.Cut(strings.TrimSpace(v), "T")
	for _, l := range []string{"20060102", "2006-01-02"} {
		if t, err := time.Parse(l, d); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

// strasse is a street compared without its house number: „Gartenweg 2“
// and „Gartenweg 7“ are one street.
func strasse(s string) string {
	var ws []string
	for _, w := range strings.Fields(s) {
		if !strings.ContainsFunc(w, unicode.IsDigit) {
			ws = append(ws, w)
		}
	}
	return klar(strings.Join(ws, ""))
}

// android are Android's relation types (ContactsContract.CommonDataKinds.Relation).
var android = map[string]string{"1": "assistant", "2": "brother", "3": "child", "4": "partner", "5": "father", "6": "friend", "7": "manager",
	"8": "mother", "9": "parent", "10": "partner", "11": "referredby", "12": "relative", "13": "sister", "14": "spouse"}

// relationen are a card's relations, each its other (UID or name) and label.
func relationen(zeilen []vcardZeile) (out [][2]string) {
	labels := map[string]string{}
	for _, z := range zeilen {
		if z.gruppe != "" && z.name == "X-ABLABEL" {
			labels[z.gruppe] = vText(z.wert)
		}
	}
	for _, z := range zeilen {
		v := strings.TrimSpace(vText(z.wert))
		switch z.name {
		case "RELATED":
			typ, _, _ := strings.Cut(z.params["TYPE"], ",")
			out = append(out, [2]string{v, typ})
		case "X-ABRELATEDNAMES":
			out = append(out, [2]string{v, labels[z.gruppe]})
		case "X-ANDROID-CUSTOM":
			f := append(strings.Split(z.wert, ";"), "", "", "")
			if f[0] == "vnd.android.cursor.item/relation" {
				eigen := ""
				if f[2] == "0" {
					eigen = f[3]
				}
				out = append(out, [2]string{vText(f[1]), eigen + android[f[2]]})
			}
		}
	}
	return out
}

// gruppen are the ties of the same address, the same company and the
// same work address, each with its value as the first card wrote it
// (Wie: the street, the ORG), so a page can say which (owner 2026-11-11).
func gruppen(ks []buchKarte) (out []festBand) {
	adr, firma, arbeit, wie := map[string][]string{}, map[string][]string{}, map[string][]string{}, map[string]string{}
	dazu := func(m map[string][]string, art, key string, roh, text []string) {
		for i, a := range roh {
			if a = klar(a); a != "" {
				m[a] = append(m[a], key)
				wie[art+a] = cmp.Or(wie[art+a], text[i])
			}
		}
	}
	for _, k := range ks {
		dazu(adr, bandAdresse, k.key, k.adressen, k.adrText)
		dazu(arbeit, bandArbeit, k.key, k.arbeit, k.arbText)
		dazu(firma, bandFirma, k.key, []string{k.firma}, []string{k.firma})
	}
	for art, m := range map[string]map[string][]string{bandAdresse: adr, bandFirma: firma, bandArbeit: arbeit} {
		grenze := map[string]int{bandAdresse: maxAdresse, bandFirma: maxFirma, bandArbeit: maxFirma}[art]
		for wert, keys := range m {
			if len(keys) > grenze {
				continue
			}
			for i, a := range keys {
				for _, b := range keys[i+1:] {
					out = append(out, festBand{A: a, B: b, Art: art, Wie: wie[art+wert]})
				}
			}
		}
	}
	return out
}

// beziehungen are the relations whose other is a contact of the same book
// (by UID, else by name) or of the other.
func beziehungen(ks []buchKarte) (out []festBand) {
	uid, name := map[string]string{}, map[string]string{}
	for _, k := range ks {
		_, u, _ := strings.Cut(k.key, "/")
		uid[strings.TrimPrefix(strings.ToLower(u), "urn:uuid:")] = k.key
		name[klar(k.name)] = k.key
	}
	for _, k := range ks {
		for _, r := range k.rel {
			ziel := uid[strings.TrimPrefix(strings.ToLower(r[0]), "urn:uuid:")]
			if ziel == "" {
				ziel = name[klar(r[0])]
			}
			if ziel != "" && ziel != k.key {
				out = append(out, festBand{A: k.key, B: ziel, Art: bandBeziehung, Wie: beziehungsWort(r[1])})
			}
		}
	}
	return out
}

// beziehungen in German, by the label's letters in lower case.
var beziehungsWoerter = map[string]string{
	"spouse": "Ehepartner/in", "partner": "Partner/in", "child": "Kind", "son": "Sohn", "daughter": "Tochter",
	"parent": "Elternteil", "mother": "Mutter", "father": "Vater", "sibling": "Geschwister", "brother": "Bruder",
	"sister": "Schwester", "friend": "Freund/in", "relative": "Verwandt", "kin": "Verwandt", "colleague": "Kollege/Kollegin",
	"coworker": "Kollege/Kollegin", "manager": "Vorgesetzte/r", "assistant": "Assistenz", "neighbor": "Nachbar/in",
}

// beziehungsWort is a relation's label as the family reads it: a known
// kind in German, iOS's _$!<Spouse>!$_ too; another as written.
func beziehungsWort(s string) string {
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "_$!<"), ">!$_")
	kind := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
	if w, ok := beziehungsWoerter[kind]; ok {
		return w
	}
	return s
}

package main

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"filippo.io/age"
)

// Social exports (owner 2026-11-11: „Runterladen würde ich ja, aber das ist
// entweder ein ‚Teilen' mit der Homes-App oder eine E-Mail-Weiterleitung und
// das System macht den Rest“). The person downloads Facebook's „Deine
// Informationen herunterladen“ (JSON) or LinkedIn's data archive and shares
// the file to the Homes app; the app recognises it, encrypts it on the
// phone to the Briefkasten's key and uploads it to /eingang/sozial/<name>.age.
// Homes never reads it. While the vault is open the Postbote decrypts it
// into the work folder, reads the friends or connections and keeps them in
// the vault (umfeld/sozial.json), additive: an entry is never removed.
//
// What they do (umfeld): names to find under „+ Beziehung“, and reasons on
// the tie between the person and a contact they match (LinkedIn by e-mail,
// both by the name with umlauts alike). Nothing is written to an address
// book, no contact is made, nothing leaves the vault.
//
// Mail forwarding is not possible: both platforms send only a notice with a
// link that needs their login, never the data itself.

const (
	sozialFormat  = "archiv-sozial"
	sozialVersion = 1
	// sozialMax is how much of one export (or one file in its ZIP) is read:
	// a friends list or Connections.csv is a few MB even for thousands.
	sozialMax = 64 << 20
)

const (
	saysSozialKaputt = "Die Datei %s ließ sich nicht entschlüsseln. Sie bleibt im Eingang; teile sie aus der Homes-App noch einmal."
	saysSozialFremd  = "Die Datei %s ist weder Facebooks Freundesliste noch LinkedIns Connections.csv. Sie wurde aus dem Eingang genommen."
	saysSozialHTML   = "Der Facebook-Export %s ist im Format HTML. Lade ihn bei Facebook noch einmal im Format JSON herunter und teile ihn dann."
)

var (
	errSozialFremd = errors.New("kein Facebook- oder LinkedIn-Export")
	errSozialHTML  = errors.New("Facebook-Export als HTML")
)

// sozialEintrag is one friend or connection: where from, the name, and
// what that source says (LinkedIn: e-mail if shared, company, position).
type sozialEintrag struct {
	Quelle   string `json:"q"` // facebook, linkedin
	Name     string `json:"n"`
	Mail     string `json:"m,omitempty"`
	Firma    string `json:"f,omitempty"`
	Position string `json:"p,omitempty"`
	Seit     string `json:"seit,omitempty"` // JJJJ-MM-TT: friends since, connected on
}

type sozialDatei struct {
	Format    string          `json:"format"`
	Version   int             `json:"version"`
	Eintraege []sozialEintrag `json:"eintraege"`
}

func (i Innen) umfeldSozial() string { return filepath.Join(i.Umfeld(), "sozial.json") }

func sozialLaden(innen Innen) []sozialEintrag {
	var d sozialDatei
	if b, err := os.ReadFile(innen.umfeldSozial()); err == nil {
		if json.Unmarshal(b, &d) != nil || d.Format != sozialFormat || d.Version != sozialVersion {
			return nil
		}
	}
	return d.Eintraege
}

// sozialKey: one entry per source, name and e-mail.
func sozialKey(e sozialEintrag) string {
	return e.Quelle + "|" + klar(e.Name) + "|" + strings.ToLower(e.Mail)
}

// sozialMischen is alt with neu: a new entry is added, a known one takes
// what neu says of it; none is removed. Sorted, so the file is stable.
func sozialMischen(alt, neu []sozialEintrag) []sozialEintrag {
	je := map[string]sozialEintrag{}
	for _, e := range alt {
		je[sozialKey(e)] = e
	}
	for _, e := range neu {
		a := je[sozialKey(e)]
		je[sozialKey(e)] = sozialEintrag{Quelle: e.Quelle, Name: e.Name, Mail: e.Mail,
			Firma: cmp.Or(e.Firma, a.Firma), Position: cmp.Or(e.Position, a.Position), Seit: cmp.Or(e.Seit, a.Seit)}
	}
	out := slices.Collect(maps.Values(je))
	slices.SortFunc(out, func(a, b sozialEintrag) int { return strings.Compare(sozialKey(a), sozialKey(b)) })
	return out
}

// sozialLesen reads one export by its content, not its name: a ZIP
// (Facebook's or LinkedIn's whole archive), Facebook's friends JSON, or
// LinkedIn's Connections.csv. Anything else is errSozialFremd.
func sozialLesen(pfad string) (quelle string, es []sozialEintrag, err error) {
	if z, zerr := zip.OpenReader(pfad); zerr == nil {
		defer z.Close()
		return sozialAusZip(&z.Reader)
	}
	f, err := os.Open(pfad)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, sozialMax+1))
	if err != nil {
		return "", nil, err
	}
	if len(b) > sozialMax {
		return "", nil, errSozialFremd
	}
	return sozialAusText(b)
}

// sozialAusText is a single file: JSON is Facebook's, else LinkedIn's CSV.
func sozialAusText(b []byte) (string, []sozialEintrag, error) {
	b = bytes.TrimPrefix(b, []byte("\ufeff"))
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '{' {
		es, err := facebookLesen(t)
		return "facebook", es, err
	}
	es, err := linkedinLesen(b)
	return "linkedin", es, err
}

// facebookFreunde are where Facebook's JSON export has kept the friends
// list: connections/friends/your_friends.json (2024), friends_and_followers/
// friends.json (2022), friends/friends.json (before). removed_friends,
// sent and received requests are other files and never read.
var facebookFreunde = []string{"connections/friends/your_friends.", "friends_and_followers/friends.", "friends/friends.", "friends/your_friends."}

func facebookFreundeDatei(name string) (json, html bool) {
	n := strings.ToLower(name)
	for _, p := range facebookFreunde {
		ist := func(endung string) bool { return n == p+endung || strings.HasSuffix(n, "/"+p+endung) }
		if ist("json") {
			return true, false
		}
		if ist("html") {
			return false, true
		}
	}
	return false, false
}

func sozialAusZip(z *zip.Reader) (string, []sozialEintrag, error) {
	html := false
	for _, f := range z.File {
		j, h := facebookFreundeDatei(f.Name)
		html = html || h
		switch {
		case j:
			b, err := zipLesen(f)
			if err != nil {
				return "", nil, err
			}
			es, err := facebookLesen(b)
			return "facebook", es, err
		case strings.EqualFold(path.Base(f.Name), "Connections.csv"):
			b, err := zipLesen(f)
			if err != nil {
				return "", nil, err
			}
			es, err := linkedinLesen(b)
			return "linkedin", es, err
		}
	}
	if html {
		return "facebook", nil, errSozialHTML
	}
	return "", nil, errSozialFremd
}

func zipLesen(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > sozialMax {
		return nil, errSozialFremd
	}
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, sozialMax+1))
	if err == nil && len(b) > sozialMax {
		err = errSozialFremd
	}
	return b, err
}

// facebookLesen reads {"friends_v2": [{"name", "timestamp"}]} (older:
// "friends"). Facebook writes each UTF-8 byte of a name as its own \u00XX
// escape („JÃ¼rgen“), so a name of such runes is read back as bytes.
func facebookLesen(b []byte) ([]sozialEintrag, error) {
	var d map[string]json.RawMessage
	if json.Unmarshal(b, &d) != nil {
		return nil, errSozialFremd
	}
	roh, da := d["friends_v2"]
	if !da {
		if roh, da = d["friends"]; !da {
			return nil, errSozialFremd
		}
	}
	var fs []struct {
		Name      string `json:"name"`
		Timestamp int64  `json:"timestamp"`
	}
	if json.Unmarshal(roh, &fs) != nil {
		return nil, errSozialFremd
	}
	var out []sozialEintrag
	for _, f := range fs {
		name := strings.TrimSpace(facebookText(f.Name))
		if name == "" {
			continue
		}
		e := sozialEintrag{Quelle: "facebook", Name: name}
		if f.Timestamp > 0 {
			e.Seit = time.Unix(f.Timestamp, 0).UTC().Format(time.DateOnly)
		}
		out = append(out, e)
	}
	return out, nil
}

// facebookText undoes Facebook's escaping: runes all below 256 with one
// above 127, that are valid UTF-8 as bytes, are those bytes.
func facebookText(s string) string {
	b := make([]byte, 0, len(s))
	hoch := false
	for _, r := range s {
		if r > 0xFF {
			return s
		}
		hoch = hoch || r > 0x7F
		b = append(b, byte(r))
	}
	if !hoch || !utf8.Valid(b) {
		return s
	}
	return string(b)
}

// linkedinLesen reads Connections.csv: a „Notes:“ preamble, then the
// header First Name, Last Name, URL, Email Address, Company, Position,
// Connected On (URL since 2022; columns found by name). The profile URL is
// not kept.
func linkedinLesen(b []byte) ([]sozialEintrag, error) {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(b, []byte("\ufeff"))))
	r.FieldsPerRecord, r.LazyQuotes = -1, true
	spalte := map[string]int{}
	var out []sozialEintrag
	for {
		z, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errSozialFremd
		}
		if len(spalte) == 0 {
			for i, k := range z {
				spalte[strings.ToLower(strings.TrimSpace(k))] = i
			}
			_, vor := spalte["first name"]
			_, nach := spalte["last name"]
			if !vor || !nach {
				clear(spalte)
			}
			continue
		}
		feld := func(k string) string {
			if i, da := spalte[k]; da && i < len(z) {
				return strings.TrimSpace(z[i])
			}
			return ""
		}
		name := strings.TrimSpace(feld("first name") + " " + feld("last name"))
		if name == "" {
			continue
		}
		out = append(out, sozialEintrag{Quelle: "linkedin", Name: name, Mail: strings.ToLower(feld("email address")),
			Firma: feld("company"), Position: feld("position"), Seit: linkedinDatum(feld("connected on"))})
	}
	if len(spalte) == 0 {
		return nil, errSozialFremd
	}
	return out, nil
}

// linkedinDatum is „12 Mar 2019“ (or an ISO date) as JJJJ-MM-TT; "" else.
func linkedinDatum(s string) string {
	for _, f := range []string{"02 Jan 2006", "2 Jan 2006", time.DateOnly} {
		if t, err := time.Parse(f, s); err == nil {
			return t.Format(time.DateOnly)
		}
	}
	return ""
}

// sozialGrundText is an entry's reason on the person's tie: „kennen sich
// (Facebook seit 2014)“, „Kollege bei ACME GmbH (Entwickler, LinkedIn seit 2019)“.
func sozialGrundText(e sozialEintrag) string {
	seit := ""
	if len(e.Seit) >= 4 {
		seit = " seit " + e.Seit[:4]
	}
	if e.Quelle == "facebook" {
		return "kennen sich (Facebook" + seit + ")"
	}
	wo := "LinkedIn" + seit
	if e.Position != "" {
		wo = e.Position + ", " + wo
	}
	if e.Firma == "" {
		return "verbunden (" + wo + ")"
	}
	return "Kollege bei " + e.Firma + " (" + wo + ")"
}

// sozialZustellen reads one export from the inbox into the vault: decrypted
// into the work folder (removed on every path), read, mixed into
// umfeld/sozial.json, then taken out of the inbox. One that cannot be
// decrypted stays; one that is no export (or Facebook's HTML) goes, since
// no later try could read it, and the page says why.
func (p *Postbote) sozialZustellen(ctx context.Context, innen Innen, id age.Identity, d Datei) error {
	if p.Arbeit != nil {
		defer p.Arbeit()()
	}
	p.gangSetzen(func(g *Gang) { *g = Gang{Was: d.Rel, Quelle: "sozial", Seit: p.now()} })
	defer p.gangSetzen(func(g *Gang) { *g = Gang{} })
	fehler := func(satz string, grund error) error {
		log.Printf("archiv: Export %s: %v", d.Rel, grund)
		if ctx.Err() == nil {
			p.Protokoll.Fehlschlag(Fehler{Am: p.now(), Quelle: "sozial", Datei: d.Rel, Satz: satz})
		}
		return grund
	}
	klartext := filepath.Join(innen.Arbeit(), "sozial-"+zufall())
	defer os.Remove(klartext)
	if err := ageAuspacken(d.Pfad, klartext, id); err != nil {
		return fehler(fmt.Sprintf(saysSozialKaputt, d.Rel), err)
	}
	quelle, es, err := sozialLesen(klartext)
	if err != nil {
		satz := fmt.Sprintf(saysSozialFremd, d.Rel)
		if errors.Is(err, errSozialHTML) {
			satz = fmt.Sprintf(saysSozialHTML, d.Rel)
		}
		if errors.Is(err, errSozialFremd) || errors.Is(err, errSozialHTML) {
			os.Remove(d.Pfad)
		}
		fehler(satz, err)
		return nil // nothing to try again
	}
	alle := sozialMischen(sozialLaden(innen), es)
	if err := jsonImTresor(innen.umfeldSozial(), sozialDatei{Format: sozialFormat, Version: sozialVersion, Eintraege: alle}); err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, d.Rel), err)
	}
	if err := os.Remove(d.Pfad); err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, d.Rel), err)
	}
	log.Printf("archiv: Export %s eingelesen: %d Einträge von %s", d.Rel, len(es), quelle)
	p.Protokoll.FehlerWeg("sozial") // an earlier export's sentence is answered by this one
	p.Protokoll.Eingelesen("sozial", d.Rel, Zahlen{Sozial: len(es)}, p.now())
	if p.SozialNeu != nil {
		p.SozialNeu(innen)
	}
	return nil
}

// sozialSetzen gives the net the social exports and rebuilds it.
func (n *Netz) sozialSetzen(es []sozialEintrag) {
	if len(es) == 0 {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sozial = es
	n.bauen()
}

// sozialBauen finds each entry's node — LinkedIn's e-mail on a card, else
// the one node of that name (umlauts alike; two of that name: none) — and
// keeps its reason there. An entry matching nobody is a node of its own
// („sozial:<name>“), so „+ Beziehung“ finds it; it has no tie and is no
// contact, so an answer about it stays in the vault. n.mu is held.
func (n *Netz) sozialBauen() {
	n.sozialGrund = map[string][]string{}
	if len(n.sozial) == 0 {
		return
	}
	perMail, perName := map[string]string{}, map[string][]string{}
	for _, k := range n.st.Kontakte {
		for _, m := range k.Mails {
			perMail[strings.ToLower(m)] = n.von[k.Key]
		}
	}
	for id, k := range n.knoten {
		perName[klar(k.Name)] = append(perName[klar(k.Name)], id)
	}
	for _, e := range n.sozial {
		name := klar(e.Name)
		id := ""
		if e.Mail != "" {
			id = perMail[strings.ToLower(e.Mail)]
		}
		if id == "" {
			switch ids := perName[name]; {
			case len(ids) > 1 || name == "":
				continue
			case len(ids) == 1:
				id = ids[0]
			default:
				id = "sozial:" + name
				n.knoten[id] = Knoten{ID: id, Name: e.Name}
				perName[name] = []string{id}
			}
		}
		if g := sozialGrundText(e); !slices.Contains(n.sozialGrund[id], g) {
			n.sozialGrund[id] = append(n.sozialGrund[id], g)
		}
	}
}

// sozialGruende are the social reasons of a tie of the person's; n.mu is held.
func (n *Netz) sozialGruende(k Kante) []string {
	switch n.ich {
	case "":
		return nil
	case k.A:
		return n.sozialGrund[k.B]
	case k.B:
		return n.sozialGrund[k.A]
	}
	return nil
}

// SozialNeu: a social export went into the vault; the kept net takes it.
func (u *Umfeld) SozialNeu(innen Innen) {
	if u.Eingerichtet() {
		u.laden(innen)
	}
}

// sozialKarte is the sources' card for Facebook and LinkedIn: how the
// exports come, that mail cannot bring them, and what the vault holds.
func (w *Web) sozialKarte(b *strings.Builder, offen bool, qa quelleAuf) {
	innen, auf := w.Tresor.Innen()
	je := map[string]int{}
	if offen && auf {
		for _, e := range sozialLaden(innen) {
			je[e.Quelle]++
		}
	}
	zeile := w.letzteZeile("sozial", "Eintrag", "Einträge")
	if len(je) > 0 && zeile != "meldet einen Fehler" {
		zeile = anzahl(je["facebook"], "Freund", "Freunde") + ", " + anzahl(je["linkedin"], "Kontakt", "Kontakte")
	}
	quelleKopf(b, qa, "sozial", "Facebook und LinkedIn", zeile)
	if offen && auf {
		if len(je) == 0 {
			b.WriteString(`<p class=k>Noch nichts im Archiv.</p>`)
		} else {
			fmt.Fprintf(b, `<p>Im Archiv: %s aus Facebook, %s aus LinkedIn.</p>`, esc(anzahl(je["facebook"], "Freund", "Freunde")), esc(anzahl(je["linkedin"], "Kontakt", "Kontakte")))
		}
	}
	if n := wartende(w.Eingang)["sozial"].Wartend; n > 0 {
		fmt.Fprintf(b, `<p>%s im Eingang, verschlüsselt. Beim Öffnen kommen sie ins Archiv.</p>`, esc(anzahl(n, "Datei wartet", "Dateien warten")))
	}
	w.fehlerVon(b, "sozial")
	b.WriteString(`<p class=k>So kommen sie her: Bei Facebook „Deine Informationen herunterladen“ (Format JSON, mindestens „Freunde“), bei LinkedIn „Kopie deiner Daten anfordern“ (mit „Kontakte“). Die Datei auf dem Handy teilen → „Ins Archiv“ (Homes-App). Die App verschlüsselt sie schon auf dem Handy für dein Archiv.</p>`)
	b.WriteString(`<p class=k>Eine E-Mail-Weiterleitung geht hier nicht: Facebook und LinkedIn schicken nur einen Link, der ihre Anmeldung braucht, nie die Daten selbst.</p>`)
	b.WriteString(`<p class=k>Die Namen helfen bei „+ Beziehung“ und als Grund bei „Steht nahe“. Sie bleiben in deinem Archiv: kein Kontakt wird angelegt, kein Adressbuch geändert.</p>` + quelleEnde)
}

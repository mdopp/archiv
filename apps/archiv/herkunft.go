package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Home town and employer from mail (archiv#44, the rest of #42): for a
// contact the phone books know, the end of their newest mails in the
// archive (the signature, „Grüße aus …“, the company under the name) and
// their address's domain go to the box's own model, which answers where
// they live and whom they work for, each with how sure it is. The same
// rules as the relation hints (hinweise.go): only while the vault is
// open, the lease asked before each mail, a lease or a refusal stops the
// round; what the model read is kept in the vault by msgvault's id
// (umfeld/herkunft.json), so no mail is sent twice, nothing of a text or
// an answer is logged. A field one of the contact's cards already has
// (an address, an ORG) is not suggested.
//
// Readings at modellSicher or more add up by value (klar: umlauts and
// case alike); the value with the most, alone, is the suggestion. The
// person's „Stimmt“ or „Stimmt nicht“ is kept in the vault
// (umfeld/herkunft-antworten.json); confirmed ones stay there: the Homes
// packet has no line for them yet.

const (
	herkunftJePerson = 2   // mails of one contact the model reads at most
	herkunftTreffer  = 400 // the newest mails from known senders a round reads from msgvault
	herkunftText     = 700 // characters of a mail's end the model gets
	herkunftWert     = 60  // characters of a town or an employer at most
	herkunftZeigen   = 30
)

const (
	feldOrt    = "ort"
	feldArbeit = "arbeit"
)

// herkunftLesung is what the model read in one mail: whose (the node),
// when it was read, and the town and the employer with their sureness.
type herkunftLesung struct {
	Am        time.Time `json:"am"`
	Wer       string    `json:"wer"`
	Gesendet  time.Time `json:"g,omitzero"`
	Ort       string    `json:"o,omitempty"`
	OrtSicher float64   `json:"os,omitempty"`
	Arbeit    string    `json:"a,omitempty"`
	ArbSicher float64   `json:"as,omitempty"`
}

// herkunftAntwort is the person's answer about a field's value.
type herkunftAntwort struct {
	Wert string    `json:"w"`
	Ja   bool      `json:"ja"`
	Am   time.Time `json:"am"`
}

func (i Innen) umfeldHerkunft() string { return filepath.Join(i.Umfeld(), "herkunft.json") }
func (i Innen) umfeldHerkunftAntworten() string {
	return filepath.Join(i.Umfeld(), "herkunft-antworten.json")
}

func herkunftLaden(innen Innen) map[string]herkunftLesung {
	out := map[string]herkunftLesung{}
	if b, err := os.ReadFile(innen.umfeldHerkunft()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func herkunftAntwortenLaden(innen Innen) map[string]herkunftAntwort {
	out := map[string]herkunftAntwort{}
	if b, err := os.ReadFile(innen.umfeldHerkunftAntworten()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// herkunftSQL is the one read-only statement: the newest herkunftTreffer
// mails (no list, not deleted, not the person's own), herkunftJePerson per
// sender address, with that address.
func herkunftSQL() string {
	return fmt.Sprintf(`WITH m AS (
  SELECT m.id, m.sent_at, m.sender_id,
    row_number() OVER (PARTITION BY m.sender_id ORDER BY m.sent_at DESC, m.id) AS r
  FROM messages m JOIN conversations c ON c.id = m.conversation_id
  WHERE c.conversation_type = 'email_thread' AND m.deleted_at IS NULL AND COALESCE(m.list_id, '') = ''
    AND NOT COALESCE(m.is_from_me, false) AND m.sender_id IS NOT NULL
)
SELECT CAST(m.id AS VARCHAR) AS nachricht, strftime(m.sent_at, '%%Y-%%m-%%dT%%H:%%M:%%SZ') AS am, p.email_address AS mail
FROM m JOIN participants p ON p.id = m.sender_id
WHERE m.r <= %d AND COALESCE(p.email_address, '') <> ''
ORDER BY m.sent_at DESC, m.id
LIMIT %d`, herkunftJePerson, herkunftTreffer)
}

// rohMail is a mail as the statement answers it.
type rohMail struct {
	ID, Mail string
	Am       time.Time
}

// HerkunftMails reads the newest mails of each sender through msgvault's API.
func (m *MsgvaultAPI) HerkunftMails(ctx context.Context) ([]rohMail, error) {
	r, err := m.abfrage(ctx, herkunftSQL())
	if err != nil {
		return nil, err
	}
	return mailsLesen(r)
}

// mailsLesen turns the rows into mails, by column name.
func mailsLesen(r abfrageAntwort) ([]rohMail, error) {
	spalte := map[string]int{}
	for i, c := range r.Columns {
		spalte[c] = i
	}
	for _, c := range []string{"nachricht", "am", "mail"} {
		if _, da := spalte[c]; !da {
			return nil, fmt.Errorf("msgvault-Abfrage ohne Spalte %q", c)
		}
	}
	var out []rohMail
	for _, z := range r.Rows {
		if len(z) < len(r.Columns) || z[spalte["nachricht"]] == nil || z[spalte["mail"]] == nil {
			continue
		}
		t, _ := time.Parse(time.RFC3339, fmt.Sprint(z[spalte["am"]]))
		out = append(out, rohMail{ID: fmt.Sprint(z[spalte["nachricht"]]), Mail: strings.TrimSpace(fmt.Sprint(z[spalte["mail"]])), Am: t})
	}
	return out, nil
}

// Text is a message's body through msgvault's API (plain text, else HTML).
func (m *MsgvaultAPI) Text(ctx context.Context, id string) (string, error) {
	var a struct {
		Body string `json:"body"`
	}
	if !nurZiffern.MatchString(id) {
		return "", fmt.Errorf("keine Nachrichten-Id")
	}
	if err := m.rufe(ctx, http.MethodGet, "/api/v1/messages/"+id, nil, nil, &a); err != nil {
		return "", err
	}
	return a.Body, nil
}

var (
	htmlWeg   = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)\s*>`)
	htmlZeile = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|tr|li|h[1-6])\s*>`)
	htmlTag   = regexp.MustCompile(`<[^>]*>`)
	// zitatKopf: where a reply quotes the mail before it
	zitatKopf = regexp.MustCompile(`(?im)^\s*(am .{3,80} schrieb.*:|on .{3,80} wrote:|-{2,}\s*(original|ursprüngliche)|von:\s|from:\s|le .{3,80} a écrit|el .{3,80} escribió)`)
)

// mailEnde is the end of a mail's own text: HTML made text, a quoted
// mail and quoted lines left out, at most herkunftText characters.
func mailEnde(body string) string {
	if strings.Contains(body, "</") || strings.Contains(body, "<br") {
		body = htmlWeg.ReplaceAllString(body, "")
		body = htmlTag.ReplaceAllString(htmlZeile.ReplaceAllString(body, "\n"), "")
		body = html.UnescapeString(body)
	}
	if loc := zitatKopf.FindStringIndex(body); loc != nil && loc[0] > 0 {
		body = body[:loc[0]]
	}
	var zeilen []string
	for _, z := range strings.Split(body, "\n") {
		z = strings.TrimSpace(z)
		if z != "" && !strings.HasPrefix(z, ">") {
			zeilen = append(zeilen, z)
		}
	}
	r := []rune(strings.Join(zeilen, "\n"))
	if len(r) > herkunftText {
		r = append([]rune("…"), r[len(r)-herkunftText:]...)
	}
	return string(r)
}

// freemail are domains of everyone: they say nothing of an employer.
var freemail = []string{"gmail.com", "googlemail.com", "web.de", "gmx.de", "gmx.net", "gmx.at", "gmx.ch", "t-online.de", "freenet.de", "arcor.de",
	"posteo.de", "mailbox.org", "icloud.com", "me.com", "mac.com", "aol.com", "aol.de", "proton.me", "protonmail.com", "online.de", "email.de", "vodafone.de", "yandex.ru", "mail.ru"}

// firmenDomain is the domain of an address when it may be an employer's; "" a free one.
func firmenDomain(mail string) string {
	_, d, _ := strings.Cut(strings.ToLower(mail), "@")
	frei := slices.Contains(freemail, d) || slices.ContainsFunc([]string{"yahoo.", "outlook.", "hotmail.", "live.", "msn."}, func(p string) bool { return strings.HasPrefix(d, p) })
	if frei || !strings.Contains(d, ".") {
		return ""
	}
	return d
}

// herkunftSystem is the model's instruction: the same for every mail.
const herkunftSystem = `Du hilfst einer Person, ihr Adressbuch zu ergänzen. Du bekommst das Ende einer E-Mail (meist Grußformel und Signatur) und die Domain der Absenderadresse. ` +
	`Lies daraus, wo der Absender wohnt und bei wem er arbeitet. ` +
	`Antworte nur mit JSON, genau in dieser Form: {"ort":"Ulm","ort_sicher":0.8,"arbeitgeber":"Werk AG","arbeitgeber_sicher":0.9}. ` +
	`„ort“ ist die Stadt, in der der Absender lebt („Grüße aus Ulm“, eine private Adresse); die Adresse einer Firma ist kein Wohnort. ` +
	`„arbeitgeber“ ist die Firma oder Organisation, für die er arbeitet (Signatur, Firmenadresse, die Domain einer Firma). ` +
	`Was die Mail nicht nahelegt, bleibt leer ("") mit Sicherheit 0; rate nicht. Zitierte ältere Mails und Werbung gehören nicht dazu. ` +
	`Die Sicherheit ist eine Zahl von 0 bis 1.`

// herkunftFrage is the model's question about one mail of wer.
func herkunftFrage(wer, domain, ende string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Absender: %s\n", cmp.Or(wer, "unbekannt"))
	if domain != "" {
		fmt.Fprintf(&b, "Domain der Absenderadresse: %s\n", domain)
	}
	fmt.Fprintf(&b, "\nEnde der E-Mail:\n«%s»\n", ende)
	return b.String()
}

// herkunftAntwortLesen reads the model's answer strictly: one JSON
// object, nothing after it; a value longer than herkunftWert, with a line
// break or with a sureness outside 0 to 1 is left out. Unreadable: none.
func herkunftAntwortLesen(s string) (l herkunftLesung, ok bool) {
	s = strings.TrimSpace(s)
	if rest, cut := strings.CutPrefix(s, "```"); cut {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(rest, "json")), "```"))
	}
	var a struct {
		Ort       *string  `json:"ort"`
		OrtSicher *float64 `json:"ort_sicher"`
		Arbeit    *string  `json:"arbeitgeber"`
		ArbSicher *float64 `json:"arbeitgeber_sicher"`
	}
	d := json.NewDecoder(strings.NewReader(s))
	if d.Decode(&a) != nil || a.Ort == nil && a.Arbeit == nil {
		return l, false
	}
	if _, err := d.Token(); err != io.EOF {
		return l, false
	}
	wert := func(v *string, sicher *float64) (string, float64) {
		if v == nil || sicher == nil || *sicher < 0 || *sicher > 1 {
			return "", 0
		}
		w := strings.Join(strings.Fields(*v), " ")
		if w == "" || len([]rune(w)) > herkunftWert || strings.ContainsAny(*v, "\n<>{}") {
			return "", 0
		}
		return w, *sicher
	}
	l.Ort, l.OrtSicher = wert(a.Ort, a.OrtSicher)
	l.Arbeit, l.ArbSicher = wert(a.Arbeit, a.ArbSicher)
	return l, true
}

// herkunftKandidat is a mail the round may send, with its sender's node.
type herkunftKandidat struct {
	rohMail
	wer string
}

// herkunftKandidaten are the mails of roh worth reading, newest first:
// not read before, a known sender (not the person, no bulk address)
// missing a town or an employer, at most herkunftJePerson read of each.
func (n *Netz) herkunftKandidaten(roh []rohMail, ks []buchKarte, ls map[string]herkunftLesung) (out []herkunftKandidat) {
	je := map[string]int{}
	for _, l := range ls {
		je[l.Wer]++
	}
	for _, r := range roh {
		if _, da := ls[r.ID]; da {
			continue
		}
		wer := n.knotenVon(gespraechsTeilnehmer{Mail: r.Mail}, ks)
		if wer == "" || wer == n.Ich() || je[wer] >= herkunftJePerson || !n.herkunftOffen(wer) {
			continue
		}
		je[wer]++
		out = append(out, herkunftKandidat{rohMail: r, wer: wer})
	}
	return out
}

// name is a node's name; "" none.
func (n *Netz) name(id string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.knoten[id].Name
}

// herkunftOffen: a card of wer misses a town or an employer.
func (n *Netz) herkunftOffen(wer string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.merkmale[wer]
	return len(m.strassen) == 0 || m.firma == ""
}

// herkunftRunde reads the candidates' mails and asks the model about
// each, the lease asked before each; each answer is kept in the vault at
// once. Taken, silent or closing: it stops, the rest waits. Counts only
// in the log.
func (u *Umfeld) herkunftRunde(ctx context.Context, innen Innen, n *Netz, ks []buchKarte) {
	if !u.Modell.eingerichtet() || u.Msgvault == nil || n == nil {
		return
	}
	roh, err := u.Msgvault.HerkunftMails(ctx)
	if err != nil {
		if !errors.Is(err, errNichtBereit) && !errors.Is(err, errGespraecheSpaeter) && ctx.Err() == nil {
			log.Printf("archiv: Umfeld: Mails für Wohnort und Arbeit aus msgvault: %v", err)
		}
		return
	}
	ls := herkunftLaden(innen)
	ks2 := n.herkunftKandidaten(roh, ks, ls)
	gelesen, grund := 0, ""
	for _, k := range ks2 {
		if grund = u.herkunftLesen(ctx, innen, n, k, ls); grund != "" {
			break
		}
		gelesen++
	}
	if gelesen > 0 {
		n.herkunftSetzen(ls, nil)
		u.geaendert()
		log.Printf("archiv: Umfeld: %d Mails auf Wohnort und Arbeit vom Modell der Box gelesen, %d warten", gelesen, len(ks2)-gelesen)
	}
	if grund != "" && ctx.Err() == nil {
		log.Printf("archiv: Umfeld: Wohnort und Arbeit warten auf eine spätere Runde: %s", grund)
	}
}

// herkunftLesen asks the lease, reads the mail, asks the model and keeps
// what it read; "" done, else why it stopped (never a text).
func (u *Umfeld) herkunftLesen(ctx context.Context, innen Innen, n *Netz, k herkunftKandidat, ls map[string]herkunftLesung) string {
	if ctx.Err() != nil {
		return "der Tresor schließt"
	}
	modell, err := u.Modell.frei(ctx)
	if err != nil {
		return err.Error()
	}
	body, err := u.Msgvault.Text(ctx, k.ID)
	if err != nil {
		return "msgvault: die Mail liest sich nicht"
	}
	l := herkunftLesung{}
	if ende := mailEnde(body); ende != "" {
		antwort, err := u.Modell.fragen(ctx, modell, herkunftSystem, herkunftFrage(n.name(k.wer), firmenDomain(k.Mail), ende))
		if err != nil {
			return err.Error()
		}
		l, _ = herkunftAntwortLesen(antwort)
	}
	l.Am, l.Wer, l.Gesendet = u.now(), k.wer, k.Am
	ls[k.ID] = l
	if err := jsonImTresor(innen.umfeldHerkunft(), ls); err != nil {
		delete(ls, k.ID)
		return "speichern: " + err.Error()
	}
	return ""
}

// herkunftVorschlag is a node's suggested value of one field: Belege the
// mails that say so, Zuletzt the newest of them.
type herkunftVorschlag struct {
	Wert    string
	Belege  int
	Zuletzt time.Time
}

// herkunftSetzen gives the net what the model read (not nil) and the
// person's answers (not nil), and makes each node's suggestions: by
// field, the readings at modellSicher or more summed by value, the one
// with the most, alone.
func (n *Netz) herkunftSetzen(ls map[string]herkunftLesung, as map[string]herkunftAntwort) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if as != nil {
		n.herkunftAntworten = maps.Clone(as)
	}
	if ls == nil {
		return
	}
	type stimme struct {
		summe float64
		v     herkunftVorschlag
	}
	je := map[string]map[string]*stimme{} // node|field → klar value → votes
	dazu := func(wer, feld, wert string, sicher float64, am time.Time) {
		if wert == "" || sicher < modellSicher {
			return
		}
		key := wer + "|" + feld
		if je[key] == nil {
			je[key] = map[string]*stimme{}
		}
		s := je[key][klar(wert)]
		if s == nil {
			s = &stimme{}
			je[key][klar(wert)] = s
		}
		s.summe += sicher
		s.v.Belege++
		if !am.Before(s.v.Zuletzt) {
			s.v.Wert, s.v.Zuletzt = wert, am
		}
	}
	for _, id := range slices.Sorted(maps.Keys(ls)) {
		l := ls[id]
		dazu(l.Wer, feldOrt, l.Ort, l.OrtSicher, l.Gesendet)
		dazu(l.Wer, feldArbeit, l.Arbeit, l.ArbSicher, l.Gesendet)
	}
	n.herkunft = map[string]herkunftVorschlag{}
	for key, werte := range je {
		var best, zweit float64
		wahl := ""
		for _, w := range slices.Sorted(maps.Keys(werte)) {
			switch s := werte[w].summe; {
			case s > best:
				best, zweit, wahl = s, best, w
			case s > zweit:
				zweit = s
			}
		}
		if wahl != "" && best != zweit {
			n.herkunft[key] = werte[wahl].v
		}
	}
}

// HerkunftZeile is a suggested or confirmed town or employer as the page shows it.
type HerkunftZeile struct {
	Wer     Knoten
	Feld    string
	Wert    string
	Belege  int
	Zuletzt time.Time
}

// HerkunftVorschlaege are the suggestions not answered, of persons shown,
// whose cards lack the field; the most mails first, then by name.
func (n *Netz) HerkunftVorschlaege() (out []HerkunftZeile) {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for key, v := range n.herkunft {
		wer, feld, _ := strings.Cut(key, "|")
		m := n.merkmale[wer]
		if a, da := n.herkunftAntworten[key]; da && klar(a.Wert) == klar(v.Wert) || !n.befragbar(wer) || wer == n.ich ||
			feld == feldOrt && len(m.strassen) > 0 || feld == feldArbeit && m.firma != "" {
			continue
		}
		out = append(out, HerkunftZeile{Wer: n.knoten[wer], Feld: feld, Wert: v.Wert, Belege: v.Belege, Zuletzt: v.Zuletzt})
	}
	slices.SortFunc(out, func(a, b HerkunftZeile) int {
		return cmp.Or(cmp.Compare(b.Belege, a.Belege), strings.Compare(a.Wer.Name, b.Wer.Name), strings.Compare(a.Feld, b.Feld))
	})
	return out[:min(len(out), herkunftZeigen)]
}

// HerkunftBestaetigt are the towns and employers the person confirmed, by name.
func (n *Netz) HerkunftBestaetigt() (out []HerkunftZeile) {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for key, a := range n.herkunftAntworten {
		wer, feld, _ := strings.Cut(key, "|")
		if k, da := n.knoten[wer]; da && a.Ja {
			out = append(out, HerkunftZeile{Wer: k, Feld: feld, Wert: a.Wert})
		}
	}
	slices.SortFunc(out, func(a, b HerkunftZeile) int {
		return cmp.Or(strings.Compare(a.Wer.Name, b.Wer.Name), strings.Compare(a.Feld, b.Feld))
	})
	return out
}

// HerkunftSagen keeps the person's answer about wer's suggested field
// (ja: it is, else it is not); vergessen drops the answer. It answers all
// of them, for the vault.
func (n *Netz) HerkunftSagen(wer, feld, wert string, ja, vergessen bool) (map[string]herkunftAntwort, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	key := wer + "|" + feld
	if vergessen {
		if _, da := n.herkunftAntworten[key]; !da {
			return nil, errFrageWeg
		}
		delete(n.herkunftAntworten, key)
		return maps.Clone(n.herkunftAntworten), nil
	}
	if v, da := n.herkunft[key]; !da || klar(v.Wert) != klar(wert) {
		return nil, errFrageWeg
	}
	n.herkunftAntworten[key] = herkunftAntwort{Wert: n.herkunft[key].Wert, Ja: ja, Am: n.now()}
	return maps.Clone(n.herkunftAntworten), nil
}

// HerkunftSagen keeps the answer in the open vault.
func (u *Umfeld) HerkunftSagen(innen Innen, wer, feld, wert string, ja, vergessen bool) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	alle, err := n.HerkunftSagen(wer, feld, wert, ja, vergessen)
	if err != nil {
		return err
	}
	return jsonImTresor(innen.umfeldHerkunftAntworten(), alle)
}

// herkunftWerkzeug is `archiv umfeld-hinweise -herkunft`: with sql the
// body msgvault's POST /api/v1/query takes; without, it reads msgvault's
// answer on stdin as a round does and prints one line per mail: whether
// it has a date and an address. No address, no text.
func herkunftWerkzeug(sql bool, ein io.Reader, aus io.Writer) int {
	if sql {
		json.NewEncoder(aus).Encode(map[string]string{"sql": herkunftSQL()})
		return 0
	}
	var r abfrageAntwort
	if err := json.NewDecoder(ein).Decode(&r); err != nil {
		fmt.Fprintln(aus, "keine Antwort von msgvault:", err)
		return 1
	}
	ms, err := mailsLesen(r)
	if err != nil {
		fmt.Fprintln(aus, err)
		return 1
	}
	for _, m := range ms {
		fmt.Fprintf(aus, "mail am=%t adresse=%t\n", !m.Am.IsZero(), strings.Contains(m.Mail, "@"))
	}
	return 0
}

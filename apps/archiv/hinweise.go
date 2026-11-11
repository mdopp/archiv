package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Relation hints from the person's messages (archiv#27, owner 2026-11-11:
// „… dann gehen Absender, Empfänger und Text zum LLM mit der Bitte den
// Grad zu bewerten. Dazu Hinweise die wir schon für die Personen haben.“).
//
// While the vault is open, each Umfeld round asks msgvault (read only,
// POST /api/v1/query) for the newest messages with a relation word of
// beziehungswoerter.yml (woerter.go) in them; msgvault's filter is wide,
// the archive's own match decides. A message whose sender and recipients
// the phone books know, and that no earlier round sent, goes to the box's
// own model (laib's model role, MODEL_URL: the household model behind
// laib-lease's proxy, ADR 0007's host.containers.internal) with who sent
// it to whom, its text (at most modellText characters) and what is
// already known of them: the phone books' relations, the person's
// answers, their shared groups and categories, their family names. The
// model answers strict JSON: the relations it reads, each with how sure
// it is. What it said is kept per message in the vault
// (umfeld/hinweise.json), so no message is sent twice.
//
// A pair's readings at modellSicher or more add up by kind; the kind
// with the most, alone, becomes the pair's suggestion on its question,
// with the cue as the reason: „Vorschlag, weil Uwe dir „Hallo Mama“
// schreibt.“ A family kind makes the pair a question even without a tie.
// Nothing is set without the person's tap.
//
// Limits (archiv#42, owner 2026-11-11: faster): while the vault is open a
// round reads every candidate, one message after the other, and asks the
// lease before each: any window standing, the proxy refusing or the model
// silent, and the rest waits for a later round; nothing piles up. Every
// modellJeSchub messages the pairs' suggestions are made anew, so the
// page shows them while the round goes on. The closing ends it (the
// round's context). A pair is read in at most modellJePaar messages. Nothing of a
// message or of an answer is logged; the text leaves the vault only in
// the request to the box's model.

const (
	modellSicher     = 0.7 // a reading counts from this sureness on
	modellJeSchub    = 10  // messages read before the suggestions are made anew
	modellJePaar     = 3   // messages read for one pair at most
	modellText       = 600 // characters of a message the model gets
	modellTreffer    = 500 // the newest messages with a word a round reads from msgvault
	modellEmpfaenger = 8   // recipients of one message the model is told about
	modellAntwort    = 400 // tokens the model may answer
)

// Modell is the box's model: MODEL_URL (…/v1), MODEL_LEASE, MODEL_KEY,
// and the household preset to ask when the lease names none.
type Modell struct {
	URL, Lease, Key, Name string
	Client                *http.Client
}

func (m *Modell) eingerichtet() bool { return m != nil && m.URL != "" }

// errModellVergeben: a lease holds the card or the proxy refuses (409,
// 429, 503); the round stops and a later one tries again.
var errModellVergeben = errors.New("das Modell der Box ist gerade vergeben")

func (m *Modell) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

// frei asks the lease whether the household model answers now, and its
// name; errModellVergeben while any window stands.
func (m *Modell) frei(ctx context.Context) (string, error) {
	if m.Lease == "" {
		return m.Name, nil
	}
	q, err := http.NewRequestWithContext(ctx, http.MethodGet, m.Lease, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.client().Do(q)
	if err != nil {
		return "", errors.New("die Lease des Modells antwortet nicht")
	}
	defer resp.Body.Close()
	var l struct {
		State string `json:"state"`
		Alias string `json:"alias"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&l) != nil {
		return "", fmt.Errorf("die Lease des Modells antwortet %d", resp.StatusCode)
	}
	if l.State != "" && l.State != "none" {
		return "", errModellVergeben
	}
	return cmp.Or(l.Alias, m.Name), nil
}

// fragen sends one chat to the model and answers its text. An error never
// carries the request or the answer.
func (m *Modell) fragen(ctx context.Context, modell, system, nutzer string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":           modell,
		"messages":        []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": nutzer}},
		"temperature":     0,
		"max_tokens":      modellAntwort,
		"stream":          false,
		"response_format": map[string]string{"type": "json_object"},
	})
	q, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(m.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("X-Laib-Background", "archiv") // the archive's own work, no person asking (laib-lease #550)
	if m.Key != "" {
		q.Header.Set("Authorization", "Bearer "+m.Key)
	}
	resp, err := m.client().Do(q)
	if err != nil {
		return "", errors.New("das Modell antwortet nicht")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusConflict, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return "", errModellVergeben
	default:
		return "", fmt.Errorf("das Modell antwortet %d", resp.StatusCode)
	}
	var a struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&a); err != nil || len(a.Choices) == 0 {
		return "", errors.New("die Antwort des Modells liest sich nicht")
	}
	return a.Choices[0].Message.Content, nil
}

// modellHinweis is one relation the model read: X is Art of Y, Sicher how sure.
type modellHinweis struct {
	X      string  `json:"x"`
	Y      string  `json:"y"`
	Art    string  `json:"art"`
	Sicher float64 `json:"s"`
}

// gelesen is a message the model read, kept in the vault by msgvault's
// id: when, its sender's and recipients' nodes, the cue (a few words
// around the relation word) and what the model read.
type gelesen struct {
	Am       time.Time       `json:"am"`
	Von      string          `json:"von"`
	An       []string        `json:"an"`
	Zitat    string          `json:"z"`
	Hinweise []modellHinweis `json:"h,omitempty"`
}

// paarHinweis is a pair's suggestion from the model: X is Art of the
// other, Weil the cue.
type paarHinweis struct {
	X, Art, Weil string
	Sicher       float64
}

func (i Innen) umfeldHinweise() string { return filepath.Join(i.Umfeld(), "hinweise.json") }

func hinweiseLaden(innen Innen) map[string]gelesen {
	out := map[string]gelesen{}
	if b, err := os.ReadFile(innen.umfeldHinweise()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// hinweiseSetzen takes what the model read and makes each pair's
// suggestion: readings at modellSicher or more, summed by kind (seen from
// the pair's smaller id); the kind with the most, alone, and the cue of
// its surest reading.
func (n *Netz) hinweiseSetzen(gs map[string]gelesen) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.gelesen = maps.Clone(gs)
	type stimme struct {
		summe float64
		best  float64
		weil  string
	}
	je := map[string]map[string]*stimme{}
	for _, id := range slices.Sorted(maps.Keys(gs)) {
		g := gs[id]
		for _, h := range g.Hinweise {
			if h.Sicher < modellSicher || h.X == h.Y || !slices.Contains(arten, h.Art) {
				continue
			}
			key, art := paarKey(h.X, h.Y), h.Art
			if h.X != min(h.X, h.Y) {
				art = cmp.Or(gegen[art], art)
			}
			if je[key] == nil {
				je[key] = map[string]*stimme{}
			}
			s := je[key][art]
			if s == nil {
				s = &stimme{}
				je[key][art] = s
			}
			s.summe += h.Sicher
			if h.Sicher > s.best {
				s.best, s.weil = h.Sicher, n.schreibt(g, h.X, h.Y)
			}
		}
	}
	n.hinweise = map[string]paarHinweis{}
	for key, arten := range je {
		var best, zweit float64
		wahl := ""
		for _, art := range slices.Sorted(maps.Keys(arten)) {
			switch s := arten[art].summe; {
			case s > best:
				best, zweit, wahl = s, best, art
			case s > zweit:
				zweit = s
			}
		}
		if wahl == "" || best == zweit {
			continue
		}
		x, _, _ := strings.Cut(key, "|")
		n.hinweise[key] = paarHinweis{X: x, Art: wahl, Weil: arten[wahl].weil, Sicher: arten[wahl].best}
	}
}

// schreibt is the cue of g as the reason for the pair a, b, verb last
// („Vorschlag, weil …“): „Uwe Dopp dir „Hallo Mama“ schreibt“, „du Uwe
// Dopp „Hallo Mama“ schreibst“. n.mu is held.
func (n *Netz) schreibt(g gelesen, a, b string) string {
	name := func(id string) string { return cmp.Or(n.knoten[id].Name, "jemand") }
	z := "„" + g.Zitat + "“"
	anderer := ""
	switch g.Von {
	case a:
		anderer = b
	case b:
		anderer = a
	default:
		return name(g.Von) + " an beide " + z + " schreibt"
	}
	switch {
	case g.Von == n.ich:
		return "du " + name(anderer) + " " + z + " schreibst"
	case anderer == n.ich:
		return name(g.Von) + " dir " + z + " schreibt"
	}
	return name(g.Von) + " " + name(anderer) + " " + z + " schreibt"
}

// hinweisPaare are the pairs a name's word (toward the person) or the
// model points to family, each once; n.mu is held.
func (n *Netz) hinweisPaare() (out [][2]string) {
	if n.ich != "" {
		for _, id := range slices.Sorted(maps.Keys(n.knoten)) {
			if wort, _ := n.namensWort(id); id != n.ich && wort != "" {
				out = append(out, [2]string{id, n.ich})
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(n.hinweise)) {
		if slices.Contains(familienArten, n.hinweise[key].Art) {
			a, b, _ := strings.Cut(key, "|")
			out = append(out, [2]string{a, b})
		}
	}
	return out
}

// rohNachricht is a message with a relation word as msgvault answers it:
// its id, chat or mail, when, whether the person sent it, its text, its
// sender and its recipients.
type rohNachricht struct {
	ID     string
	Art    string
	Am     time.Time
	VonMir bool
	Text   string
	Von    []gespraechsTeilnehmer
	An     []gespraechsTeilnehmer
}

// hinweiseSQL is the one read-only statement: the newest modellTreffer
// messages (no list, not deleted) whose text has a relation word, one row
// per message and participant (rolle von or an); a chat's recipients are
// its members but the sender, a mail's its To and Cc.
func hinweiseSQL(muster string) string {
	return fmt.Sprintf(`WITH m AS (
  SELECT id, conversation_id, sent_at, sender_id, is_from_me, snippet
  FROM messages
  WHERE deleted_at IS NULL AND COALESCE(list_id, '') = '' AND snippet IS NOT NULL
    AND regexp_matches(lower(snippet), '%[1]s')
  ORDER BY sent_at DESC, id
  LIMIT %[2]d
), c AS (
  SELECT id, CASE WHEN conversation_type = 'email_thread' THEN '%[3]s' ELSE '%[4]s' END AS art FROM conversations
), an AS (
  SELECT m.id AS nachricht, r.participant_id AS p
  FROM m JOIN message_recipients r ON r.message_id = m.id
  WHERE r.recipient_type IN ('to', 'cc') AND r.participant_id IS NOT NULL
  UNION
  SELECT m.id, cp.participant_id
  FROM m JOIN c ON c.id = m.conversation_id AND c.art = '%[4]s' JOIN conversation_participants cp ON cp.conversation_id = m.conversation_id
  WHERE cp.participant_id IS DISTINCT FROM m.sender_id
), wer AS (
  SELECT id AS nachricht, sender_id AS p, 'von' AS rolle FROM m WHERE sender_id IS NOT NULL
  UNION ALL
  SELECT nachricht, p, 'an' FROM an
)
SELECT CAST(m.id AS VARCHAR) AS nachricht, COALESCE(c.art, '%[3]s') AS art,
  strftime(m.sent_at, '%%Y-%%m-%%dT%%H:%%M:%%SZ') AS am, m.is_from_me AS von_mir, m.snippet AS text,
  COALESCE(wer.rolle, '') AS rolle, COALESCE(p.phone_number, '') AS telefon, COALESCE(p.email_address, '') AS mail
FROM m LEFT JOIN c ON c.id = m.conversation_id LEFT JOIN wer ON wer.nachricht = m.id LEFT JOIN participants p ON p.id = wer.p
ORDER BY m.sent_at DESC, m.id, rolle DESC`, strings.ReplaceAll(muster, "'", "''"), modellTreffer, gespraechMail, gespraechChat)
}

// Schlagwortnachrichten reads the messages with a relation word through msgvault's API.
func (m *MsgvaultAPI) Schlagwortnachrichten(ctx context.Context, muster string) ([]rohNachricht, error) {
	r, err := m.abfrage(ctx, hinweiseSQL(muster))
	if err != nil {
		return nil, err
	}
	return nachrichtenLesen(r)
}

// nachrichtenLesen turns the rows into messages, by column name.
func nachrichtenLesen(r abfrageAntwort) ([]rohNachricht, error) {
	spalte := map[string]int{}
	for i, c := range r.Columns {
		spalte[c] = i
	}
	for _, c := range []string{"nachricht", "art", "am", "von_mir", "text", "rolle", "telefon", "mail"} {
		if _, da := spalte[c]; !da {
			return nil, fmt.Errorf("msgvault-Abfrage ohne Spalte %q", c)
		}
	}
	text := func(z []any, c string) string {
		if i := spalte[c]; i < len(z) && z[i] != nil {
			return fmt.Sprint(z[i])
		}
		return ""
	}
	var out []rohNachricht
	wo := map[string]int{}
	for _, z := range r.Rows {
		if len(z) < len(r.Columns) {
			continue
		}
		id := text(z, "nachricht")
		i, da := wo[id]
		if !da {
			t, _ := time.Parse(time.RFC3339, text(z, "am"))
			mir, _ := z[spalte["von_mir"]].(bool)
			out = append(out, rohNachricht{ID: id, Art: text(z, "art"), Am: t, VonMir: mir, Text: text(z, "text")})
			i = len(out) - 1
			wo[id] = i
		}
		t := gespraechsTeilnehmer{Telefon: strings.TrimSpace(text(z, "telefon")), Mail: strings.TrimSpace(text(z, "mail"))}
		if t.Telefon == "" && t.Mail == "" {
			continue
		}
		switch text(z, "rolle") {
		case "von":
			out[i].Von = append(out[i].Von, t)
		case "an":
			out[i].An = append(out[i].An, t)
		}
	}
	return out, nil
}

// knotenVon is the shown node of the participant's cards; "" none. n.mu is free.
func (n *Netz) knotenVon(t gespraechsTeilnehmer, ks []buchKarte) string {
	if massenAdresse(t.Mail) {
		return ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, key := range teilnehmerKarten(t, ks) {
		if id := n.von[key]; n.selbst[id] {
			return n.ich
		} else if id != "" && n.sichtbar(id) {
			return id
		}
	}
	return ""
}

// kandidat is a message the round may send: msgvault's id, its sender's
// and recipients' nodes, its relation words and its text.
type kandidat struct {
	id      string
	am      time.Time
	von     string
	an      []string
	treffer []Worttreffer
	text    string
}

// kandidaten are the messages of roh worth a question, newest first: not
// read before, a relation word in them (imText), a known sender and at
// least one known recipient (in a chat the person is one, if she did not
// send it), and a pair not yet read modellJePaar times. At most hoechstens (0: all).
func (n *Netz) kandidaten(roh []rohNachricht, ks []buchKarte, gs map[string]gelesen, hoechstens int) (out []kandidat) {
	ich := n.Ich()
	je := map[string]int{}
	zaehlen := func(von string, an []string) {
		for _, a := range an {
			je[paarKey(von, a)]++
		}
	}
	for _, g := range gs {
		zaehlen(g.Von, g.An)
	}
	for _, r := range roh {
		if _, da := gs[r.ID]; da {
			continue
		}
		treffer := woerter.imText(r.Text)
		if len(treffer) == 0 {
			continue
		}
		von := ""
		if r.VonMir {
			von = ich
		} else if len(r.Von) > 0 {
			von = n.knotenVon(r.Von[0], ks)
		}
		var an []string
		for _, t := range r.An {
			if id := n.knotenVon(t, ks); id != "" && id != von && !slices.Contains(an, id) {
				an = append(an, id)
			}
		}
		if r.Art == gespraechChat && !r.VonMir && ich != "" && ich != von && !slices.Contains(an, ich) {
			an = append(an, ich)
		}
		an = an[:min(len(an), modellEmpfaenger)]
		if von == "" || len(an) == 0 || !slices.ContainsFunc(an, func(a string) bool { return je[paarKey(von, a)] < modellJePaar }) {
			continue
		}
		out = append(out, kandidat{id: r.ID, am: r.Am, von: von, an: an, treffer: treffer, text: r.Text})
		zaehlen(von, an)
		if len(out) == hoechstens {
			break
		}
	}
	return out
}

// modellArten are the kinds the model may answer, as the prompt names them.
func modellArten() string {
	var out []string
	for _, a := range arten {
		out = append(out, a+" ("+kurzWort(a)+")")
	}
	return strings.Join(out, ", ")
}

// hinweisSystem is the model's instruction: the same for every message.
func hinweisSystem() string {
	return `Du hilfst einer Person, ihr Adressbuch zu ergänzen: wie stehen Menschen zueinander? ` +
		`Du bekommst eine einzelne Nachricht aus ihrem Archiv, wer sie wem geschrieben hat, und was über diese Menschen schon bekannt ist. ` +
		`Bewerte, welche Beziehung die Nachricht zwischen den genannten Personen nahelegt. ` +
		`Antworte nur mit JSON, genau in dieser Form: {"beziehungen":[{"x":"A","y":"B","art":"eltern","sicher":0.9}]}. ` +
		`x und y sind die Buchstaben der Personen; „art“ heißt: x ist <art> von y. ` +
		`Erlaubte Arten: ` + modellArten() + `. ` +
		`„sicher“ ist eine Zahl von 0 bis 1. Nimm nur auf, was die Nachricht selbst nahelegt; ` +
		`ein Kosewort ist nicht immer eine Partnerschaft (Eltern nennen ihr Kind auch „Schatz“), ` +
		`und wer über eine dritte Person schreibt („meine Mutter“), sagt nichts über den Empfänger. ` +
		`Legt die Nachricht nichts nahe: {"beziehungen":[]}.`
}

// hinweisFrage is the model's question about k: the persons as letters
// (A the sender), what is known of them, the relation words and the text.
// It answers the letters' nodes. n.mu is free.
func (n *Netz) hinweisFrage(k kandidat) (string, map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := append([]string{k.von}, k.an...)
	buchstaben := map[string]string{}
	var b strings.Builder
	b.WriteString("Personen:\n")
	for i, id := range ids {
		l := string(rune('A' + i))
		buchstaben[l] = id
		rolle := "bekommt die Nachricht"
		if i == 0 {
			rolle = "schreibt die Nachricht"
		}
		if id == n.ich {
			rolle += "; ihr gehört dieses Archiv"
		}
		fmt.Fprintf(&b, "%s: %s (%s)\n", l, cmp.Or(n.knoten[id].Name, "unbekannt"), rolle)
	}
	if bekannt := n.bekanntUeber(ids); len(bekannt) > 0 {
		b.WriteString("\nSchon bekannt:\n")
		for _, s := range bekannt {
			b.WriteString("- " + s + "\n")
		}
	}
	var worte []string
	for _, t := range k.treffer {
		worte = append(worte, "„"+t.Wort+"“")
	}
	text := []rune(strings.TrimSpace(k.text))
	if len(text) > modellText {
		text = append(text[:modellText], '…')
	}
	fmt.Fprintf(&b, "\nBeziehungswörter darin: %s\n\nNachricht von A", strings.Join(worte, ", "))
	if !k.am.IsZero() {
		fmt.Fprintf(&b, " vom %s", k.am.Format("02.01.2006"))
	}
	fmt.Fprintf(&b, ":\n«%s»\n", string(text))
	return b.String(), buchstaben
}

// bekanntUeber is what the net knows of ids, as lines for the model: the
// person's answers and the phone books' relations naming one of them,
// their shared groups with a category, their family names. n.mu is held.
func (n *Netz) bekanntUeber(ids []string) (out []string) {
	in := func(id string) bool { return slices.Contains(ids, id) }
	name := func(id string) string { return n.knoten[id].Name }
	for _, key := range slices.Sorted(maps.Keys(n.antworten)) {
		a := n.antworten[key]
		x, y, _ := strings.Cut(key, "|")
		if a.X == y {
			x, y = y, x
		}
		if (in(x) || in(y)) && slices.Contains(arten, a.Art) && name(x) != "" && name(y) != "" {
			out = append(out, fmt.Sprintf("%s ist %s von %s (von der Person angegeben)", name(x), kurzWort(a.Art), name(y)))
		}
	}
	for _, f := range n.st.Fest {
		a, b := n.von[f.A], n.von[f.B]
		if f.Art == bandBeziehung && f.Wie != "" && a != "" && b != "" && (in(a) || in(b)) {
			out = append(out, fmt.Sprintf("%s ist %s von %s (Telefonbuch)", name(b), f.Wie, name(a)))
		}
	}
	for i, a := range ids {
		for _, b := range ids[i+1:] {
			for _, g := range n.paarGruppen[paarKey(a, b)] {
				s := fmt.Sprintf("%s und %s sind beide in der Gruppe „%s“", name(a), name(b), n.gespraech[g].Titel)
				if kat, _ := n.kategorieVon(g); kat != "" {
					s += " (" + kategorieName(kat, n.gruppen[g].Name) + ")"
				}
				out = append(out, s)
			}
		}
	}
	for _, id := range ids {
		for _, k := range n.st.Kontakte {
			if cmp.Or(k.Person, k.Key) == id && k.Family != "" {
				out = append(out, fmt.Sprintf("Nachname von %s: %s", name(id), k.Family))
				break
			}
		}
	}
	out = slices.Compact(out)
	return out[:min(len(out), 15)]
}

// hinweisAntwort reads the model's answer strictly: one JSON object with
// „beziehungen“, nothing after it; each relation with two different known
// letters, a known kind and a sureness from 0 to 1, the rest left out.
// A fence (```json) around it is taken off. Unreadable: none.
func hinweisAntwort(s string, buchstaben map[string]string) (out []modellHinweis) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "```"); ok {
		rest = strings.TrimPrefix(rest, "json")
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "```"))
	}
	var a struct {
		Beziehungen []struct {
			X      string   `json:"x"`
			Y      string   `json:"y"`
			Art    string   `json:"art"`
			Sicher *float64 `json:"sicher"`
		} `json:"beziehungen"`
	}
	d := json.NewDecoder(strings.NewReader(s))
	if err := d.Decode(&a); err != nil || a.Beziehungen == nil && !strings.Contains(s, `"beziehungen"`) {
		return nil
	}
	if _, err := d.Token(); err != io.EOF {
		return nil
	}
	for _, r := range a.Beziehungen {
		x, y := buchstaben[strings.ToUpper(strings.TrimSpace(r.X))], buchstaben[strings.ToUpper(strings.TrimSpace(r.Y))]
		art := strings.ToLower(strings.TrimSpace(r.Art))
		if x == "" || y == "" || x == y || !slices.Contains(arten, art) || r.Sicher == nil || *r.Sicher < 0 || *r.Sicher > 1 {
			continue
		}
		out = append(out, modellHinweis{X: x, Y: y, Art: art, Sicher: *r.Sicher})
	}
	return out
}

// hinweiseRunde sends the candidates to the model, one after the other,
// the lease asked before each; each answer is kept in the vault at once,
// and every modellJeSchub the net gets the pairs' suggestions. The model
// taken or silent, or the vault closing: it stops, the rest waits. It
// logs counts, never a text or an answer.
func (u *Umfeld) hinweiseRunde(ctx context.Context, innen Innen, n *Netz, ks []buchKarte) {
	if !u.Modell.eingerichtet() || u.Msgvault == nil || n == nil {
		return
	}
	roh, err := u.Msgvault.Schlagwortnachrichten(ctx, woerter.muster())
	if err != nil {
		if !errors.Is(err, errNichtBereit) && !errors.Is(err, errGespraecheSpaeter) && ctx.Err() == nil {
			log.Printf("archiv: Umfeld: Nachrichten mit Beziehungswörtern aus msgvault: %v", err)
		}
		return
	}
	gs := hinweiseLaden(innen)
	offen := len(n.kandidaten(roh, ks, gs, 0))
	u.setzen(func(s *UmfeldStand) { s.ModellGelesen, s.ModellOffen, s.ModellGezaehlt = len(gs), offen, true })
	gelesenJetzt, grund := 0, ""
	for grund == "" {
		schub := n.kandidaten(roh, ks, gs, modellJeSchub)
		if len(schub) == 0 {
			break
		}
		neu := 0
		for _, k := range schub {
			if grund = u.modellLesen(ctx, innen, n, k, gs); grund != "" {
				break
			}
			neu++
		}
		if neu > 0 {
			gelesenJetzt += neu
			n.hinweiseSetzen(gs)
			offen = max(0, offen-neu)
			u.setzen(func(s *UmfeldStand) { s.ModellGelesen, s.ModellOffen, s.ModellGezaehlt = len(gs), offen, true })
		}
	}
	if grund != "" && ctx.Err() == nil {
		log.Printf("archiv: Umfeld: Beziehungshinweise warten auf eine spätere Runde: %s", grund)
	}
	if gelesenJetzt > 0 {
		log.Printf("archiv: Umfeld: %d Nachrichten mit Beziehungswörtern vom Modell der Box gelesen, %d warten", gelesenJetzt, offen)
	}
}

// modellLesen asks the lease, then the model about k, and keeps what it
// read in gs and the vault; "" done, else why it stopped (never a text).
func (u *Umfeld) modellLesen(ctx context.Context, innen Innen, n *Netz, k kandidat, gs map[string]gelesen) string {
	if ctx.Err() != nil {
		return "der Tresor schließt"
	}
	modell, err := u.Modell.frei(ctx)
	if err != nil {
		return err.Error()
	}
	frage, buchstaben := n.hinweisFrage(k)
	antwort, err := u.Modell.fragen(ctx, modell, hinweisSystem(), frage)
	if err != nil {
		return err.Error()
	}
	gs[k.id] = gelesen{Am: u.now(), Von: k.von, An: k.an, Zitat: k.treffer[0].Zitat, Hinweise: hinweisAntwort(antwort, buchstaben)}
	if err := jsonImTresor(innen.umfeldHinweise(), gs); err != nil {
		delete(gs, k.id)
		return "speichern: " + err.Error()
	}
	return ""
}

// modellAus is the box's model from the environment laib's template sets
// (MODEL_URL, MODEL_LEASE, MODEL_KEY: laib's builtins as a pod reaches
// them; MODEL_NAME, the household preset); nil without an http(s) MODEL_URL.
func modellAus(getenv func(string) string) *Modell {
	u := strings.TrimRight(strings.TrimSpace(getenv("MODEL_URL")), "/")
	if p, err := url.Parse(u); u == "" || err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return nil
	}
	return &Modell{URL: u, Lease: strings.TrimSpace(getenv("MODEL_LEASE")), Key: strings.TrimSpace(getenv("MODEL_KEY")), Name: strings.TrimSpace(getenv("MODEL_NAME"))}
}

// hinweiseWerkzeug is `archiv umfeld-hinweise`: with -sql it prints the
// body msgvault's POST /api/v1/query takes for the messages with a
// relation word (-wort w: with that one word instead of the file's, for
// integration.sh's fixtures; -geburtstag: the birthday wishes'; -paare: the
// couples'; -herkunft: the mails for home town and employer, herkunft.go); without, it reads msgvault's answer on
// stdin as a round does and prints each message as one line: kind, sent
// by the person, how many senders and recipients, its relation words. No
// text, no number, no address.
func hinweiseWerkzeug(args []string, ein io.Reader, aus io.Writer) int {
	if slices.Contains(args, "-herkunft") {
		return herkunftWerkzeug(slices.Contains(args, "-sql"), ein, aus)
	}
	if slices.Contains(args, "-sql") {
		muster := woerter.muster()
		if slices.Contains(args, "-geburtstag") { // the birthday wishes' statement (geburtstage.go)
			muster = gebWuensche
		}
		if slices.Contains(args, "-paare") { // the couples' statement (paare.go)
			muster = woerter.paarMuster()
		}
		if i := slices.Index(args, "-wort"); i >= 0 && i+1 < len(args) && nurWort.MatchString(args[i+1]) {
			muster = `(^|\PL)(` + strings.ToLower(args[i+1]) + `)(\PL|$)`
		}
		json.NewEncoder(aus).Encode(map[string]string{"sql": hinweiseSQL(muster)})
		return 0
	}
	var r abfrageAntwort
	if err := json.NewDecoder(ein).Decode(&r); err != nil {
		fmt.Fprintln(aus, "keine Antwort von msgvault:", err)
		return 1
	}
	ns, err := nachrichtenLesen(r)
	if err != nil {
		fmt.Fprintln(aus, err)
		return 1
	}
	for _, m := range ns {
		var worte []string
		for _, t := range woerter.imText(m.Text) {
			worte = append(worte, klar(t.Wort))
		}
		fmt.Fprintf(aus, "%s %t von=%d an=%d %s\n", m.Art, m.VonMir, len(m.Von), len(m.An), strings.Join(worte, ","))
	}
	return 0
}

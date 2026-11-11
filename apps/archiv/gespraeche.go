package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Chats and mail as sources of the net (archiv#27 step 3): a group chat
// (WhatsApp, Signal, an MMS group) of at most gruppeMax members and a
// mail thread of at most fadenMax addresses are evidence that their
// members belong together, as a photo day is. Mailing lists and bulk
// senders are left out. msgvault knows who took part; the archive reads
// it through msgvault's SQL API (POST /api/v1/query, read only, the
// analytics views of msgvault 0.22.0) while the vault is open.
//
// A participant becomes a person only through the phone books: their
// number (last nine digits) or their address (case aside) on a contact's
// card. One that matches no card, or cards of differently named contacts
// (a shared landline), is left out; nobody is invented. The table keeps
// only the matched contacts' keys, the size, when, how many messages and
// a chat's title (for the group suggestions of step 5), never a number
// or an address.
//
// The weights, in the photos' spirit (netz.go): each conversation counts
// for each pair in it gespraechGewicht[art]/(size-1), halved every
// netzHalbwert since its last message. A group chat weighs as two photo
// days of the same size: a group is a circle that stays. A mail thread
// weighs a quarter of that: mail is cheap, and threads with the same
// people come by the dozen. What chats and mail add to a pair is capped
// at gespraechDeckel, below a phone book's relation (3), so a busy inbox
// cannot outrank the family. A tie from chats and mail alone needs two
// conversations that weigh belegMin together (Kante.belegt): two chats of
// nine or two threads of three, never two class chats of thirty. A mail
// thread only one of its people wrote in is no conversation (Sender): an
// invoice, a circular. A chat of two (gespraechDirekt) is the person's
// own conversation with one other: a tie from direktMin messages on when
// both wrote, and the best sign of who is close (naehe.go).

const (
	gespraechChat   = "chat"
	gespraechMail   = "mail"
	gespraechDirekt = "direkt"
	gruppeMin       = 3  // a chat of two is no group
	gruppeMax       = 30 // a bigger chat is a club, a class, a street
	fadenMin        = 2
	fadenMax        = 10 // a mail to more is a circular
	gespraechDeckel = 2.0
)

var gespraechGewicht = map[string]float64{gespraechChat: 2, gespraechMail: 0.5}

// netzGespraech is a chat or a mail thread of the table: msgvault's
// conversation id, chat or mail, a chat's title, how many took part (all,
// the matched and the others), how many messages, the last one's time,
// and the contacts' keys of those who took part and are in a phone book.
type netzGespraech struct {
	ID          string    `json:"i"`
	Art         string    `json:"a"`
	Titel       string    `json:"t,omitempty"`
	Groesse     int       `json:"g"`
	Nachrichten int       `json:"n"`
	Zuletzt     time.Time `json:"z"`
	Wer         []string  `json:"w"`
	// Sender: how many of its people wrote in it; 0 a table read before
	// the archive asked (counted as before)
	Sender int `json:"s,omitempty"`
}

// zaehlt: the conversation is within its kind's size.
func (g netzGespraech) zaehlt() bool {
	switch g.Art {
	case gespraechChat:
		return g.Groesse >= gruppeMin && g.Groesse <= gruppeMax
	case gespraechMail:
		return g.Groesse >= fadenMin && g.Groesse <= fadenMax && g.Sender != 1
	case gespraechDirekt:
		return g.Groesse == direktGroesse && g.Sender >= 2
	}
	return false
}

// gespraechsTeilnehmer is one who took part, as msgvault knows them.
type gespraechsTeilnehmer struct {
	ID            string
	Telefon, Mail string
}

// rohGespraech is a conversation as msgvault answers it, before matching.
type rohGespraech struct {
	ID          string
	Art         string
	Titel       string
	Groesse     int
	Nachrichten int
	Zuletzt     time.Time
	Sender      int  // distinct senders among its messages
	Liste       bool // a message of it came through a mailing list (List-Id)
	Wer         []gespraechsTeilnehmer
}

// gespraecheSQL is the one read-only statement: the chats (of
// direktGroesse..gruppeMax members: the person's chats with one other and
// the groups; msgvault calls an MMS group a direct_chat) and the mail threads (fadenMin..fadenMax addresses among
// From, To and Cc), one row per conversation and participant. Size is
// cut here too, so a big inbox does not send every newsletter's rows.
func gespraecheSQL() string {
	return fmt.Sprintf(`WITH m AS (
  SELECT id, conversation_id, sent_at, sender_id, COALESCE(list_id, '') <> '' AS liste
  FROM messages WHERE deleted_at IS NULL AND conversation_id IS NOT NULL
), c AS (
  SELECT id, COALESCE(title, '') AS titel,
    CASE WHEN conversation_type = 'email_thread' THEN '%[1]s' ELSE '%[2]s' END AS art
  FROM conversations WHERE conversation_type IN ('group_chat', 'direct_chat', 'email_thread')
), wer AS (
  SELECT cp.conversation_id AS g, cp.participant_id AS p
  FROM conversation_participants cp JOIN c ON c.id = cp.conversation_id AND c.art = '%[2]s'
  UNION
  SELECT m.conversation_id, r.participant_id
  FROM m JOIN message_recipients r ON r.message_id = m.id JOIN c ON c.id = m.conversation_id AND c.art = '%[1]s'
  WHERE r.recipient_type IN ('from', 'to', 'cc') AND r.participant_id IS NOT NULL
  UNION
  SELECT m.conversation_id, m.sender_id FROM m JOIN c ON c.id = m.conversation_id WHERE m.sender_id IS NOT NULL
), groesse AS (
  SELECT g, count(*) AS k FROM wer GROUP BY g
), nach AS (
  SELECT conversation_id AS g, count(*) AS n, max(sent_at) AS zuletzt, bool_or(liste) AS liste, count(DISTINCT sender_id) AS sender FROM m GROUP BY conversation_id
)
SELECT CAST(c.id AS VARCHAR) AS gespraech, c.art, c.titel, groesse.k AS groesse, nach.n AS nachrichten,
  strftime(nach.zuletzt, '%%Y-%%m-%%dT%%H:%%M:%%SZ') AS zuletzt, nach.liste, nach.sender,
  CAST(p.id AS VARCHAR) AS teilnehmer, COALESCE(p.phone_number, '') AS telefon, COALESCE(p.email_address, '') AS mail
FROM c JOIN nach ON nach.g = c.id JOIN groesse ON groesse.g = c.id JOIN wer ON wer.g = c.id JOIN participants p ON p.id = wer.p
WHERE (c.art = '%[2]s' AND groesse.k BETWEEN %[3]d AND %[4]d) OR (c.art = '%[1]s' AND groesse.k BETWEEN %[5]d AND %[6]d)
ORDER BY c.id, p.id`, gespraechMail, gespraechChat, direktGroesse, gruppeMax, fadenMin, fadenMax)
}

// errGespraecheSpaeter: msgvault is still building its analytics (202)
// or has none yet (503); the round keeps what it read last.
var errGespraecheSpaeter = errors.New("msgvault rechnet seine Auswertung noch")

// abfrageAntwort is msgvault's QueryResult.
type abfrageAntwort struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// abfrage runs one read-only statement through msgvault's POST
// /api/v1/query; errGespraecheSpaeter while the analytics are not
// ready (503, or 202: a build was started, nothing to read yet).
func (m *MsgvaultAPI) abfrage(ctx context.Context, sql string) (abfrageAntwort, error) {
	var r abfrageAntwort
	err := m.rufe(ctx, http.MethodPost, "/api/v1/query", nil, map[string]string{"sql": sql}, &r)
	var f *apiFehler
	switch {
	case errors.As(err, &f) && f.Status == http.StatusServiceUnavailable:
		return r, errGespraecheSpaeter
	case err != nil:
		return r, err
	case r.Columns == nil:
		return r, errGespraecheSpaeter
	}
	return r, nil
}

// Gespraeche reads the group chats and mail threads through msgvault's API.
func (m *MsgvaultAPI) Gespraeche(ctx context.Context) ([]rohGespraech, error) {
	r, err := m.abfrage(ctx, gespraecheSQL())
	if err != nil {
		return nil, err
	}
	return gespraecheLesen(r)
}

// gespraecheLesen turns the rows into conversations, by column name.
func gespraecheLesen(r abfrageAntwort) ([]rohGespraech, error) {
	spalte := map[string]int{}
	for i, c := range r.Columns {
		spalte[c] = i
	}
	for _, c := range []string{"gespraech", "art", "titel", "groesse", "nachrichten", "zuletzt", "liste", "sender", "teilnehmer", "telefon", "mail"} {
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
	zahl := func(z []any, c string) int {
		switch v := z[spalte[c]].(type) {
		case float64:
			return int(v)
		case string:
			n, _ := strconv.Atoi(v)
			return n
		}
		return 0
	}
	var out []rohGespraech
	for _, z := range r.Rows {
		if len(z) < len(r.Columns) {
			continue
		}
		id := text(z, "gespraech")
		if len(out) == 0 || out[len(out)-1].ID != id {
			t, _ := time.Parse(time.RFC3339, text(z, "zuletzt"))
			liste, _ := z[spalte["liste"]].(bool)
			out = append(out, rohGespraech{ID: id, Art: text(z, "art"), Titel: strings.TrimSpace(text(z, "titel")), Groesse: zahl(z, "groesse"),
				Nachrichten: zahl(z, "nachrichten"), Zuletzt: t, Sender: zahl(z, "sender"), Liste: liste})
		}
		g := &out[len(out)-1]
		g.Wer = append(g.Wer, gespraechsTeilnehmer{ID: text(z, "teilnehmer"), Telefon: strings.TrimSpace(text(z, "telefon")), Mail: strings.TrimSpace(text(z, "mail"))})
	}
	return out, nil
}

// massenAbsender are the local parts of addresses that send to many and
// read nothing: a thread with one of them is no circle of people.
var massenAbsender = []string{"noreply", "no-reply", "no_reply", "donotreply", "do-not-reply", "mailer-daemon", "postmaster", "bounce",
	"newsletter", "news", "notification", "notifications", "notify", "info", "service", "support", "marketing", "mailer", "automated"}

// massenAdresse: the address is a bulk sender's.
func massenAdresse(mail string) bool {
	lokal, _, ok := strings.Cut(strings.ToLower(mail), "@")
	if !ok {
		return false
	}
	return slices.ContainsFunc(massenAbsender, func(m string) bool {
		return lokal == m || strings.HasPrefix(lokal, m+"-") || strings.HasPrefix(lokal, m+".") || strings.HasPrefix(lokal, m+"+")
	})
}

// gespraecheZuordnen keeps the conversations that count (size; no list,
// no bulk sender) with their participants matched to the phone books'
// contacts; a participant matching no card, or cards of differently
// named contacts, is left out.
func gespraecheZuordnen(roh []rohGespraech, ks []buchKarte) []netzGespraech {
	var out []netzGespraech
	for _, r := range roh {
		g := netzGespraech{ID: r.ID, Art: r.Art, Groesse: r.Groesse, Nachrichten: r.Nachrichten, Zuletzt: r.Zuletzt, Sender: r.Sender}
		switch {
		case r.Art == gespraechChat && r.Groesse == direktGroesse:
			g.Art = gespraechDirekt
		case r.Art == gespraechChat:
			g.Titel = r.Titel
		}
		if !g.zaehlt() || r.Liste || slices.ContainsFunc(r.Wer, func(t gespraechsTeilnehmer) bool { return massenAdresse(t.Mail) }) {
			continue
		}
		erkannt := 0
		for _, t := range r.Wer {
			keys := teilnehmerKarten(t, ks)
			if len(keys) > 0 {
				erkannt++
			}
			for _, key := range keys {
				if !slices.Contains(g.Wer, key) {
					g.Wer = append(g.Wer, key)
				}
			}
		}
		if erkannt >= 2 { // one person's two cards are no pair
			out = append(out, g)
		}
	}
	return out
}

// teilnehmerKarten are the keys of the cards with the participant's
// number or address; none when they belong to differently named contacts.
func teilnehmerKarten(t gespraechsTeilnehmer, ks []buchKarte) (keys []string) {
	name := ""
	for _, k := range ks {
		passt := t.Mail != "" && slices.ContainsFunc(k.emails, func(m string) bool { return strings.EqualFold(strings.TrimSpace(m), t.Mail) }) ||
			t.Telefon != "" && slices.ContainsFunc(k.tels, func(n string) bool { return gleicheEndziffern(n, t.Telefon) })
		if !passt {
			continue
		}
		if name != "" && klar(k.name) != name {
			return nil
		}
		name = klar(k.name)
		keys = append(keys, k.key)
	}
	return keys
}

// gespraeche reads the chats and threads into tab.Gespraeche, matched to
// the cards ks; msgvault closed, still building or out of reach: the
// ones read last stay.
func (u *Umfeld) gespraeche(ctx context.Context, tab *netzTabelle, ks []buchKarte) {
	if u.Msgvault == nil {
		return
	}
	roh, err := u.Msgvault.Gespraeche(ctx)
	if err != nil {
		if !errors.Is(err, errNichtBereit) && !errors.Is(err, errGespraecheSpaeter) && ctx.Err() == nil {
			log.Printf("archiv: Umfeld: Chats und Mails aus msgvault: %v", err)
		}
		return
	}
	tab.Gespraeche = gespraecheZuordnen(roh, ks)
}

// gespraechSumme is what a pair's chats and threads add up to.
type gespraechSumme struct {
	s, roh, zuZweit      float64
	chats, faeden, mails int
	direkt               int
	kontakt              time.Time
}

// dazu adds the conversation g to a pair's sum; true for a group chat.
func (s *gespraechSumme) dazu(g netzGespraech, jetzt time.Time) bool {
	verfall := math.Pow(0.5, max(0, jetzt.Sub(g.Zuletzt).Hours())/netzHalbwert.Hours())
	if g.Groesse <= kontaktGroesse && g.Zuletzt.After(s.kontakt) {
		s.kontakt = g.Zuletzt
	}
	if g.Art == gespraechDirekt {
		s.direkt += g.Nachrichten
		s.zuZweit += direktGewicht(g.Nachrichten) * verfall
		return false
	}
	roh := gespraechGewicht[g.Art] / float64(max(1, g.Groesse-1))
	s.s, s.roh = s.s+roh*verfall, s.roh+roh
	if g.Art == gespraechChat {
		s.chats++
		return true
	}
	s.faeden++
	s.mails += g.Nachrichten
	return false
}

// gespraecheDazu adds the chats and threads to alle, between the
// contacts' nodes, those in nicht left out: to a tie there is, always; a
// new tie from gespraecheMin conversations on.
func (n *Netz) gespraecheDazu(alle map[[2]string]*Kante, nicht map[string]bool) {
	summen := map[[2]string]*gespraechSumme{}
	n.paarGruppen = map[string][]string{}
	for _, g := range n.st.Gespraeche {
		if !g.zaehlt() {
			continue
		}
		var wer []string
		for _, key := range g.Wer {
			id := n.von[key]
			if n.selbst[id] { // any card of hers is the person
				id = n.ich
			}
			if id != "" && !nicht[id] && !slices.Contains(wer, id) {
				wer = append(wer, id)
			}
		}
		slices.Sort(wer)
		for i, a := range wer {
			for _, b := range wer[i+1:] {
				s := summen[[2]string{a, b}]
				if s == nil {
					s = &gespraechSumme{}
					summen[[2]string{a, b}] = s
				}
				if s.dazu(g, n.now()) {
					n.paarGruppen[paarKey(a, b)] = append(n.paarGruppen[paarKey(a, b)], g.ID)
				}
			}
		}
	}
	for key, s := range summen {
		k := alle[key] // one without the evidence of a tie leaves again (bauen, belegt)
		if k == nil {
			k = &Kante{A: key[0], B: key[1]}
			alle[key] = k
		}
		add := min(s.s, gespraechDeckel) + min(s.zuZweit, direktDeckel)
		k.Staerke, k.Rang, k.Beleg, k.Direkt = k.Staerke+add, k.Rang+add, k.Beleg+s.roh, s.direkt
		if s.kontakt.After(k.Kontakt) {
			k.Kontakt = s.kontakt
		}
		k.Chats, k.Mails, k.Faeden, k.Gruppen = s.chats, s.mails, s.faeden, n.paarGruppen[paarKey(key[0], key[1])]
	}
}

// gespraechGruende are the tie's chats and mails as reasons: „in 3
// Gruppen-Chats“, „12 Mails zusammen“.
func gespraechGruende(k Kante) (out []string) {
	if k.Direkt > 0 {
		out = append(out, anzahlT(k.Direkt, "Nachricht", "Nachrichten")+" zu zweit")
	}
	switch {
	case k.Chats == 1:
		out = append(out, "in einem Gruppen-Chat")
	case k.Chats > 1:
		out = append(out, fmt.Sprintf("in %s Gruppen-Chats", tausender(k.Chats)))
	}
	if k.Mails > 0 {
		out = append(out, anzahlT(k.Mails, "Mail", "Mails")+" zusammen")
	}
	return out
}

// gespraecheWerkzeug is `archiv umfeld-gespraeche`: with -sql it prints
// the body msgvault's POST /api/v1/query takes for the chats and threads;
// without, it reads msgvault's answer on stdin as a round does and prints
// each conversation within its size, one line: kind, size, messages,
// list, a chat's title. No number, no address. integration.sh runs the
// statement against the real msgvault with it.
func gespraecheWerkzeug(args []string, ein io.Reader, aus io.Writer) int {
	if slices.Contains(args, "-sql") {
		json.NewEncoder(aus).Encode(map[string]string{"sql": gespraecheSQL()})
		return 0
	}
	var r abfrageAntwort
	if err := json.NewDecoder(ein).Decode(&r); err != nil {
		fmt.Fprintln(aus, "keine Antwort von msgvault:", err)
		return 1
	}
	gs, err := gespraecheLesen(r)
	if err != nil {
		fmt.Fprintln(aus, err)
		return 1
	}
	for _, g := range gs {
		n := netzGespraech{Art: g.Art, Groesse: g.Groesse, Sender: 2} // listed whoever wrote
		titel := ""
		switch {
		case g.Art == gespraechChat && g.Groesse == direktGroesse:
			n.Art, titel = gespraechDirekt, fmt.Sprintf(" %d", g.Sender) // a chat of two with how many of them wrote
		case g.Art == gespraechChat:
			titel = " " + g.Titel
		}
		if !n.zaehlt() {
			continue
		}
		fmt.Fprintf(aus, "%s %d %d %t%s\n", n.Art, g.Groesse, g.Nachrichten, g.Liste, titel)
	}
	return 0
}

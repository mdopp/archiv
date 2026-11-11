package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Groups read by the box's model (archiv#42, owner 2026-11-11: „Gruppen
// durch das Modell einordnen“). While the vault is open, each round sends
// the active groups not yet named by the person and not yet read to the
// box's model, one after the other, the lease asked before each (as
// hinweise.go does): the group's title, its members' names (the person
// as „du“) and its newest gruppeProben messages, each at most
// gruppeProbeText characters. The model answers one category of the
// group questions and how sure it is; a reading at modellSicher or more
// is the group's suggestion, the person's tap confirms it (the chip it
// marks). The reading is kept per group in the vault
// (umfeld/gruppen-modell.json), so no group is sent twice; nothing of a
// message or an answer is logged.

const (
	gruppeProben    = 5   // a group's newest messages the model reads
	gruppeProbeText = 200 // characters of each
	gruppeMitglied  = 12  // members named to the model
)

// gruppenLesung is what the model read of a group: its category ("" none
// readable), how sure, when.
type gruppenLesung struct {
	Kategorie string    `json:"k,omitempty"`
	Sicher    float64   `json:"s,omitempty"`
	Am        time.Time `json:"am"`
}

func (i Innen) umfeldGruppenModell() string { return filepath.Join(i.Umfeld(), "gruppen-modell.json") }

func gruppenModellLaden(innen Innen) map[string]gruppenLesung {
	out := map[string]gruppenLesung{}
	if b, err := os.ReadFile(innen.umfeldGruppenModell()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// modellKategorie is the category the model read of the group, if it is
// sure enough and says something (Sonstiges says nothing); n.mu is held.
func (n *Netz) modellKategorie(id string) string {
	if l, da := n.gruppenModell[id]; da && l.Sicher >= modellSicher && l.Kategorie != kategorieSonst {
		return l.Kategorie
	}
	return ""
}

// gruppenModellSetzen gives the net what the model read of the groups.
func (n *Netz) gruppenModellSetzen(gm map[string]gruppenLesung) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.gruppenModell = maps.Clone(gm)
}

// gruppenZuLesen are the active groups the person has not named (Später
// aside) and the model has not read, the newest first; n.mu is free.
func (n *Netz) gruppenZuLesen(gm map[string]gruppenLesung) (out []GruppenFrage) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, f := range n.gruppenFragen() {
		if _, da := gm[f.Gruppe.ID]; !da && nurZiffern.MatchString(f.Gruppe.ID) {
			out = append(out, *f.Gruppe)
		}
	}
	return out
}

// gruppenProbenSQL is the one read-only statement: the newest
// gruppeProben messages with text of each conversation of ids (digits only).
func gruppenProbenSQL(ids []string) string {
	return fmt.Sprintf(`WITH m AS (
  SELECT conversation_id, snippet,
    row_number() OVER (PARTITION BY conversation_id ORDER BY sent_at DESC, id DESC) AS r
  FROM messages WHERE deleted_at IS NULL AND COALESCE(snippet, '') <> '' AND conversation_id IN (%s)
)
SELECT CAST(conversation_id AS VARCHAR) AS gespraech, snippet AS text FROM m WHERE r <= %d ORDER BY gespraech, r`, strings.Join(ids, ", "), gruppeProben)
}

// GruppenProben reads the newest messages of the conversations ids, by conversation.
func (m *MsgvaultAPI) GruppenProben(ctx context.Context, ids []string) (map[string][]string, error) {
	ids = nurIDs(ids)
	if m == nil || len(ids) == 0 {
		return nil, nil
	}
	var r abfrageAntwort
	err := m.rufe(ctx, http.MethodPost, "/api/v1/query", nil, map[string]string{"sql": gruppenProbenSQL(ids)}, &r)
	var f *apiFehler
	switch {
	case errors.As(err, &f) && f.Status == http.StatusServiceUnavailable:
		return nil, errGespraecheSpaeter
	case err != nil:
		return nil, err
	case r.Columns == nil:
		return nil, errGespraecheSpaeter
	}
	return probenLesen(r)
}

// probenLesen turns the rows into each conversation's texts, by column name.
func probenLesen(r abfrageAntwort) (map[string][]string, error) {
	spalte := map[string]int{}
	for i, c := range r.Columns {
		spalte[c] = i
	}
	for _, c := range []string{"gespraech", "text"} {
		if _, da := spalte[c]; !da {
			return nil, fmt.Errorf("msgvault-Abfrage ohne Spalte %q", c)
		}
	}
	out := map[string][]string{}
	for _, z := range r.Rows {
		if len(z) < len(r.Columns) || z[spalte["gespraech"]] == nil || z[spalte["text"]] == nil {
			continue
		}
		id := fmt.Sprint(z[spalte["gespraech"]])
		if t := strings.Join(strings.Fields(fmt.Sprint(z[spalte["text"]])), " "); t != "" && len(out[id]) < gruppeProben {
			out[id] = append(out[id], t)
		}
	}
	return out, nil
}

// gruppenSystem is the model's instruction for a group: the same for every one.
func gruppenSystem() string {
	var ks []string
	for _, k := range kategorien {
		ks = append(ks, k+" ("+kategorieWorte[k]+")")
	}
	return `Du hilfst einer Person, ihre Gruppen-Chats einzuordnen. ` +
		`Du bekommst den Namen einer Gruppe, ihre Mitglieder und ein paar ihrer neuesten Nachrichten. ` +
		`Sag, was für eine Gruppe das ist. Antworte nur mit JSON, genau in dieser Form: {"kategorie":"familie","sicher":0.8}. ` +
		`Erlaubte Kategorien: ` + strings.Join(ks, ", ") + `. „sicher“ ist eine Zahl von 0 bis 1. ` +
		`Ein Name allein ist kein Beweis („Team Oma“ kann die Familie sein); passt keine Kategorie, antworte {"kategorie":"sonstiges","sicher":0.5}.`
}

// gruppenFrage is the model's question about g: its title, its members
// (the person as „du“), the texts.
func (n *Netz) gruppenFrage(g GruppenFrage, texte []string) string {
	ich := n.Ich()
	var b strings.Builder
	fmt.Fprintf(&b, "Gruppe: „%s“\nMitglieder", g.Titel)
	if gr := n.gespraechGroesse(g.ID); gr > 0 {
		fmt.Fprintf(&b, " (%d insgesamt, diese stehen im Telefonbuch)", gr)
	}
	b.WriteString(":\n")
	for _, k := range g.Mitglieder[:min(len(g.Mitglieder), gruppeMitglied)] {
		if k.ID == ich {
			b.WriteString("- die Person selbst, der dieses Archiv gehört\n")
			continue
		}
		b.WriteString("- " + k.Name + "\n")
	}
	if len(texte) > 0 {
		b.WriteString("\nNeueste Nachrichten:\n")
		for _, t := range texte {
			r := []rune(t)
			if len(r) > gruppeProbeText {
				r = append(r[:gruppeProbeText], '…')
			}
			b.WriteString("«" + string(r) + "»\n")
		}
	}
	return b.String()
}

// gespraechGroesse is how many took part in the conversation id.
func (n *Netz) gespraechGroesse(id string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.gespraech[id].Groesse
}

// gruppenAntwortLesen reads the model's answer strictly: one JSON object
// with a known category and a sureness from 0 to 1, nothing after it; a
// fence around it is taken off. Unreadable: the zero reading.
func gruppenAntwortLesen(s string) gruppenLesung {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "```"); ok {
		rest = strings.TrimPrefix(rest, "json")
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "```"))
	}
	var a struct {
		Kategorie string   `json:"kategorie"`
		Sicher    *float64 `json:"sicher"`
	}
	d := json.NewDecoder(strings.NewReader(s))
	if err := d.Decode(&a); err != nil {
		return gruppenLesung{}
	}
	if _, err := d.Token(); err != io.EOF {
		return gruppenLesung{}
	}
	k := strings.ToLower(strings.TrimSpace(a.Kategorie))
	if !slices.Contains(kategorien, k) || a.Sicher == nil || *a.Sicher < 0 || *a.Sicher > 1 {
		return gruppenLesung{}
	}
	return gruppenLesung{Kategorie: k, Sicher: *a.Sicher}
}

// gruppenRunde sends the groups to read to the model, one after the
// other, the lease asked before each; each reading is kept in the vault
// at once. The model taken or silent, or the vault closing: it stops,
// the rest waits. It logs counts, never a text or an answer.
func (u *Umfeld) gruppenRunde(ctx context.Context, innen Innen, n *Netz) {
	if !u.Modell.eingerichtet() || u.Msgvault == nil || n == nil {
		return
	}
	gm := gruppenModellLaden(innen)
	n.gruppenModellSetzen(gm)
	zuLesen := n.gruppenZuLesen(gm)
	if len(zuLesen) == 0 {
		return
	}
	gelesenJetzt, grund := 0, ""
	for i := 0; i < len(zuLesen) && grund == ""; i += letzteHoechst {
		schub := zuLesen[i:min(len(zuLesen), i+letzteHoechst)]
		var ids []string
		for _, g := range schub {
			ids = append(ids, g.ID)
		}
		proben, err := u.Msgvault.GruppenProben(ctx, ids)
		if err != nil {
			grund = err.Error()
			break
		}
		for _, g := range schub {
			if ctx.Err() != nil {
				grund = "der Tresor schließt"
				break
			}
			modell, err := u.Modell.frei(ctx)
			if err != nil {
				grund = err.Error()
				break
			}
			antwort, err := u.Modell.fragen(ctx, modell, gruppenSystem(), n.gruppenFrage(g, proben[g.ID]))
			if err != nil {
				grund = err.Error()
				break
			}
			l := gruppenAntwortLesen(antwort)
			l.Am = u.now()
			gm[g.ID] = l
			if err := jsonImTresor(innen.umfeldGruppenModell(), gm); err != nil {
				delete(gm, g.ID)
				grund = "speichern: " + err.Error()
				break
			}
			gelesenJetzt++
			n.gruppenModellSetzen(gm)
		}
	}
	if grund != "" && ctx.Err() == nil {
		log.Printf("archiv: Umfeld: Gruppen warten auf eine spätere Runde: %s", grund)
	}
	if gelesenJetzt > 0 {
		u.geaendert()
		log.Printf("archiv: Umfeld: %d Gruppen vom Modell der Box eingeordnet", gelesenJetzt)
	}
}

// gruppenWerkzeug is `archiv umfeld-gruppen`: with -sql id… it prints the
// body msgvault's POST /api/v1/query takes for those groups' newest
// messages; without, it reads msgvault's answer on stdin as a round does
// and prints one line per conversation: its id and how many texts. No text.
func gruppenWerkzeug(args []string, ein io.Reader, aus io.Writer) int {
	if len(args) > 0 && args[0] == "-sql" {
		ids := nurIDs(args[1:])
		if len(ids) == 0 {
			fmt.Fprintln(aus, "keine Gesprächs-ids")
			return 2
		}
		json.NewEncoder(aus).Encode(map[string]string{"sql": gruppenProbenSQL(ids)})
		return 0
	}
	var r abfrageAntwort
	if err := json.NewDecoder(ein).Decode(&r); err != nil {
		fmt.Fprintln(aus, "keine Antwort von msgvault:", err)
		return 1
	}
	ps, err := probenLesen(r)
	if err != nil {
		fmt.Fprintln(aus, err)
		return 1
	}
	for _, id := range slices.Sorted(maps.Keys(ps)) {
		fmt.Fprintf(aus, "%s texte=%d\n", id, len(ps[id]))
	}
	return 0
}

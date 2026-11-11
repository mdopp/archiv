package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The edge page's last lines (archiv#27 step 4): for the group chats and
// mail threads both are in, msgvault's newest message of each — its id,
// for the link into msgvault's own view (/messages/<id> opens it in its
// conversation), when, whether the person sent it, its text. Read live
// through msgvault's SQL API (read only) while the page is built; nothing
// of it is kept. msgvault closed, busy or silent: the page shows the
// conversations without their last line.

const (
	letzteZeichen = 120 // characters of a last line the page shows
	letzteWarten  = 3 * time.Second
	letzteHoechst = 30 // conversations asked about at once
)

var nurZiffern = regexp.MustCompile(`^[0-9]{1,19}$`)

// letzteNachricht is a conversation's newest message.
type letzteNachricht struct {
	ID     string
	Am     time.Time
	VonMir bool
	Text   string
}

// letzteSQL is the one read-only statement: the newest message of each
// conversation of ids (msgvault's ids, digits only: anything else is
// left out before it gets here).
func letzteSQL(ids []string) string {
	return fmt.Sprintf(`WITH m AS (
  SELECT id, conversation_id, sent_at, is_from_me, snippet,
    row_number() OVER (PARTITION BY conversation_id ORDER BY sent_at DESC, id DESC) AS r
  FROM messages WHERE deleted_at IS NULL AND conversation_id IN (%s)
)
SELECT CAST(conversation_id AS VARCHAR) AS gespraech, CAST(id AS VARCHAR) AS nachricht,
  strftime(sent_at, '%%Y-%%m-%%dT%%H:%%M:%%SZ') AS am, is_from_me AS von_mir, COALESCE(snippet, '') AS text
FROM m WHERE r = 1`, strings.Join(ids, ", "))
}

// nurIDs are the ids of msgvault's form, at most letzteHoechst.
func nurIDs(ids []string) (out []string) {
	for _, id := range ids {
		if nurZiffern.MatchString(id) && !slices.Contains(out, id) && len(out) < letzteHoechst {
			out = append(out, id)
		}
	}
	return out
}

// Letzte reads the newest message of each conversation of ids, by conversation.
func (m *MsgvaultAPI) Letzte(ctx context.Context, ids []string) (map[string]letzteNachricht, error) {
	ids = nurIDs(ids)
	if m == nil || len(ids) == 0 {
		return nil, nil
	}
	ctx, fertig := context.WithTimeout(ctx, letzteWarten)
	defer fertig()
	var r abfrageAntwort
	err := m.rufe(ctx, http.MethodPost, "/api/v1/query", nil, map[string]string{"sql": letzteSQL(ids)}, &r)
	var f *apiFehler
	switch {
	case errors.As(err, &f) && f.Status == http.StatusServiceUnavailable:
		return nil, errGespraecheSpaeter
	case err != nil:
		return nil, err
	case r.Columns == nil:
		return nil, errGespraecheSpaeter
	}
	return letzteLesen(r)
}

// letzteLesen turns the rows into last messages, by column name.
func letzteLesen(r abfrageAntwort) (map[string]letzteNachricht, error) {
	spalte := map[string]int{}
	for i, c := range r.Columns {
		spalte[c] = i
	}
	for _, c := range []string{"gespraech", "nachricht", "am", "von_mir", "text"} {
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
	out := map[string]letzteNachricht{}
	for _, z := range r.Rows {
		if len(z) < len(r.Columns) {
			continue
		}
		t, _ := time.Parse(time.RFC3339, text(z, "am"))
		mir, _ := z[spalte["von_mir"]].(bool)
		out[text(z, "gespraech")] = letzteNachricht{ID: text(z, "nachricht"), Am: t, VonMir: mir, Text: strings.Join(strings.Fields(text(z, "text")), " ")}
	}
	return out, nil
}

// kanteWerkzeug is `archiv umfeld-kante`: with -sql id… it prints the body
// msgvault's POST /api/v1/query takes for those conversations' last
// lines; without, it reads msgvault's answer on stdin as the page does and
// prints one line per conversation: its id, whether a message id came,
// sent by the person, whether there is text. No text.
func kanteWerkzeug(args []string, ein io.Reader, aus io.Writer) int {
	if len(args) > 0 && args[0] == "-sql" {
		ids := nurIDs(args[1:])
		if len(ids) == 0 {
			fmt.Fprintln(aus, "keine Gesprächs-ids")
			return 2
		}
		json.NewEncoder(aus).Encode(map[string]string{"sql": letzteSQL(ids)})
		return 0
	}
	var r abfrageAntwort
	if err := json.NewDecoder(ein).Decode(&r); err != nil {
		fmt.Fprintln(aus, "keine Antwort von msgvault:", err)
		return 1
	}
	ls, err := letzteLesen(r)
	if err != nil {
		fmt.Fprintln(aus, err)
		return 1
	}
	for _, id := range slices.Sorted(func(yield func(string) bool) {
		for k := range ls {
			if !yield(k) {
				return
			}
		}
	}) {
		l := ls[id]
		fmt.Fprintf(aus, "%s nachricht=%t mir=%t text=%t\n", id, nurZiffern.MatchString(l.ID), l.VonMir, l.Text != "")
	}
	return 0
}

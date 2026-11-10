package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// MsgvaultAPI reads and decides through msgvault's own HTTP API on
// loopback, with the vault's key (only while open and answering). Routes
// of msgvault 0.22.0 (api/openapi.yaml at tag v0.22.0):
//
//	GET  /api/v1/carddav/books                                 the address books and their roles
//	GET  /api/v1/identity/match-candidates?state=candidate|conflict  suggestions, with their review token
//	GET  /api/v1/identity/match-candidates/{id}                one, with a fresh token
//	POST /api/v1/identity/match-candidates/{id}/review/accept  {"review_token": …}
//	POST /api/v1/identity/match-candidates/{id}/review/reject  {"review_token": …}
//	GET  /api/v1/participants/{id}                             who an observed address is (display_label, identifiers)
//	GET  /api/v1/people, /api/v1/people/{id}, …/identities, …/profile  the saved persons
//	POST /api/v1/people/{id}/merge                             If-Match: both revisions, Idempotency-Key
//	POST /api/v1/query                                         {"sql": …}, read only: the Umfeld's chats and threads (gespraeche.go)
type MsgvaultAPI struct {
	Port   int
	Key    func() string // Tresor.APIKey: "" while closed or starting
	Client *http.Client
}

// apiFehler is msgvault's error answer ({"error": code, "message": …}).
type apiFehler struct {
	Status  int
	Code    string `json:"error"`
	Meldung string `json:"message"`
}

func (e *apiFehler) Error() string {
	return fmt.Sprintf("msgvault %d %s: %s", e.Status, e.Code, e.Meldung)
}

var errNichtBereit = errors.New("msgvault ist nicht offen")

func (m *MsgvaultAPI) rufe(ctx context.Context, method, pfad string, kopf http.Header, rein, raus any) error {
	key := ""
	if m.Key != nil {
		key = m.Key()
	}
	if key == "" {
		return errNichtBereit
	}
	var body io.Reader
	if rein != nil {
		b, err := json.Marshal(rein)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	q, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(m.Port)+pfad, body)
	if err != nil {
		return err
	}
	for k, v := range kopf {
		q.Header[k] = v
	}
	q.Header.Set("X-API-Key", key)
	if rein != nil {
		q.Header.Set("Content-Type", "application/json")
	}
	c := m.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		f := &apiFehler{Status: resp.StatusCode}
		json.Unmarshal(b, f)
		return f
	}
	if raus == nil {
		return nil
	}
	return json.Unmarshal(b, raus)
}

// --- address books

type CardDAVBuch struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	WriteTarget  bool   `json:"write_target"`
	Subscribed   bool   `json:"subscribed"`
	LookupSource bool   `json:"lookup_source"`
}

func (m *MsgvaultAPI) Buecher(ctx context.Context) ([]CardDAVBuch, error) {
	var r struct {
		Books []CardDAVBuch `json:"books"`
	}
	err := m.rufe(ctx, http.MethodGet, "/api/v1/carddav/books", nil, nil, &r)
	return r.Books, err
}

// --- identity suggestions

type Vorschlag struct {
	ID              int64    `json:"id"`
	State           string   `json:"state"`
	Basis           string   `json:"basis"`
	LeftKind        string   `json:"left_kind"`
	LeftID          int64    `json:"left_id"`
	RightKind       string   `json:"right_kind"`
	RightID         int64    `json:"right_id"`
	NormalizedValue *string  `json:"normalized_value"`
	ServiceSlug     *string  `json:"service_slug"`
	Source          string   `json:"source"`
	ReviewToken     *string  `json:"review_token"`
	Actionable      bool     `json:"actionable"`
	Blocker         *string  `json:"blocker"`
	Confidence      *float64 `json:"confidence"`
	Evidence        []struct {
		Kind   string  `json:"evidence_kind"`
		Detail *string `json:"detail"`
		Source string  `json:"source"`
	} `json:"evidence"`
	LeftPerson  *personBindung `json:"left_person"`
	RightPerson *personBindung `json:"right_person"`
}

type personBindung struct {
	PersonID int64 `json:"person_id"`
	Revision int64 `json:"revision"`
}

func (v Vorschlag) Token() string {
	if v.ReviewToken == nil {
		return ""
	}
	return *v.ReviewToken
}

// Offen: not decided yet (msgvault keeps conflicts in their own state).
func (v Vorschlag) Offen() bool { return v.State == "candidate" || v.State == "conflict" }

// Vorschlaege lists the open suggestions: msgvault's API (unlike its
// CLI) takes one state per request, so candidate and conflict are asked
// for apart.
func (m *MsgvaultAPI) Vorschlaege(ctx context.Context, limit int) ([]Vorschlag, error) {
	var alle []Vorschlag
	for _, state := range []string{"candidate", "conflict"} {
		var r struct {
			Candidates []Vorschlag `json:"candidates"`
		}
		if err := m.rufe(ctx, http.MethodGet, "/api/v1/identity/match-candidates?state="+state+"&limit="+strconv.Itoa(limit), nil, nil, &r); err != nil {
			return nil, err
		}
		alle = append(alle, r.Candidates...)
	}
	return alle, nil
}

func (m *MsgvaultAPI) Vorschlag(ctx context.Context, id int64) (Vorschlag, error) {
	var v Vorschlag
	err := m.rufe(ctx, http.MethodGet, "/api/v1/identity/match-candidates/"+strconv.FormatInt(id, 10), nil, nil, &v)
	return v, err
}

// Entscheiden accepts (ja) or rejects a suggestion with the token the
// person saw. A token msgvault no longer accepts comes back as
// errVeraltet: the evidence changed meanwhile.
func (m *MsgvaultAPI) Entscheiden(ctx context.Context, id int64, ja bool, token string) error {
	art := "reject"
	if ja {
		art = "accept"
	}
	err := m.rufe(ctx, http.MethodPost, "/api/v1/identity/match-candidates/"+strconv.FormatInt(id, 10)+"/review/"+art, nil,
		map[string]string{"review_token": token}, nil)
	var f *apiFehler
	switch {
	case errors.As(err, &f) && f.Code == "identity_match_review_stale":
		return errVeraltet
	case errors.As(err, &f) && f.Code == "person_merge_required":
		return errZweiPersonen
	}
	return err
}

var errZweiPersonen = errors.New("zwei verschiedene Personen")

var errVeraltet = errors.New("veraltet")

// --- persons

type Person struct {
	ID              int64   `json:"id"`
	DisplayName     string  `json:"display_name"`
	Revision        int64   `json:"revision"`
	ParticipantIDs  []int64 `json:"participant_ids"`
	VCardUID        string  `json:"vcard_uid"`
	CardDAVBindings []struct {
		RemoteUID string `json:"remote_uid"`
	} `json:"carddav_bindings"`
}

// GeloeschtAm: when this person's card was deleted on the phone, by the
// archive's contact store (weg: UID -> date).
func (p Person) GeloeschtAm(weg map[string]time.Time) (time.Time, bool) {
	for _, b := range p.CardDAVBindings {
		if t, ok := weg[b.RemoteUID]; ok {
			return t, true
		}
	}
	t, ok := weg[p.VCardUID]
	return t, ok
}

func (m *MsgvaultAPI) Personen(ctx context.Context) ([]Person, error) {
	var r struct {
		People []Person `json:"people"`
	}
	err := m.rufe(ctx, http.MethodGet, "/api/v1/people", nil, nil, &r)
	return r.People, err
}

func (m *MsgvaultAPI) Person(ctx context.Context, id int64) (Person, error) {
	var p Person
	err := m.rufe(ctx, http.MethodGet, "/api/v1/people/"+strconv.FormatInt(id, 10), nil, nil, &p)
	return p, err
}

type Identitaet struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func (m *MsgvaultAPI) Identitaeten(ctx context.Context, id int64) ([]Identitaet, error) {
	var r struct {
		Identities []Identitaet `json:"identities"`
	}
	err := m.rufe(ctx, http.MethodGet, "/api/v1/people/"+strconv.FormatInt(id, 10)+"/identities", nil, nil, &r)
	return r.Identities, err
}

// Kontaktpunkte are the numbers and addresses a person's profile holds
// (from the address book: a card's TEL and EMAIL), which Identitaeten
// leaves out until they appear in a message.
func (m *MsgvaultAPI) Kontaktpunkte(ctx context.Context, id int64) ([]Identitaet, error) {
	var r struct {
		ContactPoints []struct {
			Kind     string `json:"address_kind"`
			Original string `json:"original_value"`
		} `json:"contact_points"`
	}
	err := m.rufe(ctx, http.MethodGet, "/api/v1/people/"+strconv.FormatInt(id, 10)+"/profile", nil, nil, &r)
	var out []Identitaet
	for _, c := range r.ContactPoints {
		out = append(out, Identitaet{Kind: c.Kind, Value: c.Original})
	}
	return out, err
}

// Teilnehmer is an observed address (participant) as msgvault names it.
type Teilnehmer struct {
	ID           int64  `json:"id"`
	DisplayLabel string `json:"display_label"`
	DisplayName  string `json:"display_name"`
	Identifiers  []struct {
		Type         string `json:"type"`
		Value        string `json:"value"`
		DisplayValue string `json:"display_value"`
		ServiceLabel string `json:"service_label"`
	} `json:"identifiers"`
}

func (m *MsgvaultAPI) Teilnehmer(ctx context.Context, id int64) (Teilnehmer, error) {
	var t Teilnehmer
	err := m.rufe(ctx, http.MethodGet, "/api/v1/participants/"+strconv.FormatInt(id, 10), nil, nil, &t)
	return t, err
}

// Zusammenfuehren merges absorbed into survivor, guarded by the two
// revisions the person saw in the preview: if either changed since,
// msgvault refuses (409/412) and nothing happens. The key makes a
// repeated click the same request. A merge is recorded and can be split
// again in msgvault.
func (m *MsgvaultAPI) Zusammenfuehren(ctx context.Context, survivor, survivorRev, absorbed, absorbedRev int64, schluessel string) error {
	kopf := http.Header{
		"If-Match":        {fmt.Sprintf(`"person-%d-r%d", "person-%d-r%d"`, survivor, survivorRev, absorbed, absorbedRev)},
		"Idempotency-Key": {schluessel},
	}
	err := m.rufe(ctx, http.MethodPost, "/api/v1/people/"+strconv.FormatInt(survivor, 10)+"/merge", kopf,
		map[string]int64{"absorbed_person_id": absorbed}, nil)
	var f *apiFehler
	if errors.As(err, &f) && f.Code == "person_merge_revision_conflict" {
		return fmt.Errorf("%w: %v", errVeraltet, err)
	}
	return err
}

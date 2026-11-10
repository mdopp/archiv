package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func personenWeb(t *testing.T) (*webTest, *fakeAPI) {
	t.Helper()
	wt := neuesWeb(t)
	api, client := neueAPI(t)
	wt.w.Personen = &Personenkarte{API: client}
	wt.oeffnen(t)
	nr := "+4915199999999"
	api.vorschlaege = []map[string]any{
		{"id": float64(7), "state": "candidate", "basis": "phone", "left_kind": "participant", "left_id": 11, "right_kind": "participant", "right_id": 12,
			"normalized_value": nr, "source": "archive_observation", "actionable": true, "evidence": []any{}},
		{"id": float64(8), "state": "accepted", "basis": "email", "left_kind": "participant", "left_id": 1, "right_kind": "participant", "right_id": 2,
			"source": "archive_observation", "actionable": false, "evidence": []any{}},
		{"id": float64(9), "state": "conflict", "basis": "email", "left_kind": "participant", "left_id": 13, "right_kind": "participant", "right_id": 14,
			"normalized_value": "carla@example.org", "source": "carddav_import", "actionable": false, "blocker": "person_merge_required",
			"left_person": map[string]any{"person_id": 1, "revision": 3}, "right_person": map[string]any{"person_id": 2, "revision": 5}, "evidence": []any{}},
		{"id": float64(10), "state": "candidate", "basis": "phone", "left_kind": "observation", "left_id": 1, "right_kind": "contact_point", "right_id": 2,
			"source": "archive_observation", "actionable": false, "blocker": "endpoint_unsupported", "evidence": []any{}},
	}
	api.teilnehmer[11] = map[string]any{"id": 11, "display_label": "Ben", "identifiers": []any{map[string]any{"type": "whatsapp", "value": nr, "display_value": nr}}}
	api.teilnehmer[12] = map[string]any{"id": 12, "display_label": "+4915199999999", "identifiers": []any{map[string]any{"type": "phone", "value": nr}}}
	api.personen = map[int64]Person{1: {ID: 1, DisplayName: "Carla", Revision: 3}, 2: {ID: 2, DisplayName: "Carla Müller", Revision: 5}}
	api.identitaet[1] = []Identitaet{{Kind: "email", Value: "carla@example.org"}}
	api.identitaet[2] = []Identitaet{{Kind: "phone", Value: "+4917012345678"}}
	return wt, api
}

var tokenFeld = regexp.MustCompile(`name=id value="7"><input type=hidden name=token value="([^"]+)"`)

// The card asks in plain German, with what speaks for it; Ja and Nein
// carry the token of what the person saw.
func TestTheSuggestionsCardAsksInPlainGerman(t *testing.T) {
	wt, api := personenWeb(t)
	body := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	for _, want := range []string{"Ist das dieselbe Person?", "<b>Ben (+4915199999999)</b> und <b>+4915199999999</b>", "Beide haben die Telefonnummer +4915199999999.",
		"gefunden aus deinen Nachrichten", `value=ja`, `value=nein`, "Zusammenführen ansehen", `/tresor/personen/vorschau?a=1&amp;b=2`,
		"1 weiterer Vorschlag kann nur das Archiv-Programm selbst klären", "Zwei Personen von Hand zusammenführen", `<option value="1">Carla</option>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `name=id value="8"`) {
		t.Fatal("a decided suggestion is asked again")
	}
	m := tokenFeld.FindStringSubmatch(body)
	if m == nil || m[1] != "token-7-0" {
		t.Fatalf("token: %v", m)
	}
	rec := wt.rufe("POST", "/tresor/personen/entscheiden", anna, url.Values{"id": {"7"}, "token": {m[1]}, "antwort": {"ja"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/tresor?tab=fragen&m=person-ja") {
		t.Fatalf("%d %s", rec.Code, rec.Header())
	}
	var rein map[string]string
	json.Unmarshal([]byte(api.koerper["/api/v1/identity/match-candidates/7/review/accept"]), &rein)
	if rein["review_token"] != "token-7-0" {
		t.Fatalf("accept got %v", api.koerper)
	}
	rec = wt.rufe("POST", "/tresor/personen/entscheiden", anna, url.Values{"id": {"7"}, "token": {"token-7-0"}, "antwort": {"nein"}})
	if rec.Code != http.StatusSeeOther || api.koerper["/api/v1/identity/match-candidates/7/review/reject"] == "" {
		t.Fatalf("Nein: %d %v", rec.Code, api.gesehen())
	}
}

// A token gone stale: the page says so and asks again, with the new token.
func TestAStaleTokenIsFetchedAgainAndSaid(t *testing.T) {
	wt, api := personenWeb(t)
	wt.rufe("GET", "/tresor", anna, nil)
	api.mu.Lock()
	api.veraltet = true
	api.mu.Unlock()
	rec := wt.rufe("POST", "/tresor/personen/entscheiden", anna, url.Values{"id": {"7"}, "token": {"token-7-0"}, "antwort": {"ja"}})
	body := rec.Body.String()
	if rec.Code != http.StatusConflict || !strings.Contains(body, saysVorschlagVeraltet) {
		t.Fatalf("%d %s", rec.Code, body)
	}
	if m := tokenFeld.FindStringSubmatch(body); m == nil || m[1] != "token-7-1" {
		t.Fatalf("the page does not show the fresh token: %v", m)
	}
}

// Merging by hand: first the preview of both, then the merge guarded by
// the revisions shown; a change in between shows the preview again.
func TestMergeTwoPersonsWithAPreview(t *testing.T) {
	wt, api := personenWeb(t)
	rec := wt.rufe("GET", "/tresor/personen/vorschau?a=1&b=2", anna, nil)
	body := rec.Body.String()
	for _, want := range []string{"Bleibt (mit diesem Namen): Carla", "Geht in ihr auf: Carla Müller", "carla@example.org", "+4917012345678", "(Telefon, in deinen Nachrichten)", "+49 151 7777777 <span class=k>(Telefon, im Adressbuch)",
		`name=ar value="3"`, `name=br value="5"`, "Andersherum", "Ja, zusammenführen"} {
		if !strings.Contains(body, want) {
			t.Fatalf("preview lacks %q:\n%s", want, body)
		}
	}
	if n := len(api.personen); n != 2 {
		t.Fatal("the preview changed something")
	}
	if rec := wt.rufe("GET", "/tresor/personen/vorschau?a=1&b=1", anna, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("the same person twice: %d", rec.Code)
	}
	schluessel := regexp.MustCompile(`name=schluessel value="([0-9a-f]+)"`).FindStringSubmatch(body)[1]

	// Carla Müller changed meanwhile
	api.mu.Lock()
	p := api.personen[2]
	p.Revision = 6
	api.personen[2] = p
	api.mu.Unlock()
	form := url.Values{"a": {"1"}, "ar": {"3"}, "b": {"2"}, "br": {"5"}, "schluessel": {schluessel}}
	rec = wt.rufe("POST", "/tresor/personen/zusammen", anna, form)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), saysZusammenVeraltet) || !strings.Contains(rec.Body.String(), `name=br value="6"`) {
		t.Fatalf("a stale preview: %d %s", rec.Code, rec.Body)
	}
	form.Set("br", "6")
	rec = wt.rufe("POST", "/tresor/personen/zusammen", anna, form)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/tresor?tab=fragen&m=zusammen") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	kopf := api.kopf["/api/v1/people/1/merge"]
	if kopf.Get("If-Match") != `"person-1-r3", "person-2-r6"` || kopf.Get("Idempotency-Key") != schluessel {
		t.Fatalf("merge headers: %v", kopf)
	}
	if _, da := api.personen[2]; da {
		t.Fatal("not merged")
	}
}

func TestSuggestionsNeedAnOpenArchive(t *testing.T) {
	wt := neuesWeb(t)
	_, client := neueAPI(t)
	wt.w.Personen = &Personenkarte{API: client}
	rec := wt.rufe("POST", "/tresor/personen/entscheiden", anna, url.Values{"id": {"7"}, "token": {"t"}, "antwort": {"ja"}})
	if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), saysNichtBereit) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMsgvaultHinweise is msgvault's POST /api/v1/query for both of the
// round's statements: no conversations, and the messages with a word.
type fakeMsgvaultHinweise struct {
	*httptest.Server
	mu     sync.Mutex
	zeilen [][]any
	// wuensche answer the birthday wishes' statement (geburtstage.go),
	// proben the groups' newest messages (gruppenmodell.go)
	wuensche, proben [][]any
	// paare answer the couples' statement (paare.go), herkunft the mails'
	// (herkunft.go), koerper GET /api/v1/messages/{id}'s body
	paare, herkunft [][]any
	koerper         map[string]string
}

var hinweisSpalten = []string{"nachricht", "art", "am", "von_mir", "text", "rolle", "telefon", "mail"}

func neuesMsgvaultHinweise(t *testing.T) (*fakeMsgvaultHinweise, *MsgvaultAPI) {
	f := &fakeMsgvaultHinweise{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var q struct {
			SQL string `json:"sql"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		if id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/messages/"); ok && r.Method == http.MethodGet && r.Header.Get("X-API-Key") == "vault-key" {
			if body, da := f.koerper[id]; da {
				jsonAus(w, http.StatusOK, map[string]any{"id": id, "body": body})
			} else {
				jsonAus(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			}
			return
		}
		if r.Header.Get("X-API-Key") != "vault-key" || r.URL.Path != "/api/v1/query" {
			jsonAus(w, http.StatusUnauthorized, map[string]string{"error": "nein"})
			return
		}
		if strings.Contains(q.SQL, "PARTITION BY m.sender_id") {
			jsonAus(w, http.StatusOK, map[string]any{"columns": []string{"nachricht", "am", "mail"}, "rows": f.herkunft})
			return
		}
		if strings.Contains(q.SQL, `(&|\+)`) {
			jsonAus(w, http.StatusOK, map[string]any{"columns": hinweisSpalten, "rows": f.paare})
			return
		}
		if strings.Contains(q.SQL, "WHERE r <= ") {
			jsonAus(w, http.StatusOK, map[string]any{"columns": []string{"gespraech", "text"}, "rows": f.proben})
			return
		}
		if strings.Contains(q.SQL, "geburtstag") {
			jsonAus(w, http.StatusOK, map[string]any{"columns": hinweisSpalten, "rows": f.wuensche})
			return
		}
		if !strings.Contains(q.SQL, "regexp_matches(lower(snippet)") {
			jsonAus(w, http.StatusOK, map[string]any{"columns": abfrageSpalten, "rows": [][]any{}})
			return
		}
		jsonAus(w, http.StatusOK, map[string]any{"columns": hinweisSpalten, "rows": f.zeilen})
	}))
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.URL)
	port, _ := strconv.Atoi(u.Port())
	return f, &MsgvaultAPI{Port: port, Key: func() string { return "vault-key" }}
}

func (f *fakeMsgvaultHinweise) setzen(z ...[]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zeilen = append(f.zeilen, z...)
}

func hinweisZeile(id string, mir bool, text, rolle, tel string) []any {
	return []any{id, "chat", "2026-10-01T08:00:00Z", mir, text, rolle, tel, ""}
}

// fakeModell is laib-lease's proxy: the lease (state, the household
// alias) and /v1/chat/completions, answering what antwort says.
type fakeModell struct {
	*httptest.Server
	mu       sync.Mutex
	lease    string
	status   int
	antwort  func(frage string) string
	anfragen []map[string]any
	kopf     []http.Header
}

func neuesModell(t *testing.T) (*fakeModell, *Modell) {
	f := &fakeModell{lease: "none", status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/model-lease":
			jsonAus(w, http.StatusOK, map[string]any{"state": f.lease, "alias": "qwen-haus", "holder": map[bool]string{true: "homes"}[f.lease != "none"]})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
			var a map[string]any
			json.NewDecoder(r.Body).Decode(&a)
			f.anfragen, f.kopf = append(f.anfragen, a), append(f.kopf, r.Header.Clone())
			if f.status != http.StatusOK {
				jsonAus(w, f.status, map[string]any{"error": map[string]string{"message": "vergeben"}})
				return
			}
			msgs := a["messages"].([]any)
			frage := msgs[1].(map[string]any)["content"].(string)
			jsonAus(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": f.antwort(frage)}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f, &Modell{URL: f.URL + "/v1", Lease: f.URL + "/api/model-lease", Key: "modell-key", Name: "ersatz"}
}

func (f *fakeModell) gefragt() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.anfragen {
		out = append(out, a["messages"].([]any)[1].(map[string]any)["content"].(string))
	}
	return out
}

// The model path: a message with a relation word whose sender and
// recipient the phone books know goes to the box's model with who wrote
// it to whom, its text and what is known of them; the answer is read
// strictly, a sure reading becomes the pair's suggestion with the cue,
// an unsure one nothing. Each message is sent once (the vault keeps what
// the model read), none without a word or with an unknown sender; a
// lease or a refusal leaves the rest for a later round. Nothing of a
// text or an answer is logged, nothing lands outside the vault.
func TestHinweiseMitModell(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(io.MultiWriter(&logs, os.Stderr))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	ut := netzBox(t)
	ut.rad.haushalt["b.vcf"] = karteMit("b", "Ben Berg", "ADR;TYPE=HOME:;;Gartenweg 2;Saarbrücken;;66111;Deutschland\r\nTEL:0152 2222 2222\r\n")
	ut.rad.haushalt["k.vcf"] = karteMit("k", "Kim Berg", "N:Berg;Kim;;;\r\nTEL:0153 3333 3333\r\n")
	mv, api := neuesMsgvaultHinweise(t)
	mv.setzen(
		hinweisZeile("1", false, "Hallo Mama, kommst du heute?", "von", "+4915333333333"),
		hinweisZeile("1", false, "Hallo Mama, kommst du heute?", "an", "+4917000000000"),
		hinweisZeile("2", true, "Gute Nacht, Schatz", "von", "+4917000000000"),
		hinweisZeile("2", true, "Gute Nacht, Schatz", "an", "+4915222222222"),
		hinweisZeile("3", false, "Die Tomate ist rot", "von", "+4915333333333"),
		hinweisZeile("4", false, "Hallo Oma", "von", "+4915999999999"),
	)
	mo, modell := neuesModell(t)
	mo.antwort = func(frage string) string {
		if strings.Contains(frage, "Hallo Mama") {
			return `{"beziehungen":[{"x":"B","y":"A","art":"eltern","sicher":0.9}]}`
		}
		return "```json\n{\"beziehungen\":[{\"x\":\"A\",\"y\":\"B\",\"art\":\"partner\",\"sicher\":0.4}]}\n```"
	}
	ut.u.Msgvault, ut.u.Modell = api, modell
	ut.u.Runde(t.Context(), ut.innen)

	fragen := mo.gefragt()
	if len(fragen) > 0 {
		t.Logf("the prompt:\n%s", fragen[0])
	}
	if len(fragen) != 2 {
		t.Fatalf("asked %d, want 2 (no word, an unknown sender: none): %q", len(fragen), fragen)
	}
	for _, want := range []string{"A: Kim Berg (schreibt die Nachricht)", "B: Anna Berg (bekommt die Nachricht; ihr gehört dieses Archiv)",
		"Schon bekannt:", "(Telefonbuch)", "Nachname von Kim Berg: Berg", "Beziehungswörter darin: „Mama“", "Nachricht von A vom 01.10.2026:\n«Hallo Mama, kommst du heute?»"} {
		if !strings.Contains(fragen[0], want) {
			t.Errorf("the prompt lacks %q:\n%s", want, fragen[0])
		}
	}
	a := mo.anfragen[0]
	if a["model"] != "qwen-haus" || a["response_format"].(map[string]any)["type"] != "json_object" || !strings.Contains(a["messages"].([]any)[0].(map[string]any)["content"].(string), "Antworte nur mit JSON") {
		t.Fatalf("the request: %v", a)
	}
	if k := mo.kopf[0]; k.Get("Authorization") != "Bearer modell-key" || k.Get("X-Laib-Background") == "" {
		t.Fatalf("headers: %v", k)
	}
	n := ut.u.Netz()
	fs := n.Fragen()
	if fs[0].X.Name != "Kim Berg" || !fs[0].Ich || fs[0].Vorschlag != "kind" || fs[0].Weil != "Kim Berg dir „Hallo Mama, kommst du“ schreibt" {
		t.Fatalf("Kim: %+v", fs[0])
	}
	if _, da := n.hinweise[paarKey("p-anna", "p-ben")]; da {
		t.Fatal("an unsure reading became a suggestion")
	}
	gs := hinweiseLaden(ut.innen)
	if len(gs) != 2 || gs["1"].Zitat != "Hallo Mama, kommst du" || len(gs["2"].Hinweise) != 1 {
		t.Fatalf("the vault keeps: %+v", gs)
	}
	if fi, err := os.Stat(ut.innen.umfeldHinweise()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}

	ut.u.Runde(t.Context(), ut.innen)
	if len(mo.gefragt()) != 2 {
		t.Fatal("a message sent twice")
	}

	mv.setzen(hinweisZeile("5", false, "Bis morgen, Mama", "von", "+4915333333333"))
	mo.mu.Lock()
	mo.lease = "ready"
	mo.mu.Unlock()
	ut.u.Runde(t.Context(), ut.innen)
	if len(mo.gefragt()) != 2 || len(hinweiseLaden(ut.innen)) != 2 {
		t.Fatal("asked while a lease holds the card")
	}
	mo.mu.Lock()
	mo.lease, mo.status = "none", http.StatusConflict
	mo.mu.Unlock()
	ut.u.Runde(t.Context(), ut.innen)
	if len(mo.gefragt()) != 3 || len(hinweiseLaden(ut.innen)) != 2 {
		t.Fatal("a refused message was kept as read")
	}
	mo.mu.Lock()
	mo.status = http.StatusOK
	mo.mu.Unlock()
	ut.u.Runde(t.Context(), ut.innen)
	if len(mo.gefragt()) != 4 || len(hinweiseLaden(ut.innen)) != 3 {
		t.Fatalf("not tried again: %d asked", len(mo.gefragt()))
	}

	for _, geheim := range []string{"Mama", "kommst", "Schatz", "Tomate", "beziehungen", "Kim Berg"} {
		if strings.Contains(logs.String(), geheim) {
			t.Errorf("the log carries %q:\n%s", geheim, logs.String())
		}
	}
	filepath.WalkDir(ut.daten, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("outside the vault: %s", p)
		}
		return nil
	})
}

// Without the box's model nothing is read or sent; the rest of Umfeld works.
func TestHinweiseOhneModell(t *testing.T) {
	ut := netzBox(t)
	mv, api := neuesMsgvaultHinweise(t)
	mv.setzen(hinweisZeile("1", false, "Hallo Mama", "von", "+4915333333333"))
	ut.u.Msgvault = api
	ut.u.Runde(t.Context(), ut.innen)
	if _, err := os.Stat(ut.innen.umfeldHinweise()); err == nil {
		t.Fatal("read without a model")
	}
	if modellAus(func(string) string { return "" }) != nil || modellAus(umgebung{"MODEL_URL": "ftp://x"}.get) != nil {
		t.Fatal("a model from nothing")
	}
	m := modellAus(umgebung{"MODEL_URL": "http://host.containers.internal:11435/v1/", "MODEL_LEASE": "http://host.containers.internal:11435/api/model-lease", "MODEL_KEY": "k", "MODEL_NAME": "qwen"}.get)
	if m == nil || m.URL != "http://host.containers.internal:11435/v1" || m.Name != "qwen" {
		t.Fatalf("%+v", m)
	}
}

type umgebung map[string]string

func (u umgebung) get(k string) string { return u[k] }

// The model's answer is read strictly: one object, nothing after it; a
// relation needs two different known letters, a known kind and a
// sureness from 0 to 1; a fence around it is fine.
func TestHinweisAntwort(t *testing.T) {
	b := map[string]string{"A": "p-kim", "B": "p-anna"}
	for _, c := range []struct {
		antwort string
		n       int
	}{
		{`{"beziehungen":[{"x":"B","y":"A","art":"eltern","sicher":0.9}]}`, 1},
		{"```json\n{\"beziehungen\":[{\"x\":\"b\",\"y\":\"a\",\"art\":\"Eltern\",\"sicher\":1}]}\n```", 1},
		{`{"beziehungen":[]}`, 0},
		{`{"beziehungen":[{"x":"B","y":"A","art":"eltern","sicher":0.9}]} Gern geschehen!`, 0},
		{`Die beiden sind Mutter und Kind.`, 0},
		{`{"beziehungen":[{"x":"Z","y":"A","art":"eltern","sicher":0.9}, {"x":"A","y":"A","art":"kind","sicher":0.9}, {"x":"A","y":"B","art":"erzfeind","sicher":0.9}, {"x":"A","y":"B","art":"kind","sicher":1.5}, {"x":"A","y":"B","art":"kind"}]}`, 0},
		{`{"relations":[{"x":"B","y":"A","art":"eltern","sicher":0.9}]}`, 0},
	} {
		if got := hinweisAntwort(c.antwort, b); len(got) != c.n {
			t.Errorf("%q: %+v", c.antwort, got)
		}
	}
}

// Readings add up per pair and kind from either side; a tie is no
// suggestion; a family kind makes the pair a question without a tie, the
// cue as its reason.
func TestHinweiseZusammen(t *testing.T) {
	jetzt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	st := netzTabelle{Namen: map[string]string{}, Ich: "haushalt/a",
		Kontakte: []netzKontakt{{Key: "haushalt/a", Name: "Anna Berg"}, {Key: "haushalt/h", Name: "Hedwig Dopp"}, {Key: "haushalt/u", Name: "Uwe Dopp"}, {Key: "haushalt/f", Name: "Fritz Kurz"}}}
	n := neuesNetz(st, nil, nil, time.UTC, func() time.Time { return jetzt })
	n.hinweiseSetzen(map[string]gelesen{
		"1": {Von: "haushalt/a", An: []string{"haushalt/h"}, Zitat: "Hallo Mama", Hinweise: []modellHinweis{{X: "haushalt/h", Y: "haushalt/a", Art: "eltern", Sicher: 0.8}}},
		"2": {Von: "haushalt/h", An: []string{"haushalt/a"}, Zitat: "mein Kind", Hinweise: []modellHinweis{{X: "haushalt/a", Y: "haushalt/h", Art: "kind", Sicher: 0.75}}},
		"3": {Von: "haushalt/h", An: []string{"haushalt/a"}, Zitat: "Schatz", Hinweise: []modellHinweis{{X: "haushalt/h", Y: "haushalt/a", Art: "partner", Sicher: 0.95}}},
		"4": {Von: "haushalt/u", An: []string{"haushalt/f"}, Zitat: "Bruder", Hinweise: []modellHinweis{{X: "haushalt/u", Y: "haushalt/f", Art: "geschwister", Sicher: 0.8}}},
		"5": {Von: "haushalt/u", An: []string{"haushalt/f"}, Zitat: "Schatz", Hinweise: []modellHinweis{{X: "haushalt/u", Y: "haushalt/f", Art: "partner", Sicher: 0.8}}},
	})
	got := fragenVon(n.Fragen())
	if !slices.Equal(got, []string{"Hedwig Dopp>Anna Berg=eltern"}) {
		t.Fatalf("questions: %v", got)
	}
	if f := n.Fragen()[0]; f.Weil != "du Hedwig Dopp „Hallo Mama“ schreibst" {
		t.Fatalf("the cue: %q", f.Weil)
	}
}

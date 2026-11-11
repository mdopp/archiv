package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHomes is Homes' /intern/uebernahme (homes internal/uebernahme): it
// checks each packet as Homes does and keeps what it took.
type fakeHomes struct {
	*httptest.Server
	mu      sync.Mutex
	pakete  []uebernahmePaket
	kaputt  bool
	header  []http.Header
	nonces  map[string]bool
	geheim  string
	falsche int
}

func neuesHomes(t *testing.T) *fakeHomes {
	f := &fakeHomes{geheim: "homes-geheim", nonces: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		d, s := r.PostFormValue("d"), r.PostFormValue("s")
		m := hmac.New(sha256.New, []byte(f.geheim))
		m.Write([]byte("archiv-uebernahme-2." + d))
		var p uebernahmePaket
		b, _ := base64.RawURLEncoding.DecodeString(d)
		if r.Method != "POST" || r.URL.Path != "/intern/uebernahme" || s != base64.RawURLEncoding.EncodeToString(m.Sum(nil)) || json.Unmarshal(b, &p) != nil || f.nonces[p.Nonce] {
			f.falsche++
			http.Error(rw, `{"ok":false}`, http.StatusUnauthorized)
			return
		}
		if f.kaputt {
			http.Error(rw, `{"ok":false,"grund":"ins Adressbuch ließ sich nicht schreiben"}`, http.StatusBadGateway)
			return
		}
		f.nonces[p.Nonce] = true
		f.pakete = append(f.pakete, p)
		f.header = append(f.header, r.Header.Clone())
		rw.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeHomes) genommen() []uebernahmePaket {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uebernahmePaket(nil), f.pakete...)
}

// eintragenBox is the open archive with netzBox's Umfeld and a fake Homes.
func eintragenBox(t *testing.T) (*webTest, *umfeldTest, *fakeHomes, Innen) {
	ut := netzBox(t)
	wt := neuesWeb(t)
	wt.oeffnen(t)
	innen, _ := wt.tt.Innen()
	for _, d := range []string{"umfeld", "kontakte"} {
		os.MkdirAll(filepath.Join(innen.Wurzel, d), 0o700)
	}
	for _, p := range [][2]string{{ut.innen.umfeldTabelle(), innen.umfeldTabelle()}, {kontakteDatei(ut.innen), kontakteDatei(innen)}, {ut.innen.ImmichSchluessel(), innen.ImmichSchluessel()}} {
		b, _ := os.ReadFile(p[0])
		os.WriteFile(p[1], b, 0o600)
	}
	ut.u.Geaendert = nil
	homes := neuesHomes(t)
	wt.w.Umfeld, wt.w.HomesIntern, wt.w.Now = ut.u, homes.URL, func() time.Time { return *ut.jetzt }
	ut.u.laden(innen)
	return wt, ut, homes, innen
}

// folgen is the browser after a form: the 303 and the page it leads to.
func (wt *webTest) folgen(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("not a 303: %d\n%s", rec.Code, rec.Body)
	}
	ziel := rec.Header().Get("Location")
	return ziel, wt.rufe("GET", ziel, anna, nil).Body.String()
}

// One tap (owner 2026-11-10): the main page asks one question, its kinds
// as chips with the suggestion marked, and says where the answer goes. A
// chip is a plain form post (no script needed): it saves the answer in the
// vault and Homes writes it at once, server to server, signed for anna,
// with what follows from it by definition (owner 2026-11-11); the page
// comes back with the next question and „✓ … eingetragen · daraus: …
// · Rückgängig“; one Rückgängig takes all of it back from Homes and the
// vault. The tab counts only the visit's portion, never the backlog;
// Später puts a question behind it; when it is done, „Alles nachgebessert
// für jetzt“ and a quiet „weitere Fragen“.
func TestEinTipp(t *testing.T) {
	wt, _, homes, innen := eintragenBox(t)

	page := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	for _, want := range []string{`<h1>Dein Archiv</h1>`, `action="/tresor/abschliessen"`, `action="/tresor/suche"`, `aria-current=page>Fragen <span class=anzahl>4</span>`,
		`href="/tresor?tab=nahe"`, `href="/tresor?tab=stammbaum"`, `class="frage sofort"`, "Kim Berg ist für dich …", `name=art value=fremd class="chip"`,
		"kennen sich nicht", "<summary>mehr …</summary>", `name=art value=expartner`, "kommt nur auf dein Handy",
		"Später", "noch 4 zum Nachbessern", `<a class=quellen id=quellen-zeile data-live href="/tresor/quellen">`} {
		if !strings.Contains(page, want) {
			t.Fatalf("%q missing:\n%s", want, page)
		}
	}
	if strings.Contains(page, "Signal-Passphrase (30") || strings.Contains(page, "Fritz Kurz ist für") || strings.Contains(page, "offen</span>") {
		t.Fatal("the main page shows a source's setup, more than one question, or the backlog")
	}

	// a phone contact: only the phone, and what follows by definition with it (Fritz, Anna's sibling, is Kim's uncle)
	ziel, page := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"kind"}, "zurueck": {"/tresor"}}))
	if !strings.HasPrefix(ziel, "/tresor?") || !strings.Contains(ziel, "tab=fragen") ||
		!strings.Contains(page, "✓ Kim Berg ist Kind von dir · eingetragen, kommt auf dein Handy · daraus: weil du und Fritz Kurz Geschwister seid und Kim Berg dein Kind ist, ist Fritz Kurz Onkel/Tante von Kim Berg") ||
		!strings.Contains(page, `<button name=tu value=vergessen class=leise data-laeuft="Nimmt zurück …">Rückgängig</button>`) || !strings.Contains(page, "Ben Berg ist für Kim Berg …") ||
		!strings.Contains(page, "<span class=anzahl>2</span>") || !strings.Contains(page, "bleibt in deinem Archiv: Kim Berg steht in keinem Adressbuch, nur auf deinen Fotos") {
		t.Fatalf("after the tap: %s\n%s", ziel, page)
	}
	ps := homes.genommen()
	if len(ps) != 2 || ps[0].Person != "anna" || ps[0].Weg || len(ps[0].Nonce) != 32 || ps[0].Zeilen[0] != (uebernahmeZeile{X: uebernahmeEnde{Name: "Kim Berg"}, Y: uebernahmeEnde{Name: "Anna Berg", Handy: "k1"}, Art: "kind"}) ||
		ps[1].Zeilen[0] != (uebernahmeZeile{X: uebernahmeEnde{Name: "Fritz Kurz", Handy: "urn:uuid:k2"}, Y: uebernahmeEnde{Name: "Kim Berg"}, Art: "onkel"}) {
		t.Fatalf("Homes got: %+v", ps)
	}
	if h := homes.header[0]; h.Get(proxyHeader) != "" || h.Get("Remote-User") != "" || h.Get("Cookie") != "" {
		t.Fatalf("the archive sent a browser's or the proxy's headers: %v", h)
	}
	if b, _ := os.ReadFile(innen.umfeldAntworten()); !strings.Contains(string(b), `"art":"onkel"`) || !strings.Contains(string(b), `"aus":"p-anna|p-kim"`) || !strings.Contains(string(b), `"eingetragen":{"wohin":"handy"`) {
		t.Fatalf("the vault: %s", b)
	}

	// one Rückgängig: Homes takes both lines back, the vault forgets both, the question is back
	ziel, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {paarKey("p-kim", "p-anna")}, "tu": {"vergessen"}, "zurueck": {"/tresor"}}))
	if ps := homes.genommen(); len(ps) != 4 || !ps[2].Weg || !ps[3].Weg || !slices.ContainsFunc(ps[2:], func(p uebernahmePaket) bool { return p.Zeilen[0] == ps[0].Zeilen[0] }) ||
		!slices.ContainsFunc(ps[2:], func(p uebernahmePaket) bool { return p.Zeilen[0] == ps[1].Zeilen[0] }) {
		t.Fatalf("Rückgängig at Homes: %+v", ps)
	}
	if !strings.Contains(page, "Rückgängig gemacht. Die Frage kommt wieder.") || !strings.Contains(page, "<span class=anzahl>4</span>") {
		t.Fatalf("after Rückgängig: %s\n%s", ziel, page)
	}
	if b, _ := os.ReadFile(innen.umfeldAntworten()); strings.Contains(string(b), "kind") || strings.Contains(string(b), "onkel") {
		t.Fatalf("still in the vault: %s", b)
	}

	// Später: nothing to Homes, the question waits behind the portion, the next comes
	ziel, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"spaeter"}, "zurueck": {"/tresor"}}))
	if ziel != "/tresor?tab=fragen" || !strings.Contains(page, "Fritz Kurz ist für Ben Berg …") || !strings.Contains(page, "<span class=anzahl>3</span>") || strings.Contains(page, "Kim Berg ist für") ||
		!strings.Contains(page, "Vorschlag, weil du und Fritz Kurz Geschwister seid und du und Ben Berg Partner seid.") {
		t.Fatalf("Später: %s\n%s", ziel, page)
	}
	_, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-fritz"}, "y": {"p-ben"}, "art": {"schwager"}, "zurueck": {"/tresor"}}))
	if len(homes.genommen()) != 5 || !strings.Contains(page, "✓ Fritz Kurz ist Schwager/Schwägerin von Ben Berg · eingetragen, kommt auf dein Handy") || !strings.Contains(page, "Fritz Kurz ist für Kim Berg …") {
		t.Fatalf("Schwager:\n%s", page)
	}

	// kennen sich nicht, keine Beziehung: nothing to Homes; the portion is done, calmly, and nothing counts on
	_, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-fritz"}, "y": {"p-kim"}, "art": {"fremd"}, "zurueck": {"/tresor"}}))
	if !strings.Contains(page, "✓ Fritz Kurz und Kim Berg kennen sich nicht · gemerkt") || !strings.Contains(page, "die letzte zum Nachbessern") {
		t.Fatalf("fremd:\n%s", page)
	}
	_, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-ben"}, "y": {"p-kim"}, "art": {"keine"}, "zurueck": {"/tresor"}}))
	if !strings.Contains(page, esc(saysNachgebessert)) || !strings.Contains(page, `<a class=leise href="/tresor?tab=fragen&amp;mehr=1">weitere Fragen</a>`) ||
		strings.Contains(page, "class=anzahl") || strings.Contains(page, `class="frage sofort"`) {
		t.Fatalf("the portion done:\n%s", page)
	}

	// weitere Fragen: one beyond the portion, with a way out instead of a count; the answer goes on there
	page = wt.rufe("GET", "/tresor?tab=fragen&mehr=1", anna, nil).Body.String()
	if !strings.Contains(page, "Kim Berg ist für dich …") || !strings.Contains(page, `<input type=hidden name=mehr value=1>`) || !strings.Contains(page, ">Genug für jetzt</a>") ||
		strings.Contains(page, "class=anzahl") || strings.Contains(page, "zum Nachbessern</span>") {
		t.Fatalf("weitere Fragen:\n%s", page)
	}
	ziel, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"kind"}, "mehr": {"1"}, "zurueck": {"/tresor"}}))
	if !strings.Contains(ziel, "mehr=1") || len(homes.genommen()) != 6 || !strings.Contains(page, "✓ Kim Berg ist Kind von dir · eingetragen, kommt auf dein Handy</span>") || !strings.Contains(page, esc(saysKeineFragen)) {
		t.Fatalf("beyond the portion (Fritz and Kim answered „kennen sich nicht“: no uncle follows): %s %d\n%s", ziel, len(homes.genommen()), page)
	}

	if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "zurueck": {"/tresor"}}); rec.Code != 400 || !strings.Contains(rec.Body.String(), esc(saysFrageOhneWahl)) {
		t.Fatalf("no chip: %d", rec.Code)
	}
	for _, x := range []string{"p-gerd", "haushalt/c"} { // one photo day; the same firm only: they know each other, no question
		if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"x": {x}, "y": {"p-anna"}, "art": {"keine"}}); rec.Code != 409 {
			t.Fatalf("%s not asked: %d", x, rec.Code)
		}
	}
	if len(homes.genommen()) != 6 || homes.falsche != 0 {
		t.Fatalf("Homes: %d taken, %d refused", len(homes.genommen()), homes.falsche)
	}
	wt.tt.Abschliessen(t.Context(), "test")
	if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"kind"}}); rec.Code != 423 {
		t.Fatalf("closed: %d", rec.Code)
	}
}

// A visit is a portion (owner 2026-11-11): what was put back (Später)
// waits for the next visit — coming to the page from outside, not the
// archive's own links, answers or live updates.
func TestBesuch(t *testing.T) {
	wt, ut, _, _ := eintragenBox(t)
	draussen := maps.Clone(anna)
	draussen["Sec-Fetch-Mode"], draussen["Sec-Fetch-Site"] = "navigate", "none"
	innen := maps.Clone(draussen)
	innen["Sec-Fetch-Site"] = "same-origin"
	wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"spaeter"}, "zurueck": {"/tresor"}}))
	*ut.jetzt = ut.jetzt.Add(time.Minute)
	for _, hdr := range []map[string]string{anna, innen} {
		if page := wt.rufe("GET", "/tresor?tab=fragen", hdr, nil).Body.String(); !strings.Contains(page, "<span class=anzahl>3</span>") || strings.Contains(page, "Kim Berg ist für dich") {
			t.Fatalf("the same visit:\n%s", page)
		}
	}
	if page := wt.rufe("GET", "/tresor", draussen, nil).Body.String(); !strings.Contains(page, "<span class=anzahl>4</span>") || !strings.Contains(page, "noch 4 zum Nachbessern") {
		t.Fatalf("a new visit:\n%s", page)
	}
	u := &Umfeld{Now: func() time.Time { return *ut.jetzt }}
	erst := u.Besuch(false)
	*ut.jetzt = ut.jetzt.Add(time.Hour)
	if u.Besuch(false) != erst || u.Besuch(true) == erst {
		t.Fatal("a visit lasts until a new one")
	}
	u.vergessen()
	if u.Besuch(false) != *ut.jetzt {
		t.Fatal("the vault closed: the next page begins a visit")
	}
}

// Homes down: the answer stays in the vault, the toast says it is not
// written, one line outside the folded „Deine Antworten“ offers „erneut
// versuchen“, which writes all of them once Homes is back; a Rückgängig Homes did not take leaves both as they were. Without
// Homes the card says the answer stays in the archive for now.
func TestEinTippOhneHomes(t *testing.T) {
	wt, _, homes, innen := eintragenBox(t)
	key := paarKey("p-kim", "p-anna")
	homes.kaputt = true
	_, page := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"kind"}, "zurueck": {"/tresor"}}))
	if !strings.Contains(page, "✓ Kim Berg ist Kind von dir · noch nicht eingetragen: Homes antwortet gerade nicht · daraus:") {
		t.Fatalf("Homes down:\n%s", page)
	}
	if b, _ := os.ReadFile(innen.umfeldAntworten()); !strings.Contains(string(b), "kind") || strings.Contains(string(b), "eingetragen") {
		t.Fatalf("the vault: %s", b)
	}
	umfeld := wt.rufe("GET", "/tresor/umfeld", anna, nil).Body.String()
	nachtrag := strings.Index(umfeld, "2 Antworten noch nicht eingetragen ·</span><button name=tu value=alle-eintragen")
	falte := strings.Index(umfeld, "<details id=antworten class=karte><summary>Deine Antworten (2)</summary>")
	if nachtrag < 0 || falte < 0 || nachtrag > falte || strings.Contains(umfeld, "<details id=antworten class=karte open") || !strings.Contains(umfeld, "2 Fragen zum Nachbessern") {
		t.Fatalf("Deine Antworten folded, the retry one line outside (owner 2026-11-11):\n%s", umfeld)
	}
	if strings.Contains(umfeld, "value=eintragen") || strings.Count(umfeld, "noch nicht eingetragen") != 3 { // the line and the two answers' state inside the fold
		t.Fatalf("one retry line only:\n%s", umfeld)
	}
	if i, j := strings.Index(umfeld, "Kim Berg ist Kind von dir."), strings.Index(umfeld, "Fritz Kurz ist Onkel/Tante von Kim Berg."); i < 0 || j < 0 {
		t.Fatalf("the answers inside:\n%s", umfeld)
	}
	if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"tu": {"alle-eintragen"}, "zurueck": {"/tresor/umfeld"}}); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), esc(saysNichtEin)) {
		t.Fatalf("erneut versuchen while down: %d", rec.Code)
	}
	if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {key}, "tu": {"eintragen"}, "zurueck": {"/tresor/umfeld"}}); rec.Code != http.StatusBadGateway {
		t.Fatalf("Eintragen of one while down: %d", rec.Code)
	}
	homes.kaputt = false
	ziel, umfeld := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"tu": {"alle-eintragen"}, "zurueck": {"/tresor/umfeld"}}))
	if !strings.HasPrefix(ziel, "/tresor/umfeld") || len(homes.genommen()) != 2 || strings.Count(umfeld, ">eingetragen, kommt auf dein Handy</span>") != 2 {
		t.Fatalf("erneut versuchen: %s %d\n%s", ziel, len(homes.genommen()), umfeld)
	}
	if umfeld := wt.rufe("GET", "/tresor/umfeld", anna, nil).Body.String(); strings.Contains(umfeld, "alle-eintragen") || strings.Contains(umfeld, "noch nicht eingetragen") {
		t.Fatal("still offered after both were written")
	}
	homes.kaputt = true
	if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {key}, "tu": {"vergessen"}, "zurueck": {"/tresor"}}); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), esc(saysNichtZurueck)) {
		t.Fatalf("Rückgängig while down: %d", rec.Code)
	}
	if b, _ := os.ReadFile(innen.umfeldAntworten()); !strings.Contains(string(b), "kind") || !strings.Contains(string(b), "onkel") {
		t.Fatalf("forgotten though Homes kept it: %s", b)
	}
	wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-ben"}, "y": {"p-kim"}, "art": {"spaeter"}, "zurueck": {"/tresor"}}))
	wt.w.HomesIntern = ""
	if page := wt.rufe("GET", "/tresor", anna, nil).Body.String(); !strings.Contains(page, "Fritz Kurz ist für Ben Berg …") || !strings.Contains(page, "bleibt vorerst in deinem Archiv: Homes ist noch nicht mit ihm verbunden") {
		t.Fatalf("without Homes:\n%s", page)
	}
}

// The packet is what Homes checks (homes internal/uebernahme): version 2,
// signed with the archive's secret, for its person, made now, a new nonce
// each time.
func TestUebernahmePaket(t *testing.T) {
	jetzt := time.Date(2026, 11, 10, 9, 40, 0, 0, time.UTC)
	z := uebernahmeZeile{X: uebernahmeEnde{Name: "Carla Wolf", Haushalt: "c"}, Y: uebernahmeEnde{Name: "Anna Berg", Haushalt: "a"}, Art: "kollege"}
	d, s := uebernahmeSignieren("homes-geheim", "anna", jetzt, true, z)
	m := hmac.New(sha256.New, []byte("homes-geheim"))
	m.Write([]byte("archiv-uebernahme-2." + d))
	if s != base64.RawURLEncoding.EncodeToString(m.Sum(nil)) {
		t.Fatal("not signed as Homes checks")
	}
	b, _ := base64.RawURLEncoding.DecodeString(d)
	var p uebernahmePaket
	if err := json.Unmarshal(b, &p); err != nil || p.Person != "anna" || !p.Am.Equal(jetzt) || !p.Weg || len(p.Zeilen) != 1 || p.Zeilen[0] != z {
		t.Fatalf("%s %v", b, err)
	}
	if !strings.Contains(string(b), `"x":{"name":"Carla Wolf","haushalt":"c"}`) || !strings.Contains(string(b), `"am":"2026-11-10T09:40:00Z"`) {
		t.Fatalf("the contract's names: %s", b)
	}
	if d2, _ := uebernahmeSignieren("homes-geheim", "anna", jetzt, true, z); d2 == d {
		t.Fatal("the same nonce twice")
	}
	for env, want := range map[string]string{"http://host.containers.internal:8650": "http://host.containers.internal:8650", "http://host.containers.internal:8650/x": "http://host.containers.internal:8650",
		"": "", "ftp://x": "", "host.containers.internal:8650": ""} {
		if got := homesIntern(func(string) string { return env }); got != want {
			t.Errorf("%q: %q", env, got)
		}
	}
}

// The workplace: Steht nahe as rows with faces and bars, the Stammbaum's
// place, the sources as one line with their page, the search into
// msgvault's own page; all fits a phone (no fixed width over 411 px).
func TestArbeitsplatz(t *testing.T) {
	wt, _, _, _ := eintragenBox(t)
	nahe := wt.rufe("GET", "/tresor?tab=nahe", anna, nil).Body.String()
	for _, want := range []string{`aria-current=page>Steht nahe</a>`, "<h2>Steht dir nahe</h2>", `<ul class=leute>`, `class=balken><b style="width:100%"></b>`, `href="/tresor/umfeld?mitte=p-ben"`} {
		if !strings.Contains(nahe, want) {
			t.Fatalf("Steht nahe lacks %q:\n%s", want, nahe)
		}
	}
	if strings.Contains(nahe, `class="frage sofort"`) {
		t.Fatal("a question on Steht nahe")
	}
	if baum := wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String(); !strings.Contains(baum, `<div class=baum id=stammbaum><svg`) ||
		!strings.Contains(baum, `href="/tresor/kante?a=p-anna&amp;b=p-ben" aria-label="Du und Ben Berg: Partner/in"`) {
		t.Fatalf("the Stammbaum:\n%s", baum)
	}
	quellen := wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String()
	for _, want := range []string{"<h1>Quellen</h1>", `href="/tresor">‹ Dein Archiv</a>`, "Signal-Passphrase (30", "Postfach eintragen", "SMS und Anrufe", "64-stelligen Schlüssel", "Kontakte und Kalender", "Immich-Schlüssel", "Passkeys", "Facebook und LinkedIn", "Eine E-Mail-Weiterleitung geht hier nicht", "Ins Archiv"} {
		if !strings.Contains(quellen, want) {
			t.Fatalf("Quellen lacks %q", want)
		}
	}
	if suche := wt.rufe("GET", "/tresor/suche?q=Oma+Hilde", anna, nil).Body.String(); !strings.Contains(suche, `href="/?explore=%7B%22query%22%3A%22Oma+Hilde%22%2C%22schemaVersion%22%3A1%7D&amp;mode=full_text&amp;workspace=everything">In allen Nachrichten nach „Oma Hilde“ suchen`) ||
		strings.Contains(suche, "Auch „") {
		t.Fatalf("search:\n%s", suche)
	}
	if strings.Contains(css, "width:4") && !strings.Contains(css, "max-width:40rem") {
		t.Fatal("a fixed width")
	}
	for _, want := range []string{"max-width:100%", "overflow-wrap:anywhere", "prefers-color-scheme:light", "@keyframes weg"} {
		if !strings.Contains(css, want) {
			t.Fatalf("css lacks %q", want)
		}
	}
	if !strings.Contains(skript, "classList.contains('sofort')") || !strings.Contains(skript, "history.replaceState") {
		t.Fatal("the script does not send a chip in the background")
	}
}

// Umlauts (owner 2026-11-10: „Juergen Burger“ was not found with
// „Jürgen“): the search finds a contact whichever way its name or the
// words spell them, and in any case; it offers the other spelling for the
// messages too. The contact match of a re-keyed card does the same.
func TestJuergenIstJuergen(t *testing.T) {
	wt, _, _, innen := eintragenBox(t)
	k, _ := kontakteLaden(kontakteDatei(innen))
	k.Einmischen([]QuellKarte{{Href: "j1.vcf", Daten: karteMit("j1", "Juergen Burger", "TEL:+4915112345678\r\n")},
		{Href: "j2.vcf", Daten: karteMit("j2", "Jürgen Weiß", "TEL:+4915187654321\r\n")}}, time.Now())
	if err := k.speichern(kontakteDatei(innen)); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string][]string{
		"Jürgen":        {"Juergen Burger", "Jürgen Weiß"},
		"juergen":       {"Juergen Burger", "Jürgen Weiß"},
		"JÜRGEN burger": {"Juergen Burger"},
		"Juergen Weiss": {"Jürgen Weiß"},
		"weiß":          {"Jürgen Weiß"},
	} {
		if got := kontakteFinden(innen, q, 20); !slices.Equal(got, want) {
			t.Errorf("%q: %v", q, got)
		}
	}
	suche := wt.rufe("GET", "/tresor/suche?q=J%C3%BCrgen", anna, nil).Body.String()
	if !strings.Contains(suche, "<h2>Kontakte</h2>") || !strings.Contains(suche, "Juergen Burger<span class=k>") || !strings.Contains(suche, "Auch „Juergen“ suchen") {
		t.Fatalf("search for Jürgen:\n%s", suche)
	}
	if !gleicherKontakt(karteMit("a", "Jürgen Burger", "TEL:0151 12345678\r\n"), karteMit("b", "JUERGEN BURGER", "TEL:+49 151 12345678\r\n")) {
		t.Fatal("the contact match tells Jürgen from Juergen")
	}
}

// The Kranz family in one tap (owner 2026-11-11): Hedwig is Uwe's
// mother; with it Homes writes that she is Silke's mother-in-law — all
// three household cards — and Lena's grandmother, who is only on Uwe's
// phone, so that line goes only to the phone. The toast shows the chain;
// one Rückgängig takes all three back.
func TestKetteEinTipp(t *testing.T) {
	wt, ut, homes, innen := eintragenBox(t)
	tab := netzTabelle{Format: netzFormat, Version: netzVersion, Namen: map[string]string{}, Ich: "haushalt/a",
		Kontakte: []netzKontakt{{Key: "haushalt/a", Name: "Anna Berg"}, {Key: "haushalt/h", Name: "Hedwig Kranz"}, {Key: "haushalt/u", Name: "Uwe Kranz"},
			{Key: "haushalt/s", Name: "Silke Kranz"}, {Key: "privat/l", Name: "Lena Kranz"}},
		Fest: []festBand{{A: "haushalt/u", B: "haushalt/s", Art: bandBeziehung, Wie: beziehungsWort("Ehefrau")}, {A: "haushalt/u", B: "privat/l", Art: bandBeziehung, Wie: beziehungsWort("Tochter")},
			{A: "haushalt/h", B: "haushalt/u", Art: bandAdresse}}}
	if err := jsonImTresor(innen.umfeldTabelle(), tab); err != nil {
		t.Fatal(err)
	}
	ut.u.laden(innen)
	ziel, page := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"haushalt/h"}, "y": {"haushalt/u"}, "art": {"eltern"}, "zurueck": {"/tresor"}}))
	want := "✓ Hedwig Kranz ist Mutter/Vater von Uwe Kranz · eingetragen" +
		" · daraus: weil Uwe Kranz Hedwig Kranz’ Kind ist und Uwe Kranz und Silke Kranz Partner sind, ist Hedwig Kranz Schwiegereltern von Silke Kranz" +
		" · daraus: weil Uwe Kranz Hedwig Kranz’ Kind ist und Lena Kranz Uwe Kranz’ Kind ist, ist Hedwig Kranz Großeltern von Lena Kranz"
	if !strings.Contains(page, esc(want)) {
		t.Fatalf("the chain's toast: %s\n%s", ziel, page)
	}
	ps := homes.genommen()
	var zeilen []uebernahmeZeile
	for _, p := range ps {
		zeilen = append(zeilen, p.Zeilen...)
	}
	h := uebernahmeEnde{Name: "Hedwig Kranz", Haushalt: "h"}
	for _, z := range []uebernahmeZeile{
		{X: h, Y: uebernahmeEnde{Name: "Uwe Kranz", Haushalt: "u"}, Art: "eltern"},
		{X: h, Y: uebernahmeEnde{Name: "Silke Kranz", Haushalt: "s"}, Art: "schwiegereltern"},
		{X: uebernahmeEnde{Name: "Hedwig Kranz"}, Y: uebernahmeEnde{Name: "Lena Kranz", Handy: "l"}, Art: "grosseltern"}, // the household card never names Lena
	} {
		if !slices.Contains(zeilen, z) {
			t.Errorf("Homes did not get %+v: %+v", z, zeilen)
		}
	}
	if len(zeilen) != 3 {
		t.Fatalf("Homes got %d lines", len(zeilen))
	}
	wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {paarKey("haushalt/h", "haushalt/u")}, "tu": {"vergessen"}, "zurueck": {"/tresor"}}))
	ps = homes.genommen()
	if len(ps) != 6 || !ps[3].Weg || !ps[4].Weg || !ps[5].Weg {
		t.Fatalf("one Rückgängig, the whole chain at Homes: %+v", ps)
	}
	if b, _ := os.ReadFile(innen.umfeldAntworten()); string(b) != "{}" {
		t.Fatalf("the vault keeps: %s", b)
	}
}

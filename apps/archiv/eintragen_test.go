package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
// vault and Homes writes it at once, server to server, signed for anna;
// the page comes back with the next question and „✓ … eingetragen ·
// Rückgängig“; Rückgängig takes it back from Homes and the vault. A
// person only on photos stays in the archive, and the card says so.
func TestEinTipp(t *testing.T) {
	wt, ut, homes, innen := eintragenBox(t)
	n := ut.u.Netz()
	carla := n.Von("haushalt/c")

	page := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	for _, want := range []string{`<h1>Dein Archiv</h1>`, `action="/tresor/abschliessen"`, `action="/tresor/suche"`, `aria-current=page>Fragen <span class=anzahl>5</span>`,
		`href="/tresor?tab=nahe"`, `href="/tresor?tab=stammbaum"`, `class="frage sofort"`, "Fritz Kurz ist für Ben Berg …", "Vorschlag, weil du und Fritz Kurz Geschwister seid und du und Ben Berg Partner seid.", `name=art value=fremd class="chip"`,
		"kennen sich nicht", "<summary>mehr …</summary>", `name=art value=expartner`, "kommt nur auf dein Handy", "Später", "5 Fragen offen",
		`<a class=quellen id=quellen-zeile data-live href="/tresor/quellen">`} {
		if !strings.Contains(page, want) {
			t.Fatalf("%q missing:\n%s", want, page)
		}
	}
	if strings.Contains(page, "Signal-Passphrase (30") || strings.Contains(page, "Carla Wolf ist für") {
		t.Fatal("the main page shows a source's setup, or more than one question")
	}

	// a household pair: both household cards, by UID
	ziel, page := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {carla}, "y": {"p-anna"}, "art": {"kollege"}, "zurueck": {"/tresor"}}))
	if !strings.HasPrefix(ziel, "/tresor?") || !strings.Contains(ziel, "tab=fragen") || !strings.Contains(page, "✓ Carla Wolf ist Kollege/Kollegin von dir · eingetragen") ||
		!strings.Contains(page, `<button name=tu value=vergessen class=leise data-laeuft="Nimmt zurück …">Rückgängig</button>`) || !strings.Contains(page, "Fritz Kurz ist für Ben Berg …") {
		t.Fatalf("after the tap: %s\n%s", ziel, page)
	}
	ps := homes.genommen()
	if len(ps) != 1 || ps[0].Person != "anna" || ps[0].Weg || len(ps[0].Nonce) != 32 || ps[0].Zeilen[0] != (uebernahmeZeile{X: uebernahmeEnde{Name: "Carla Wolf", Haushalt: "c"}, Y: uebernahmeEnde{Name: "Anna Berg", Haushalt: "a"}, Art: "kollege"}) {
		t.Fatalf("Homes got: %+v", ps)
	}
	if h := homes.header[0]; h.Get(proxyHeader) != "" || h.Get("Remote-User") != "" || h.Get("Cookie") != "" {
		t.Fatalf("the archive sent a browser's or the proxy's headers: %v", h)
	}
	if b, _ := os.ReadFile(innen.umfeldAntworten()); !strings.Contains(string(b), `"art":"kollege"`) || !strings.Contains(string(b), `"eingetragen":{"wohin":"haushalt"`) {
		t.Fatalf("the vault: %s", b)
	}

	// Rückgängig: Homes takes the same line back, the vault forgets, the question is back
	ziel, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {paarKey(carla, "p-anna")}, "tu": {"vergessen"}, "zurueck": {"/tresor"}}))
	if ps := homes.genommen(); len(ps) != 2 || !ps[1].Weg || ps[1].Zeilen[0] != ps[0].Zeilen[0] {
		t.Fatalf("Rückgängig at Homes: %+v", ps)
	}
	if !strings.Contains(page, "Rückgängig gemacht. Die Frage kommt wieder.") || !strings.Contains(page, "<span class=anzahl>5</span>") {
		t.Fatalf("after Rückgängig: %s\n%s", ziel, page)
	}
	if b, _ := os.ReadFile(innen.umfeldAntworten()); strings.Contains(string(b), "kollege") {
		t.Fatalf("still in the vault: %s", b)
	}

	// a phone contact: only the phone
	_, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-fritz"}, "y": {"p-kim"}, "art": {"freund"}, "zurueck": {"/tresor"}}))
	if ps := homes.genommen(); len(ps) != 3 || ps[2].Zeilen[0].X.Handy != "urn:uuid:k2" || ps[2].Zeilen[0].Y.Haushalt != "" || ps[2].Zeilen[0].X.Haushalt != "" {
		t.Fatalf("the phone's line: %+v", ps[len(ps)-1])
	}
	if !strings.Contains(page, "✓ Fritz Kurz ist Freund/in von Kim Berg · eingetragen, kommt auf dein Handy") || !strings.Contains(page, "Fritz Kurz ist für Ben Berg …") {
		t.Fatalf("after the phone's tap:\n%s", page)
	}

	// the derived question first (Fritz is Anna's sibling, Anna Ben's partner); put back, the next comes
	_, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-fritz"}, "y": {"p-ben"}, "art": {"spaeter"}, "zurueck": {"/tresor"}}))

	// only on photos: the card says it stays, and nothing goes to Homes
	if !strings.Contains(page, "bleibt in deinem Archiv: Kim Berg steht in keinem Adressbuch, nur auf deinen Fotos") {
		t.Fatalf("the photo-only card:\n%s", page)
	}
	_, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-ben"}, "y": {"p-kim"}, "art": {"eltern"}, "zurueck": {"/tresor"}}))
	if len(homes.genommen()) != 3 || !strings.Contains(page, "✓ Ben Berg ist Mutter/Vater von Kim Berg · bleibt in deinem Archiv") {
		t.Fatalf("photo-only written: %d\n%s", len(homes.genommen()), page)
	}

	// Später and kennen sich nicht: nothing to Homes
	if ziel, _ := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"spaeter"}, "zurueck": {"/tresor"}})); ziel != "/tresor?tab=fragen" {
		t.Fatalf("Später: %s", ziel)
	}
	if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "zurueck": {"/tresor"}}); rec.Code != 400 || !strings.Contains(rec.Body.String(), esc(saysFrageOhneWahl)) {
		t.Fatalf("no chip: %d", rec.Code)
	}
	if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-gerd"}, "y": {"p-anna"}, "art": {"keine"}}); rec.Code != 409 {
		t.Fatalf("not asked: %d", rec.Code)
	}
	if len(homes.genommen()) != 3 || homes.falsche != 0 {
		t.Fatalf("Homes: %d taken, %d refused", len(homes.genommen()), homes.falsche)
	}
	wt.tt.Abschliessen(t.Context(), "test")
	if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-kim"}, "y": {"p-anna"}, "art": {"kind"}}); rec.Code != 423 {
		t.Fatalf("closed: %d", rec.Code)
	}
}

// Homes down: the answer stays in the vault, the toast says it is not
// written, „Deine Antworten“ offers Eintragen, which writes once Homes is
// back; a Rückgängig Homes did not take leaves both as they were. Without
// Homes the card says the answer stays in the archive for now.
func TestEinTippOhneHomes(t *testing.T) {
	wt, ut, homes, innen := eintragenBox(t)
	carla := ut.u.Netz().Von("haushalt/c")
	key := paarKey(carla, "p-anna")
	homes.kaputt = true
	_, page := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {carla}, "y": {"p-anna"}, "art": {"kollege"}, "zurueck": {"/tresor"}}))
	if !strings.Contains(page, "✓ Carla Wolf ist Kollege/Kollegin von dir · noch nicht eingetragen: Homes antwortet gerade nicht") {
		t.Fatalf("Homes down:\n%s", page)
	}
	if b, _ := os.ReadFile(innen.umfeldAntworten()); !strings.Contains(string(b), "kollege") || strings.Contains(string(b), "eingetragen") {
		t.Fatalf("the vault: %s", b)
	}
	umfeld := wt.rufe("GET", "/tresor/umfeld", anna, nil).Body.String()
	if !strings.Contains(umfeld, "<h2>Deine Antworten</h2>") || !strings.Contains(umfeld, `<button name=tu value=eintragen`) || !strings.Contains(umfeld, "4 offene Fragen beantworten") {
		t.Fatalf("Deine Antworten:\n%s", umfeld)
	}
	if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {key}, "tu": {"eintragen"}, "zurueck": {"/tresor/umfeld"}}); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), esc(saysNichtEin)) {
		t.Fatalf("Eintragen while down: %d", rec.Code)
	}
	homes.kaputt = false
	ziel, umfeld := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {key}, "tu": {"eintragen"}, "zurueck": {"/tresor/umfeld"}}))
	if !strings.HasPrefix(ziel, "/tresor/umfeld?") || len(homes.genommen()) != 1 || !strings.Contains(umfeld, "· eingetragen</span>") || strings.Contains(umfeld, "value=eintragen") {
		t.Fatalf("Eintragen: %s %d\n%s", ziel, len(homes.genommen()), umfeld)
	}
	homes.kaputt = true
	if rec := wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {key}, "tu": {"vergessen"}, "zurueck": {"/tresor"}}); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), esc(saysNichtZurueck)) {
		t.Fatalf("Rückgängig while down: %d", rec.Code)
	}
	if b, _ := os.ReadFile(innen.umfeldAntworten()); !strings.Contains(string(b), "kollege") {
		t.Fatalf("forgotten though Homes kept it: %s", b)
	}
	wt.w.HomesIntern = ""
	if page := wt.rufe("GET", "/tresor", anna, nil).Body.String(); !strings.Contains(page, "bleibt vorerst in deinem Archiv: Homes ist noch nicht mit ihm verbunden") {
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
	if baum := wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String(); !strings.Contains(baum, esc(saysStammbaumSpaeter)) {
		t.Fatal("the Stammbaum's place")
	}
	quellen := wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String()
	for _, want := range []string{"<h1>Quellen</h1>", `href="/tresor">‹ Dein Archiv</a>`, "Signal-Passphrase (30", "Postfach eintragen", "SMS und Anrufe", "64-stelligen Schlüssel", "Kontakte und Kalender", "Immich-Schlüssel", "Passkeys"} {
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

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLetzte is msgvault's POST /api/v1/query for the edge page's last
// lines: it answers the newest message of each conversation it knows and
// keeps the statements it got.
type fakeLetzte struct {
	*httptest.Server
	mu     sync.Mutex
	saetze []string
}

func neuesLetzte(t *testing.T) (*fakeLetzte, *MsgvaultAPI) {
	f := &fakeLetzte{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var q struct{ SQL string }
		json.NewDecoder(r.Body).Decode(&q)
		f.mu.Lock()
		f.saetze = append(f.saetze, q.SQL)
		f.mu.Unlock()
		if r.URL.Path != "/api/v1/query" || r.Header.Get("X-API-Key") != "mv-key" {
			http.Error(rw, `{"error":"nope"}`, http.StatusUnauthorized)
			return
		}
		json.NewEncoder(rw).Encode(map[string]any{"columns": []string{"gespraech", "nachricht", "am", "von_mir", "text"}, "rows": [][]any{
			{"71", "4201", "2026-11-01T18:00:00Z", false, "Kommt ihr Sonntag zum Essen?"},
			{"72", "4300", "2026-10-02T08:00:00Z", true, "Anbei die Fotos"},
		}})
	}))
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.URL)
	port, _ := strconv.Atoi(u.Port())
	return f, &MsgvaultAPI{Port: port, Key: func() string { return "mv-key" }}
}

// kanteBox is the open archive of eintragenBox with a group chat and a
// mail thread of Anna and Ben in the table, a cue the model read in
// Ben's message to Anna, msgvault answering the last lines, and Immich's
// public address.
func kanteBox(t *testing.T) (*webTest, *umfeldTest, *fakeHomes, Innen, *fakeLetzte) {
	wt, ut, homes, innen := eintragenBox(t)
	tab, err := tabelleLaden(innen)
	if err != nil {
		t.Fatal(err)
	}
	tab.Gespraeche = []netzGespraech{
		{ID: "71", Art: gespraechChat, Titel: "Familie Berg", Groesse: 3, Nachrichten: 40, Zuletzt: time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC), Wer: []string{"haushalt/a", "haushalt/b", "privat/urn:uuid:k2"}},
		{ID: "72", Art: gespraechMail, Groesse: 2, Nachrichten: 3, Zuletzt: time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC), Wer: []string{"haushalt/a", "haushalt/b"}},
		{ID: "73", Art: gespraechChat, Titel: "Verein", Groesse: 5, Nachrichten: 9, Zuletzt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), Wer: []string{"haushalt/a", "haushalt/c"}},
	}
	if err := jsonImTresor(innen.umfeldTabelle(), tab); err != nil {
		t.Fatal(err)
	}
	gs := map[string]gelesen{"5001": {Am: time.Date(2026, 5, 3, 9, 0, 0, 0, time.UTC), Von: "p-ben", An: []string{"p-anna"}, Zitat: "Hallo Schatz",
		Hinweise: []modellHinweis{{X: "p-ben", Y: "p-anna", Art: "partner", Sicher: 0.9}}}}
	if err := jsonImTresor(innen.umfeldHinweise(), gs); err != nil {
		t.Fatal(err)
	}
	letzte, api := neuesLetzte(t)
	ut.u.Msgvault, ut.u.Oeffentlich = api, "https://fotos.box.test"
	ut.u.laden(innen)
	return wt, ut, homes, innen, letzte
}

// The edge page (owner 2026-11-11: „von einer Kante schnell auf die
// Nachrichten und Bilder kommen“): both faces, the relation as chips with
// the current kind pressed (here the household book's), the shared photo
// days newest first as lazy thumbnails opening Immich, the cue first,
// then the shared chat and thread with their last line opening msgvault,
// and why the two are tied. The thumbnail comes through the archive
// with the person's key, only for a photo of the net.
func TestKanteSeite(t *testing.T) {
	wt, ut, _, _, letzte := kanteBox(t)
	page := wt.rufe("GET", kanteAdresse("p-anna", "p-ben"), anna, nil).Body.String()
	for _, want := range []string{"<h1>Was verbindet die beiden?</h1>", `<div class=paar><a href="/tresor/umfeld?mitte=p-ben">`, "<span>Du</span>",
		"Ben Berg ist für dich …", `<button name=art value=partner class="chip gewaehlt" aria-pressed=true`, "Jetzt: Partner/in, aus dem Telefonbuch.",
		`name=art value=keine`, `name=art value=fremd`, `<input type=hidden name=zurueck value="/tresor/kante?a=p-ben&amp;b=p-anna">`,
		"<h2>Fotos (3 Tage)</h2>", `<a href="https://fotos.box.test/photos/a4" target=_blank rel=noopener><img src="/tresor/kante/bild?id=a4" loading=lazy`,
		"<b>„Hallo Schatz“</b>", `<a href="/messages/5001">`, `<a href="/messages/4201"><b>Familie Berg</b><span class=k>Kommt ihr Sonntag zum Essen? · 01.11.2026</span>`,
		`<a href="/messages/4300"><b>Mail-Verlauf mit 2 Personen</b><span class=k>Du: Anbei die Fotos · 02.10.2026</span>`,
		"<h2>Warum</h2>", "<li>3 Tage auf deinen Fotos seit 2019</li>", "<li>in einem Gruppen-Chat</li>", "<li>gleiche Adresse</li>", "<li>gleicher Nachname</li>",
		"+ Beziehung von Ben Berg", `<datalist id=namen>`, `<a href="/tresor?tab=fragen">Fertig, weiter mit den Fragen</a>`} {
		if !strings.Contains(page, want) {
			t.Fatalf("%q missing:\n%s", want, page)
		}
	}
	for _, order := range [][2]string{{"bild?id=a4", "bild?id=a3"}, {"bild?id=a3", "bild?id=a1"}, {"Hallo Schatz", "Familie Berg"}, {"Familie Berg", "Mail-Verlauf"}} {
		if i, j := strings.Index(page, order[0]), strings.Index(page, order[1]); i < 0 || j < i {
			t.Fatalf("%q not before %q", order[0], order[1])
		}
	}
	if strings.Contains(page, "Verein") || strings.Contains(page, "anna@box.test") {
		t.Fatal("a chat Ben is not in, or an address, on the page")
	}
	if len(letzte.saetze) != 1 || !strings.Contains(letzte.saetze[0], "conversation_id IN (71, 72)") {
		t.Fatalf("msgvault was asked: %q", letzte.saetze)
	}

	rec := wt.rufe("GET", "/tresor/kante/bild?id=a4", anna, nil)
	if rec.Code != 200 || rec.Body.String() != "JPEG a4" || rec.Header().Get("Content-Type") != "image/jpeg" || !strings.HasPrefix(rec.Header().Get("Cache-Control"), "private") {
		t.Fatalf("thumbnail: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	for _, id := range []string{"a99", "../api/users/me", ""} {
		if rec := wt.rufe("GET", "/tresor/kante/bild?id="+url.QueryEscape(id), anna, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%q: %d", id, rec.Code)
		}
	}
	ut.immich.mu.Lock()
	gefragt := strings.Join(ut.immich.anfragen, "\n")
	ut.immich.mu.Unlock()
	if strings.Count(gefragt, "/thumbnail") != 1 {
		t.Fatalf("Immich asked for: %s", gefragt)
	}
	if rec := wt.rufe("GET", "/tresor/kante/bild?id=a4", nil, nil); rec.Code == 200 {
		t.Fatal("a thumbnail without the person")
	}

	// without msgvault: the conversations, without their last line, into msgvault's search
	ut.u.Msgvault = nil
	page = wt.rufe("GET", kanteAdresse("p-ben", "p-anna"), anna, nil).Body.String()
	if !strings.Contains(page, `<b>Familie Berg</b><span class=k>40 Nachrichten, zuletzt am 01.11.2026</span>`) || !strings.Contains(page, "Ben Berg ist für dich …") {
		t.Fatalf("without msgvault:\n%s", page)
	}

	// a pair the net does not know, and a person's other self
	if rec := wt.rufe("GET", kanteAdresse("p-anna", "niemand"), anna, nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), esc(saysKanteWeg)) {
		t.Fatalf("unknown: %d", rec.Code)
	}
}

// One tap changes the edge (owner 2026-11-11: „diese Kante schnell
// nachbessern“), asked or not: the kind is kept in the vault and written
// by Homes; Rückgängig brings back what was before — the phone book's
// relation, or the answer the tap replaced, which Homes writes again.
func TestKanteNachbessern(t *testing.T) {
	wt, _, homes, innen, _ := kanteBox(t)
	hier := kanteAdresse("p-ben", "p-anna")
	tippe := func(art string) string {
		t.Helper()
		ziel, page := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-ben"}, "y": {"p-anna"}, "art": {art}, "zurueck": {hier}}))
		if !strings.HasPrefix(ziel, kantePfad+"?") || !strings.Contains(ziel, "getan=") {
			t.Fatalf("after %s: %s", art, ziel)
		}
		return page
	}
	page := tippe("expartner")
	if !strings.Contains(page, "✓ Ben Berg ist Ex-Partner/in von dir · eingetragen") || !strings.Contains(page, `value=expartner class="chip gewaehlt" aria-pressed=true`) ||
		!strings.Contains(page, "Jetzt: Ex-Partner/in, von dir angegeben.") {
		t.Fatalf("after the tap:\n%s", page)
	}
	ps := homes.genommen()
	if len(ps) != 1 || ps[0].Weg || ps[0].Zeilen[0].Art != "expartner" || ps[0].Zeilen[0].X.Haushalt != "b" || ps[0].Zeilen[0].Y.Haushalt != "a" {
		t.Fatalf("Homes got: %+v", ps)
	}

	// the same kind again: nothing to write
	if _, page := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"x": {"p-anna"}, "y": {"p-ben"}, "art": {"expartner"}, "zurueck": {hier}})); len(homes.genommen()) != 1 || !strings.Contains(page, "Jetzt: Ex-Partner/in") {
		t.Fatalf("the same kind wrote again: %+v", homes.genommen())
	}

	// over the answer: Homes takes the old line back, then writes the new
	// one and what follows from it (Fritz, Anna's brother, is Ben's Schwager)
	tippe("partner")
	if got := homesVerlauf(homes); got != "expartner + expartner - partner + schwager +" {
		t.Fatalf("Homes got: %s", got)
	}
	b, _ := os.ReadFile(innen.umfeldAntworten())
	if !strings.Contains(string(b), `"vorher":{"antwort":{"x":"p-ben","art":"expartner"`) {
		t.Fatalf("the vault keeps no Vorher: %s", b)
	}

	// Rückgängig: expartner is back, in the vault and in Homes; the chain goes
	key := paarKey("p-ben", "p-anna")
	ziel, page := wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {key}, "tu": {"vergessen"}, "zurueck": {hier}}))
	if !strings.Contains(ziel, "m=kante-zurueck") || !strings.Contains(page, saysKanteZurueck) || !strings.Contains(page, "Jetzt: Ex-Partner/in, von dir angegeben.") {
		t.Fatalf("Rückgängig: %s\n%s", ziel, page)
	}
	if got := homesVerlauf(homes); got != "expartner + expartner - partner + schwager + schwager - partner - expartner +" {
		t.Fatalf("Homes got: %s", got)
	}
	// once more: the phone book's relation is what is left
	_, page = wt.folgen(t, wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {key}, "tu": {"vergessen"}, "zurueck": {hier}}))
	if got := homesVerlauf(homes); !strings.Contains(page, "Jetzt: Partner/in, aus dem Telefonbuch.") || !strings.HasSuffix(got, " expartner + expartner -") {
		t.Fatalf("back to the book: %s\n%s", got, page)
	}

	// Homes refuses: the edge page says so, nothing changes
	homes.mu.Lock()
	homes.kaputt = true
	homes.mu.Unlock()
	tippe("kind")
	homes.mu.Lock()
	homes.kaputt = false
	homes.mu.Unlock()
	if page := wt.rufe("GET", hier, anna, nil).Body.String(); !strings.Contains(page, "Jetzt: Kind, von dir angegeben.") {
		t.Fatalf("a refusal of Homes loses the answer:\n%s", page)
	}
}

// homesVerlauf is what Homes took, „kind +“ written, „kind -“ taken back.
func homesVerlauf(h *fakeHomes) string {
	var out []string
	for _, p := range h.genommen() {
		out = append(out, p.Zeilen[0].Art+" "+map[bool]string{true: "-", false: "+"}[p.Weg])
	}
	return strings.Join(out, " ")
}

// „+ Beziehung“ (owner 2026-11-11, „Sabine fehlt als Partnerin“): a name
// finds the person, umlauts and case alike, the person's own other cards
// never; one goes straight to the edge page, several are offered, none is
// said. The person detail and the tree have the form.
func TestBeziehungDazu(t *testing.T) {
	wt, _, _, _, _ := kanteBox(t)
	rec := wt.rufe("GET", kantePfad+"?a=p-ben&wer=carla+wolf", anna, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != kanteAdresse("p-ben", wt.w.Umfeld.Netz().Von("haushalt/c")) {
		t.Fatalf("one found: %d %v", rec.Code, rec.Header())
	}
	if rec := wt.rufe("GET", kantePfad+"?von=Du&wer=fritz", anna, nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != kanteAdresse("p-anna", "p-fritz") {
		t.Fatalf("by first name, from Du: %d %v", rec.Code, rec.Header())
	}
	if page := wt.rufe("GET", kantePfad+"?a=p-ben&wer=berg", anna, nil).Body.String(); !strings.Contains(page, "Welche Person meinst du?") || !strings.Contains(page, `href="/tresor/kante?a=p-ben&amp;b=p-kim"`) {
		t.Fatalf("several:\n%s", page)
	}
	if page := wt.rufe("GET", kantePfad+"?a=p-ben&wer=Zacharias", anna, nil).Body.String(); !strings.Contains(page, esc(saysKanteNiemand)) || !strings.Contains(page, `name=wer list=namen`) {
		t.Fatalf("none:\n%s", page)
	}
	if page := wt.rufe("GET", "/tresor/umfeld?mitte=p-ben", anna, nil).Body.String(); !strings.Contains(page, "+ Beziehung von Ben Berg") || !strings.Contains(page, `<a class=warum-klein href="/tresor/kante?a=p-ben&amp;b=p-anna">`) {
		t.Fatalf("the person detail:\n%s", page)
	}
	if page := wt.rufe("GET", "/tresor?tab=stammbaum", anna, nil).Body.String(); !strings.Contains(page, "+ Beziehung ergänzen") || !strings.Contains(page, `name=von list=namen`) {
		t.Fatalf("the tree:\n%s", page)
	}
	nahe := wt.rufe("GET", "/tresor?tab=nahe", anna, nil).Body.String()
	if !strings.Contains(nahe, `<a class=warum href="/tresor/kante?a=p-anna&amp;b=p-ben" aria-label="Was verbindet dich und Ben Berg?">`) {
		t.Fatalf("Steht nahe has no way to the edge:\n%s", nahe)
	}
	fragen := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	if !strings.Contains(fragen, `<a href="/tresor/kante?a=p-kim&amp;b=p-anna">Warum?</a>`) {
		t.Fatalf("the question card has no Warum?:\n%s", fragen)
	}
}

// Finden: umlauts spelled out or not, case, every word; the person's own
// other cards are her; Namen lists each shown name once, not hers.
func TestFinden(t *testing.T) {
	st := netzTabelle{Namen: map[string]string{}, Ich: "h/ida", IchAuch: []string{"p/ida2"}}
	for _, k := range [][2]string{{"h/ida", "Ida Roth"}, {"p/ida2", "Ida R."}, {"h/juergen", "Jürgen Burger"}, {"h/jb", "Juergen Busch"}} {
		st.Kontakte = append(st.Kontakte, netzKontakt{Key: k[0], Name: k[1]})
	}
	n := neuesNetz(st, nil, nil, time.UTC, nil)
	if ks := n.Finden("juergen burger"); len(ks) != 1 || ks[0].ID != "h/juergen" {
		t.Fatalf("%+v", ks)
	}
	if ks := n.Finden("Jürgen"); len(ks) != 2 {
		t.Fatalf("%+v", ks)
	}
	if ks := n.Finden("Ida"); len(ks) != 1 || ks[0].ID != "h/ida" {
		t.Fatalf("her other card: %+v", ks)
	}
	if ks := n.Finden("du"); len(ks) != 1 || ks[0].ID != "h/ida" {
		t.Fatalf("du: %+v", ks)
	}
	if got := strings.Join(n.Namen(), ","); got != "Juergen Busch,Jürgen Burger" {
		t.Fatal(got)
	}
}

// Every name field is namenFeld: marked for the script's rows, still with
// the datalist for a browser without script, and the datalist once per
// page (owner 2026-11-11: „Ha“ in „Zu wem?“ showed nothing on Android).
func TestNamenFelder(t *testing.T) {
	wt, _, _, _, _ := kanteBox(t)
	for _, adresse := range []string{"/tresor?tab=stammbaum", "/tresor/umfeld?mitte=p-ben", kantePfad + "?a=p-ben&wer=Zacharias", kanteAdresse("p-anna", "p-ben")} {
		page := wt.rufe("GET", adresse, anna, nil).Body.String()
		if n := strings.Count(page, `<datalist id=namen>`); n != 1 {
			t.Fatalf("%s: %d datalists named namen", adresse, n)
		}
		if want := namenListe(wt.w.Umfeld.Netz()); len(wt.w.Umfeld.Netz().Namen()) < 5 || !strings.Contains(page, want) {
			t.Fatalf("%s: the datalist is not the net's names %s:\n%s", adresse, want, page)
		}
		if !strings.Contains(page, `<input name=wer list=namen data-namen autocomplete=off required placeholder="Name">`) || strings.Count(page, "list=namen") != strings.Count(page, "data-namen ") {
			t.Fatalf("%s: a name field without namenFeld:\n%s", adresse, page)
		}
	}
	if f := namenFeld("von", `Du"`); f != `<input name=von list=namen data-namen autocomplete=off required placeholder="Name" value="Du&#34;">` {
		t.Fatal(f)
	}
	for _, want := range []string{"input[data-namen]", "removeAttribute('list')", ".slice(0,6)", "replace(/ä/g,'ae')", "b.textContent=n"} {
		if !strings.Contains(skript, want) {
			t.Fatalf("the script has no %q", want)
		}
	}
}

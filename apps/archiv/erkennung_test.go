package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func herkunftZeile(id, am, mail string) []any { return []any{id, am, mail} }

// Home town and employer from mail (archiv#44): the end of a known
// sender's newest mails (HTML made text, the quoted mail left out) goes
// to the box's model with the sender's company domain (never a free
// one's); a sure reading is a suggestion, an unsure one or a field a
// card has is none. At most two mails of one sender, none of an unknown
// one, none twice, none while a lease holds the card; nothing of a text
// in the log, nothing outside the vault.
func TestHerkunftAusMails(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(io.MultiWriter(&logs, os.Stderr))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	ut := netzBox(t)
	ut.rad.haushalt["k.vcf"] = karteMit("k", "Kim Berg", "EMAIL:kim@firma-k.de\r\n")
	ut.rad.haushalt["b.vcf"] = karteMit("b", "Ben Berg", "ADR;TYPE=HOME:;;Gartenweg 2;Saarbrücken;;66111;Deutschland\r\nEMAIL:ben@gmail.com\r\n")
	ut.rad.haushalt["c.vcf"] = karteMit("c", "Carla Wolf", "ORG:Werk AG;Einkauf\r\nEMAIL:carla@werk.example\r\n")
	mv, api := neuesMsgvaultHinweise(t)
	mv.herkunft = [][]any{
		herkunftZeile("11", "2026-10-01T08:00:00Z", "kim@firma-k.de"),
		herkunftZeile("12", "2026-09-01T08:00:00Z", "Kim@Firma-K.de"),
		herkunftZeile("13", "2026-08-01T08:00:00Z", "kim@firma-k.de"),
		herkunftZeile("21", "2026-10-02T08:00:00Z", "ben@gmail.com"),
		herkunftZeile("31", "2026-10-03T08:00:00Z", "carla@werk.example"),
		herkunftZeile("41", "2026-10-04T08:00:00Z", "fremd@example.org"),
	}
	mv.koerper = map[string]string{
		"11": "Hallo Anna,\nbis bald!\n\nViele Grüße aus Ulm\nKim\n--\nKim Berg\nFirma K GmbH\n\nAm 30.09.2026 schrieb Anna:\n> Grüße aus Berlin",
		"12": "<html><body><p>Hallo</p><div>Grüße aus Ulm<br>Kim Berg &amp; Team<br>Firma K GmbH</div><style>p{x:y}</style></body></html>",
		"13": "Grüße aus Ulm",
		"21": "Bis dann, Ben\nBank Saar AG",
		"31": "Gruß Carla",
	}
	mo, modell := neuesModell(t)
	mo.antwort = func(frage string) string {
		switch {
		case strings.Contains(frage, "Ulm"):
			return `{"ort":"Ulm","ort_sicher":0.9,"arbeitgeber":"Firma K GmbH","arbeitgeber_sicher":0.85}`
		case strings.Contains(frage, "Ben"):
			return "```json\n{\"ort\":\"Saarbrücken\",\"ort_sicher\":0.9,\"arbeitgeber\":\"Bank Saar AG\",\"arbeitgeber_sicher\":0.8}\n```"
		}
		return `{"ort":"Köln","ort_sicher":0.5,"arbeitgeber":"","arbeitgeber_sicher":0}`
	}
	ut.u.Msgvault, ut.u.Modell = api, modell
	ut.u.Runde(t.Context(), ut.innen)

	var fragen []string
	for _, f := range mo.gefragt() {
		if strings.HasPrefix(f, "Absender:") {
			fragen = append(fragen, f)
		}
	}
	if len(fragen) != 4 {
		t.Fatalf("asked %d about mails, want 4 (two of Kim's, Ben's, Carla's): %q", len(fragen), fragen)
	}
	t.Logf("the prompt:\n%s", fragen[0])
	for _, want := range []string{"Absender: Kim Berg", "Domain der Absenderadresse: firma-k.de", "Viele Grüße aus Ulm\nKim\n--\nKim Berg\nFirma K GmbH»"} {
		if !strings.Contains(fragen[0], want) {
			t.Errorf("the prompt lacks %q:\n%s", want, fragen[0])
		}
	}
	if strings.Contains(fragen[0], "Berlin") || strings.Contains(fragen[1], "<") || strings.Contains(fragen[1], "x:y") || !strings.Contains(fragen[1], "Kim Berg & Team") {
		t.Errorf("the quote or HTML reached the model:\n%s\n%s", fragen[0], fragen[1])
	}
	if strings.Contains(fragen[2], "Domain") {
		t.Errorf("a free mail domain as an employer's:\n%s", fragen[2])
	}
	var got []string
	for _, v := range ut.u.Netz().HerkunftVorschlaege() {
		got = append(got, fmt.Sprintf("%s|%s=%s/%d", v.Wer.Name, v.Feld, v.Wert, v.Belege))
	}
	if want := []string{"Kim Berg|arbeit=Firma K GmbH/2", "Kim Berg|ort=Ulm/2", "Ben Berg|arbeit=Bank Saar AG/1"}; !slices.Equal(got, want) {
		t.Fatalf("suggestions: %v, want %v", got, want)
	}
	if fi, err := os.Stat(ut.innen.umfeldHerkunft()); err != nil || fi.Mode().Perm() != 0o600 || len(herkunftLaden(ut.innen)) != 4 {
		t.Fatalf("the vault: %v %v %d", fi, err, len(herkunftLaden(ut.innen)))
	}

	ut.u.Runde(t.Context(), ut.innen)
	mv.herkunft = append(mv.herkunft, herkunftZeile("22", "2026-10-05T08:00:00Z", "ben@gmail.com"))
	mv.koerper["22"] = "Ben"
	mo.mu.Lock()
	mo.lease = "ready"
	mo.mu.Unlock()
	ut.u.Runde(t.Context(), ut.innen)
	if n := len(herkunftLaden(ut.innen)); n != 4 {
		t.Fatalf("read again or under a lease: %d", n)
	}
	for _, geheim := range []string{"Ulm", "Firma K", "Bank", "Kim", "kim@"} {
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

	// the page: one tap each; confirmed ones stay in the vault, nothing goes to Homes
	wt, ut2, homes, innen := eintragenBox(t)
	b, _ := os.ReadFile(ut.innen.umfeldHerkunft())
	os.WriteFile(innen.umfeldHerkunft(), b, 0o600)
	ut2.u.laden(innen)
	if page := wt.rufe("GET", "/tresor", anna, nil).Body.String(); !strings.Contains(page, "Erkannt: 3 Vorschläge") {
		t.Fatalf("the line:\n%s", page)
	}
	page := wt.rufe("GET", erkanntPfad, anna, nil).Body.String()
	for _, want := range []string{"Wohnort und Arbeit (3)", esc("Kim Berg wohnt in Ulm?"), esc("Kim Berg arbeitet bei Firma K GmbH?"), "Vorschlag aus 2 Mails, die letzte vom 01.10.2026."} {
		if !strings.Contains(page, want) {
			t.Fatalf("the page lacks %q:\n%s", want, page)
		}
	}
	rec := wt.rufe("POST", erkanntPfad+"/herkunft", anna, url.Values{"id": {"p-kim"}, "feld": {"ort"}, "wert": {"ulm"}, "ja": {"1"}})
	_, page = wt.folgen(t, rec)
	if !strings.Contains(page, meldungen["herkunft-ja"]) || !strings.Contains(page, "Bestätigte Wohnorte und Arbeitgeber (1)") || strings.Contains(page, esc("Kim Berg wohnt in Ulm?")) {
		t.Fatalf("Stimmt:\n%s", page)
	}
	wt.folgen(t, wt.rufe("POST", erkanntPfad+"/herkunft", anna, url.Values{"id": {"p-ben"}, "feld": {"arbeit"}, "wert": {"Bank Saar AG"}, "ja": {"0"}}))
	if a := herkunftAntwortenLaden(innen); !a["p-kim|ort"].Ja || a["p-kim|ort"].Wert != "Ulm" || a["p-ben|arbeit"].Ja {
		t.Fatalf("the vault: %+v", a)
	}
	if n := len(ut2.u.Netz().HerkunftVorschlaege()); n != 1 {
		t.Fatalf("answered ones suggested again: %d left", n)
	}
	if rec := wt.rufe("POST", erkanntPfad+"/herkunft", anna, url.Values{"id": {"p-kim"}, "feld": {"arbeit"}, "wert": {"Andere AG"}, "ja": {"1"}}); rec.Code != 409 {
		t.Fatalf("a value not suggested: %d", rec.Code)
	}
	wt.folgen(t, wt.rufe("POST", erkanntPfad+"/herkunft", anna, url.Values{"id": {"p-kim"}, "feld": {"ort"}, "tu": {"vergessen"}}))
	if len(ut2.u.Netz().HerkunftBestaetigt()) != 0 || len(ut2.u.Netz().HerkunftVorschlaege()) != 2 {
		t.Fatal("Vergessen did not give the suggestion back")
	}
	if len(homes.genommen()) != 0 {
		t.Fatal("a town or an employer went to Homes")
	}
}

// The model's answer about a mail is read strictly.
func TestHerkunftAntwort(t *testing.T) {
	for s, want := range map[string]string{
		`{"ort":"Ulm","ort_sicher":0.9,"arbeitgeber":"","arbeitgeber_sicher":0}`:    "Ulm/0.9|/0|true",
		`{"ort":"Ulm","ort_sicher":1.5,"arbeitgeber":"X","arbeitgeber_sicher":0.7}`: "/0|X/0.7|true",
		`{"ort":"Ulm\nStraße","ort_sicher":0.9}`:                                    "/0|/0|true",
		`{"ort":"Ulm","ort_sicher":0.9} und mehr`:                                   "/0|/0|false",
		`Ulm`: "/0|/0|false",
		`{}`:  "/0|/0|false",
	} {
		l, ok := herkunftAntwortLesen(s)
		if got := fmt.Sprintf("%s/%g|%s/%g|%t", l.Ort, l.OrtSicher, l.Arbeit, l.ArbSicher, ok); got != want {
			t.Errorf("%q: %q, want %q", s, got, want)
		}
	}
	if firmenDomain("a@gmail.com") != "" || firmenDomain("a@outlook.de") != "" || firmenDomain("a@werk.example") != "werk.example" {
		t.Fatal("firmenDomain")
	}
}

// The couples' words (archiv#44).
func TestPaarMuster(t *testing.T) {
	for text, want := range map[string]string{
		"Ich komme mit meinem Mann Tom.": "Tom",
		"meine Frau, Lena, kommt auch":   "Lena",
		"my wife Sarah says hi":          "Sarah",
		"meine Frau kommt auch":          "",
		"mein Mann. Tom kommt":           "",
		"Frau Müller kommt":              "",
		"mein Mann Und ich":              "",
	} {
		if got, _ := woerter.meinPartner(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
	for text, want := range map[string]string{
		"Wir kommen gern! Anna und Ben":    "Anna+Ben",
		"Wir freuen uns. LG Kim & Fritz 😊": "Kim+Fritz",
		"Anna und Ben":                     "",
		"Wir kommen. Anna und Ben kommen":  "",
		"wir kommen, anna und ben":         "",
	} {
		a, b, _ := woerter.zuZweit(text)
		if got := map[bool]string{true: a + "+" + b}[a != ""]; got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
	m := woerter.paarMuster()
	for _, s := range []string{"mit meinem mann tom", "wir kommen. kim & fritz!"} {
		if !regexp.MustCompile(m).MatchString(s) {
			t.Errorf("msgvault's filter misses %q", s)
		}
	}
}

// Couples from „wir“ messages: „mein Mann …“ once, signed by two twice,
// the name one known person; a pair the phone book knows, a name nobody
// has and a single signature are none. Stimmt nicht keeps it in the
// vault and the suggestion is gone.
func TestPaareAusWirNachrichten(t *testing.T) {
	ut := netzBox(t)
	ut.rad.haushalt["k.vcf"] = karteMit("k", "Kim Berg", "TEL:0153 3333 3333\r\n")
	ut.rad.haushalt["b.vcf"] = karteMit("b", "Ben Berg", "ADR;TYPE=HOME:;;Gartenweg 2;Saarbrücken;;66111;Deutschland\r\nTEL:0152 2222 2222\r\n")
	ut.rad.haushalt["c.vcf"] = karteMit("c", "Carla Wolf", "ORG:Werk AG;Einkauf\r\nTEL:0154 4444 4444\r\n")
	mv, api := neuesMsgvaultHinweise(t)
	mv.paare = [][]any{
		hinweisZeile("1", false, "Ich komme mit meiner Frau Anna", "von", "+4915222222222"),
		hinweisZeile("2", false, "Wir kommen gern! Kim & Fritz", "von", "+4915333333333"),
		hinweisZeile("3", false, "Wir sind schon da. Kim und Fritz", "von", "+4915333333333"),
		hinweisZeile("4", false, "Ich bringe meinen Mann Ben mit", "von", "+4915444444444"),
		hinweisZeile("5", false, "Wir warten. Kim und Gerd", "von", "+4915333333333"),
		hinweisZeile("6", false, "mein Mann Hugo grüßt", "von", "+4915444444444"),
	}
	ut.u.Msgvault = api
	ut.u.Runde(t.Context(), ut.innen)
	var got []string
	for _, b := range ut.u.Netz().ErkannteBeziehungen() {
		if b.Quelle == "wir" {
			got = append(got, fmt.Sprintf("%s|%s|%s|%s", b.X.Name, b.Y.Name, b.Art, b.Weil))
		}
	}
	want := []string{
		"Carla Wolf|Ben Berg|partner|Carla Wolf „meinen Mann Ben“ schreibt",
		"Kim Berg|Fritz Kurz|partner|Kim Berg 2 Nachrichten mit „Kim & Fritz“ unterschreibt",
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("couples:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if ps := paareLaden(ut.innen); len(ps) != 3 || ps[paarKey("p-anna", "p-ben")].Zitat != "meiner Frau Anna" { // kept, but the phone book has them
		t.Fatalf("the vault: %+v", ps)
	}

	wt, ut2, _, innen := eintragenBox(t)
	b, _ := os.ReadFile(ut.innen.umfeldPaare())
	os.WriteFile(innen.umfeldPaare(), b, 0o600)
	tab, _ := tabelleLaden(ut.innen)
	jsonImTresor(innen.umfeldTabelle(), tab)
	ut2.u.laden(innen)
	page := wt.rufe("GET", erkanntPfad, anna, nil).Body.String()
	if !strings.Contains(page, esc("Vorschlag, weil Kim Berg 2 Nachrichten mit „Kim & Fritz“ unterschreibt.")) || !strings.Contains(page, ">Stimmt nicht<") {
		t.Fatalf("the page:\n%s", page)
	}
	var x, y string
	for _, e := range ut2.u.Netz().ErkannteBeziehungen() {
		if e.Quelle == "wir" && e.X.Name == "Kim Berg" {
			x, y = e.X.ID, e.Y.ID
		}
	}
	_, page = wt.folgen(t, wt.rufe("POST", erkanntPfad+"/paar", anna, url.Values{"x": {x}, "y": {y}}))
	if !strings.Contains(page, meldungen["paar-nein"]) || strings.Contains(page, esc("„Kim & Fritz“")) {
		t.Fatalf("Stimmt nicht:\n%s", page)
	}
	if weg := paareWegLaden(innen); weg[paarKey(x, y)].IsZero() {
		t.Fatalf("not kept: %v", weg)
	}
	if rec := wt.rufe("POST", erkanntPfad+"/paar", anna, url.Values{"x": {x}, "y": {"p-anna"}}); rec.Code != 409 {
		t.Fatalf("a couple never suggested: %d", rec.Code)
	}
}

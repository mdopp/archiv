package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mdopp/archiv/apps/archiv/tresor"
)

var homesWort = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

var homesKey = map[string]string{homesKeyHeader: "homes-geheim"}

// homesHolen is Homes asking for anna, checked against contract 1 as Homes
// checks it (homes internal/plugin: pruefen, testdata/vertrag-v1.json):
// no field Homes does not know, the required ones, https links, an id a
// word and never a name, short texts.
func homesHolen(t *testing.T, wt *webTest) homesAntwort {
	t.Helper()
	rec := wt.rufe("GET", homesVertragPfad+"?person=anna", homesKey, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("contract: %d %s", rec.Code, rec.Body)
	}
	var v struct {
		Vertrag  int    `json:"vertrag"`
		Name     string `json:"name"`
		Icon     string `json:"icon"`
		Link     string `json:"link"`
		Zeile    string `json:"zeile"`
		Start    *struct{ Titel, Zeile, Link string }
		Aufgaben *[]struct {
			ID    string `json:"id"`
			Titel string `json:"titel"`
			Warum string `json:"warum"`
			Link  string `json:"link"`
			Art   string `json:"art"`
		} `json:"aufgaben"`
		Widgets []struct{ Name, Titel, Wert, Link string }
	}
	d := json.NewDecoder(strings.NewReader(rec.Body.String()))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		t.Fatalf("outside contract 1: %v: %s", err, rec.Body)
	}
	https := func(s string) bool { return strings.HasPrefix(s, "https://") }
	if v.Vertrag != 1 || v.Name == "" || !https(v.Link) || v.Icon != "archiv" {
		t.Fatalf("contract 1's head: %s", rec.Body)
	}
	if v.Aufgaben != nil {
		if len(*v.Aufgaben) > 20 {
			t.Fatalf("more than 20 tasks: %d", len(*v.Aufgaben))
		}
		for _, a := range *v.Aufgaben {
			if !homesWort.MatchString(a.ID) || a.Titel == "" || utf8.RuneCountInString(a.Titel) > 160 || utf8.RuneCountInString(a.Warum) > 400 ||
				!https(a.Link) || a.Art != "" && a.Art != "schluessel" {
				t.Fatalf("a task outside the contract: %+v", a)
			}
		}
	}
	var a homesAntwort
	json.Unmarshal(rec.Body.Bytes(), &a)
	return a
}

// homesZeile is the row id, or the zero one.
func homesZeile(a homesAntwort, id string) homesAufgabe {
	for _, z := range *cmpOrAufgaben(a.Aufgaben) {
		if z.ID == id {
			return z
		}
	}
	return homesAufgabe{}
}

func cmpOrAufgaben(as *[]homesAufgabe) *[]homesAufgabe {
	if as == nil {
		return &[]homesAufgabe{}
	}
	return as
}

// Contract 1 (homes ADR 0003, archiv ADR 0001 §4): only with Homes' key,
// only for this archive's person, GET only. Open, the tasks with the
// next question's text; closed, none (Homes keeps its own), and the
// archive keeps nothing of them outside the vault.
func TestHomesVertrag(t *testing.T) {
	wt, ut, _, _ := eintragenBox(t)
	wt.w.PublicURL = "https://archiv.box.test/"
	for _, c := range []struct {
		method, pfad string
		hdr          map[string]string
		code         int
	}{
		{"GET", homesVertragPfad + "?person=anna", nil, http.StatusUnauthorized},
		{"GET", homesVertragPfad + "?person=anna", anna, http.StatusUnauthorized}, // the person's browser is not Homes
		{"GET", homesVertragPfad + "?person=ben", homesKey, http.StatusForbidden},
		{"POST", homesVertragPfad + "?person=anna", homesKey, http.StatusMethodNotAllowed},
	} {
		if rec := wt.rufe(c.method, c.pfad, c.hdr, nil); rec.Code != c.code {
			t.Errorf("%s %s: %d, want %d", c.method, c.pfad, rec.Code, c.code)
		}
	}

	a := homesHolen(t, wt)
	n := ut.u.Netz()
	fs := n.Fragen()
	want := len(fs) + n.Erkannt(ut.u.Stand(), false).Vorschlaege()
	f := homesZeile(a, "fragen")
	if want == 0 || len(fs) == 0 || f.Titel != "Im Archiv: "+anzahl(want, "Frage", "Fragen")+" offen" || f.Warum != saysHomesNaechst+frageSatz(fs[0]) ||
		f.Link != "https://archiv.box.test/tresor" || a.Link != "https://archiv.box.test/tresor" || a.Name != "Dein Archiv" {
		t.Fatalf("open: %+v, want %d questions, next %q", a, want, frageSatz(fs[0]))
	}
	if !strings.HasPrefix(f.Warum, saysHomesNaechst+"Wie ") && !strings.HasPrefix(f.Warum, saysHomesNaechst+"Kennen ") {
		t.Errorf("the next question's text: %q", f.Warum)
	}
	if homesZeile(a, "schluessel").ID != "" {
		t.Errorf("a key missing although all are there: %+v", a.Aufgaben)
	}

	wt.tt.Abschliessen(context.Background(), "t")
	if a := homesHolen(t, wt); a.Aufgaben != nil || a.Name == "" {
		t.Fatalf("closed: %+v", a)
	}
	filepath.WalkDir(wt.data, func(p string, d os.DirEntry, err error) error { // nothing of a question outside the vault
		if err != nil || d.IsDir() || strings.Contains(p, "tresor") {
			return nil
		}
		if b, _ := os.ReadFile(p); strings.Contains(string(b), f.Warum) {
			t.Errorf("%s holds a question's text", p)
		}
		return nil
	})
}

// The program „Aufgaben für Homes“ reads only, and only what it needs; a
// person who grants it nothing hands nothing over: no tasks, as if
// closed, while the other programs work on.
func TestHomesNurMitFreigabe(t *testing.T) {
	var p programm
	for _, q := range programme {
		if q.id == progAufgaben {
			p = q
		}
	}
	want := tresor.Rechte{tresor.Beziehungen: tresor.Lesen, tresor.Kontakte: tresor.Lesen, tresor.Notizen: tresor.Lesen}
	if len(p.will) != len(want) {
		t.Fatalf("the program asks for %v", p.will)
	}
	for b, r := range want {
		if p.will[b] != r {
			t.Fatalf("the program asks for %v", p.will)
		}
	}
	if !strings.Contains(p.wozu, "außerhalb des Tresors") {
		t.Errorf("the grant does not say the text leaves the vault: %q", p.wozu)
	}

	wt, _, _, _ := eintragenBox(t)
	wt.w.PublicURL = "https://archiv.box.test"
	wt.tt.Abschliessen(context.Background(), "t")
	ohne := alleWuensche()
	delete(ohne, progAufgaben)
	if err := wt.tt.Tresor.Oeffnen(context.Background(), []byte(geheim), ohne); err != nil {
		t.Fatal(err)
	}
	wt.tt.bereit(t)
	if a := homesHolen(t, wt); a.Aufgaben != nil {
		t.Fatalf("without a grant: %+v", a.Aufgaben)
	}
}

// A choice kept before the program existed offers it as at a first
// opening; once the person said no, it stays no.
func TestNachgereichtesProgramm(t *testing.T) {
	fw := &Freigabenwahl{Datei: filepath.Join(t.TempDir(), "freigaben.json")}
	alt := alleWuensche()
	delete(alt, progAufgaben)
	delete(alt, progNachrichten) // denied before: an old file left it out
	b, _ := json.Marshal(alt)
	os.WriteFile(fw.Datei, b, 0o600)
	f := fw.Letzte()
	if len(f[progAufgaben]) != 3 || len(f[progNachrichten]) != 0 {
		t.Fatalf("the old choice: %v", f)
	}
	nein := alleWuensche()
	nein[progAufgaben] = tresor.Rechte{}
	fw.merken(nein)
	if f := fw.Letzte(); len(f[progAufgaben]) != 0 {
		t.Fatalf("her no did not hold: %v", f)
	}
}

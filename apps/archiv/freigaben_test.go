package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mdopp/archiv/apps/archiv/tresor"
)

// The person grants per session, at the opening (archiv ADR 0001 §3): what
// she ticks is all a program gets, a box for more than a program asks for
// grants nothing, and the next opening offers her choice again.
func TestTheOpeningGrantsWhatIsTicked(t *testing.T) {
	wt := neuesWeb(t)
	wt.tt.ablehnungErwartet = true
	wt.w.Freigaben = &Freigabenwahl{Datei: filepath.Join(wt.data, "freigaben.json")}
	wt.oeffnen(t)
	wt.tt.Abschliessen(context.Background(), "test")
	page := wt.rufe("GET", "/tresor", anna, nil).Body.String()
	for _, want := range []string{`<details class=freigaben id=freigaben>`, `name="f.stammbaum.beziehungen.s" value=1 checked`, `name=freigaben value=1 form=mit-passwort`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the closed page lacks %q", want)
		}
	}
	form := url.Values{"passwort": {geheim}, "freigaben": {"1"},
		"f.stammbaum.beziehungen.l": {"1"}, // read, not write
		"f.importe.nachrichten.l":   {"1"}, "f.importe.nachrichten.s": {"1"},
		"f.datenpfleger.nachrichten.l": {"1"}, // the Datenpfleger does not ask for Nachrichten
	}
	if rec := wt.rufe("POST", "/tresor/oeffnen", anna, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	wt.tt.bereit(t)
	s, _ := wt.tt.Sitzung()
	want := tresor.Freigaben{progStammbaum: {tresor.Beziehungen: tresor.Lesen}, progImporte: {tresor.Nachrichten: tresor.Lesen | tresor.Schreiben}}
	if len(s.Freigaben) != len(want) || s.Freigaben[progStammbaum][tresor.Beziehungen] != tresor.Lesen ||
		s.Freigaben[progImporte][tresor.Nachrichten] != tresor.Lesen|tresor.Schreiben || len(s.Freigaben[progStammbaum]) != 1 {
		t.Fatalf("granted %v, want %v", s.Freigaben, want)
	}
	if _, err := wt.tt.Schluessel(progDatenpfleger); !errors.Is(err, tresor.ErrNichtFreigegeben) {
		t.Fatalf("a program granted nothing got a key: %v", err)
	}
	if err := wt.tt.P.Stammbaum.schreiben(fachAntworten, []byte("{}")); !errors.Is(err, tresor.ErrNichtFreigegeben) {
		t.Fatalf("a write with a read grant: %v", err)
	}
	if _, err := wt.tt.P.Stammbaum.lesen(fachImmichSchluessel); !errors.Is(err, tresor.ErrNichtFreigegeben) {
		t.Fatalf("Fotos without the box: %v", err)
	}
	// the person's own reading of messages was not ticked either
	if rec := wt.rufe("GET", "/api/v1/stats", anna, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), esc(saysNichtFreigegeben)) {
		t.Fatalf("msgvault without the Nachrichten program: %d %s", rec.Code, rec.Body)
	}
	// the protocol shows the refusals, and the page the session's grants
	page = wt.rufe("GET", freigabenPfad, anna, nil).Body.String()
	for _, want := range []string{"<b>Umfeld und Stammbaum</b>: Beziehungen lesen", "<b>Datenpfleger</b>: nichts", "Umfeld und Stammbaum: Beziehungen schreiben, 0 erlaubt, 1 abgelehnt", "Nachrichten: Nachrichten lesen, 0 erlaubt, 1 abgelehnt"} {
		if !strings.Contains(page, esc(want)) && !strings.Contains(page, want) {
			t.Fatalf("the grants page lacks %q:\n%s", want, page)
		}
	}
	wt.tt.Abschliessen(context.Background(), "test")
	if page := wt.rufe("GET", "/tresor", anna, nil).Body.String(); strings.Contains(page, `name="f.stammbaum.beziehungen.s" value=1 checked`) || !strings.Contains(page, `name="f.stammbaum.beziehungen.l" value=1 checked`) {
		t.Fatal("the next opening does not offer the last choice")
	}
	// a form without the boxes (Homes' button) grants the last choice
	wt.rufe("POST", "/tresor/oeffnen", anna, url.Values{"passwort": {geheim}})
	wt.tt.bereit(t)
	if s, _ := wt.tt.Sitzung(); s.Freigaben[progStammbaum][tresor.Beziehungen] != tresor.Lesen || s.Freigaben[progDatenpfleger] != nil {
		t.Fatalf("without boxes: %v", s.Freigaben)
	}
}

// Locking voids every program's key at once, and a key of one session
// never opens the next: through the program's own handle and as a bearer.
func TestTheLockVoidsTheProgramsKeys(t *testing.T) {
	wt := neuesWeb(t)
	wt.oeffnen(t)
	st := wt.tt.P.Stammbaum
	if err := st.schreiben(fachAntworten, []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	alt, _ := st.p.Schluessel()
	wt.rufe("POST", "/tresor/abschliessen", anna, url.Values{})
	if _, err := st.lesen(fachAntworten); !errors.Is(err, tresor.ErrAbgeschlossen) {
		t.Fatalf("the program read after the lock: %v", err)
	}
	if _, err := alt.Lesen(tresor.Beziehungen, fachAntworten.name); !errors.Is(err, tresor.ErrAbgeschlossen) {
		t.Fatalf("its key read after the lock: %v", err)
	}
	wt.rufe("POST", "/tresor/oeffnen", anna, url.Values{"passwort": {geheim}})
	wt.tt.bereit(t)
	if _, err := alt.Lesen(tresor.Beziehungen, fachAntworten.name); !errors.Is(err, tresor.ErrAbgeschlossen) {
		t.Fatalf("the old session's key read in the new one: %v", err)
	}
	if rec := wt.rufe("GET", "/api/v1/stats", map[string]string{"Authorization": "Bearer " + alt.Token()}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the old token as a bearer: %d", rec.Code)
	}
	if b, err := st.lesen(fachAntworten); err != nil || string(b) != `{"x":1}` {
		t.Fatalf("the program's new key in the new session: %q %v", b, err)
	}
}

// The probe that holds the line between the features and the vault: no
// feature file learns where the vault is mounted, makes a program's
// handle, or builds an Innen of its own. Only main.go knows the mount
// point (to hand it to the vault), and only programme.go names programs.
func TestNoFeatureReachesTheVaultAroundTheKeys(t *testing.T) {
	regeln := []struct {
		muster *regexp.Regexp
		nur    string
		was    string
	}{
		{regexp.MustCompile(`TRESOR_MOUNT|/run/tresor`), "main.go", "the vault's mount point"},
		{regexp.MustCompile(`\.Programm\(`), "programme.go", "a program's handle"},
		{regexp.MustCompile(`Innen\{[^}]`), "programme.go", "an Innen of its own"},
		{regexp.MustCompile(`\.Schluessel\(prog|\.Vorgezeigt\(`), "seite.go", "a key for another program"},
	}
	dateien, _ := filepath.Glob("*.go")
	geprueft := 0
	for _, d := range dateien {
		if strings.HasSuffix(d, "_test.go") {
			continue
		}
		b, err := os.ReadFile(d)
		if err != nil {
			t.Fatal(err)
		}
		geprueft++
		for _, r := range regeln {
			if d != r.nur && r.muster.Match(b) {
				t.Errorf("%s reaches %s (%s): only %s may", d, r.was, r.muster.FindString(string(b)), r.nur)
			}
		}
	}
	if geprueft < 50 {
		t.Fatalf("only %d files checked: the probe looked in the wrong place", geprueft)
	}
}

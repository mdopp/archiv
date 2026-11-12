package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func aufgabenHolen(t *testing.T, wt *webTest) AufgabenStand {
	t.Helper()
	rec := wt.rufe("GET", "/intern/aufgaben", map[string]string{homesHeader: "homes-geheim"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("aufgaben: %d %s", rec.Code, rec.Body)
	}
	var s AufgabenStand
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return s
}

// What Homes may know (owner 2026-11-12): only counts, only with Homes'
// secret. They are counted while the vault is open; the last count stays
// outside it with its time and answers while it is closed; nothing of a
// question (no name, no kind) is in the answer or the file.
func TestHomesGetsOnlyTheCounts(t *testing.T) {
	wt, ut, _, innen := eintragenBox(t)
	datei := filepath.Join(wt.data, "aufgaben.json")
	wt.w.Aufgaben = &Aufgaben{Datei: datei}
	if rec := wt.rufe("GET", "/intern/aufgaben", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without the secret: %d", rec.Code)
	}
	if rec := wt.rufe("GET", "/intern/aufgaben", anna, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the person's browser is not Homes: %d", rec.Code)
	}
	if s := aufgabenHolen(t, wt); s.Am != nil || s.Offen || s.Fragen != 0 {
		t.Fatalf("never counted: %+v", s)
	}

	wt.w.aufgabenZaehlen(wt.tt.Stand())
	n := ut.u.Netz()
	want := len(n.Fragen()) + n.Erkannt(ut.u.Stand(), false).Vorschlaege()
	s := aufgabenHolen(t, wt)
	if want == 0 || s.Fragen != want || !s.Offen || s.Am == nil || !s.Am.Equal(*ut.jetzt) || s.SchluesselFehlt {
		t.Fatalf("open: %+v, want %d questions", s, want)
	}
	roh, _ := os.ReadFile(datei)
	for _, name := range []string{"Anna", "Ben", "Kim", "Fritz", "Gerd", "Berg"} {
		if strings.Contains(string(roh), name) {
			t.Fatalf("a name left the vault: %s", roh)
		}
	}
	if fi, err := os.Stat(datei); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file: %v %v", fi, err)
	}

	// unchanged and soon after: not written again; later: written with the new time
	frueh := *ut.jetzt
	*ut.jetzt = frueh.Add(time.Minute)
	wt.w.aufgabenZaehlen(wt.tt.Stand())
	if s := aufgabenHolen(t, wt); !s.Am.Equal(frueh) {
		t.Fatalf("rewritten after a minute: %v", s.Am)
	}
	*ut.jetzt = frueh.Add(aufgabenNeu + time.Minute)
	os.Remove(innen.ImmichSchluessel()) // and the Immich key is gone
	wt.w.aufgabenZaehlen(wt.tt.Stand())
	if s := aufgabenHolen(t, wt); !s.Am.Equal(*ut.jetzt) || !s.SchluesselFehlt {
		t.Fatalf("later, without the Immich key: %+v", s)
	}

	// closed: the last count, marked as not of now, also after a restart
	wt.tt.Abschliessen(context.Background(), "t")
	wt.w.aufgabenZaehlen(wt.tt.Stand())
	wt.w.Aufgaben = &Aufgaben{Datei: datei}
	s = aufgabenHolen(t, wt)
	if s.Offen || s.Fragen != want || !s.SchluesselFehlt || !s.Am.Equal(*ut.jetzt) {
		t.Fatalf("closed: %+v", s)
	}
}

// A missing Signal key is known with the vault closed too: backups wait
// and the phone number (outside the vault) is not given.
func TestAMissingSignalKeyIsSaidWhileClosed(t *testing.T) {
	wt := neuesWeb(t)
	wt.w.Aufgaben = &Aufgaben{Datei: filepath.Join(wt.data, "aufgaben.json")}
	if s := aufgabenHolen(t, wt); s.SchluesselFehlt {
		t.Fatalf("nothing waits: %+v", s)
	}
	os.MkdirAll(filepath.Join(wt.inbox, "signal"), 0o755)
	os.WriteFile(filepath.Join(wt.inbox, "signal", "signal-2026-11-01-00-00-00.backup"), []byte("x"), 0o644)
	alt := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(wt.inbox, "signal", "signal-2026-11-01-00-00-00.backup"), alt, alt)
	if s := aufgabenHolen(t, wt); !s.SchluesselFehlt || s.Am != nil {
		t.Fatalf("a backup waits without the number: %+v", s)
	}
}

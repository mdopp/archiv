package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// What Homes may know of the archive (owner 2026-11-12, homes#362): how
// many things wait for the person, never what they are. Homes shows it
// only to the archive's owner, on „Deine Aufgaben“ and on Start („Im
// Archiv: 5 Fragen offen“). The counts are known only while the vault is
// open; the last ones are kept outside it, with the time they were
// counted, so Homes can still show them while it is closed. They are not
// in laib-status.json, which reaches laib_status, the admin's view.
//
//	GET /intern/aufgaben   (X-Archiv-Homes) → AufgabenStand as JSON

// aufgabenNeu: a count unchanged is written again after this long, so
// Am says when it was last counted, not when it last changed.
const aufgabenNeu = 15 * time.Minute

// AufgabenStand is all of it; laib docs/DATA.md names these fields.
type AufgabenStand struct {
	Offen           bool       `json:"offen"`            // the vault is open: the counts are of now
	Am              *time.Time `json:"am,omitempty"`     // when they were counted; absent: never yet
	Fragen          int        `json:"fragen"`           // open questions and suggestions on the workplace (/tresor)
	Aufraeumen      int        `json:"aufraeumen"`       // what the Datenpfleger found (archiv#58)
	SchluesselFehlt bool       `json:"schluessel_fehlt"` // a key the archive waits for is missing (/tresor/quellen)
}

// Aufgaben keeps the last count in Datei (outside the vault).
type Aufgaben struct {
	Datei string
	mu    sync.Mutex
	letzt *AufgabenStand
}

// laden is the last count kept, or the zero one; a.mu is held.
func (a *Aufgaben) laden() AufgabenStand {
	if a.letzt == nil {
		var s AufgabenStand
		if b, err := os.ReadFile(a.Datei); err == nil {
			if err := json.Unmarshal(b, &s); err != nil {
				log.Printf("archiv: %s: %v", a.Datei, err)
			}
		}
		a.letzt = &s
	}
	return *a.letzt
}

// Gezaehlt keeps a count made with the vault open, at jetzt.
func (a *Aufgaben) Gezaehlt(s AufgabenStand, jetzt time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	alt := a.laden()
	if alt.Am != nil && alt.Fragen == s.Fragen && alt.Aufraeumen == s.Aufraeumen && alt.SchluesselFehlt == s.SchluesselFehlt && jetzt.Sub(*alt.Am) < aufgabenNeu {
		return
	}
	s.Offen, s.Am = false, &jetzt
	b, _ := json.Marshal(s)
	if err := schreibenAtomar(a.Datei, b, 0o600); err != nil {
		log.Printf("archiv: Aufgaben: %v", err)
	}
	a.letzt = &s
}

// Stand is the last count; offen marks it as of now.
func (a *Aufgaben) Stand(offen bool) AufgabenStand {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.laden()
	s.Offen = offen && s.Am != nil
	return s
}

// aufgabenZaehlen counts with the vault open and keeps it; closed, it
// keeps the last count. Called with every status.
func (w *Web) aufgabenZaehlen(st Stand) {
	if w.Aufgaben == nil || st.Zustand != Offen || !st.Bereit {
		return
	}
	s := AufgabenStand{SchluesselFehlt: len(w.fehlendeSchluessel(true)) > 0}
	if n := w.Umfeld.Netz(); n != nil {
		s.Fragen = len(n.Fragen()) + n.Erkannt(w.Umfeld.Stand(), w.Umfeld.modellDa()).Vorschlaege()
	}
	w.Aufgaben.Gezaehlt(s, w.jetzt())
}

// aufgaben answers /intern/aufgaben. A missing Signal key is known while
// closed too (the phone number lies outside the vault).
func (w *Web) aufgaben(rw http.ResponseWriter) {
	var s AufgabenStand
	if w.Aufgaben != nil {
		st := w.Tresor.Stand()
		s = w.Aufgaben.Stand(st.Zustand == Offen && st.Bereit)
	}
	s.SchluesselFehlt = s.SchluesselFehlt || w.fehlenSignalSchluessel()
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(s)
}

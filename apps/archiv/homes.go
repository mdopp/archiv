package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/mdopp/archiv/apps/archiv/tresor"
)

// The archive is Homes' first plugin (homes ADR 0003, archiv ADR 0001 §4,
// homes#375): Laib registers it with Homes, and Homes reads the contract
// here, with ARCHIV_HOMES_SECRET:
//
//	GET /homes/v1?person=<person>   (X-Homes-Plugin-Key) → contract 1 as JSON
//
// Its name, icon and link always; its tasks only while the vault is open,
// computed by the program „Aufgaben für Homes“ with its own key and only
// the areas it reads. They leave the vault with their text (the next
// question names people): the owner chose this on 2026-11-15, laib
// docs/DATA.md records it, and the person sees it when granting the
// program its areas. Closed, the answer has no tasks, and Homes keeps the
// ones it got last; the archive keeps nothing of them outside the vault.

const (
	homesVertragPfad = "/homes/v1"
	homesKeyHeader   = "X-Homes-Plugin-Key"
)

// homesAntwort is contract 1 (homes internal/plugin, testdata/vertrag-v1.json).
type homesAntwort struct {
	Vertrag  int             `json:"vertrag"`
	Name     string          `json:"name"`
	Icon     string          `json:"icon"`
	Link     string          `json:"link"`
	Zeile    string          `json:"zeile"`
	Aufgaben *[]homesAufgabe `json:"aufgaben,omitempty"`
}

type homesAufgabe struct {
	ID    string `json:"id"`
	Titel string `json:"titel"`
	Warum string `json:"warum,omitempty"`
	Link  string `json:"link"`
	Art   string `json:"art,omitempty"`
}

const (
	saysHomesZeile   = "Deine Nachrichten, Kontakte und Mails, gesichert und durchsuchbar"
	saysHomesFragen  = "Wer zu wem gehört: dein Archiv fragt nach."
	saysHomesPflege  = "Dein Archiv hat etwas zum Aufräumen gefunden."
	saysHomesNaechst = "Als Nächstes: "
)

func (w *Web) homesVertrag(rw http.ResponseWriter, r *http.Request) {
	if w.HomesSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get(homesKeyHeader)), []byte(w.HomesSecret)) != 1 {
		jsonFehler(rw, http.StatusUnauthorized, saysNichtAngemeldet)
		return
	}
	if r.Method != http.MethodGet {
		jsonFehler(rw, http.StatusMethodNotAllowed, "Nur GET.")
		return
	}
	if r.URL.Query().Get("person") != w.Person { // this archive is its person's only
		jsonFehler(rw, http.StatusForbidden, saysNichtDeins)
		return
	}
	basis := strings.TrimRight(w.PublicURL, "/")
	a := homesAntwort{Vertrag: 1, Name: "Dein Archiv", Icon: "archiv", Link: basis + "/tresor", Zeile: saysHomesZeile}
	if as, ok := w.homesAufgaben(basis); ok {
		a.Aufgaben = &as
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(a)
}

// homesAufgaben are the person's tasks, worked out in the open vault by
// the program „Aufgaben für Homes“: a missing key, the open questions and
// suggestions (Beziehungen), what the Datenpfleger found (Kontakte,
// Notizen); one row each with its count and the next one's text. false
// while the vault is closed or the program has no grant: Homes keeps
// what it had.
func (w *Web) homesAufgaben(basis string) ([]homesAufgabe, bool) {
	innen, auf := w.P.Aufgaben.offen()
	if !auf {
		return nil, false
	}
	out := []homesAufgabe{}
	if len(w.fehlendeSchluessel(true)) > 0 {
		out = append(out, homesAufgabe{ID: "schluessel", Titel: "Im Archiv fehlt ein Schlüssel", Warum: "Ohne ihn kommt nicht alles in dein Archiv.",
			Link: basis + "/tresor/quellen", Art: "schluessel"})
	}
	n := w.Umfeld.NetzFuer(innen)
	if n != nil {
		fs := n.Fragen()
		if zahl := len(fs) + n.Erkannt(w.Umfeld.Stand(), w.Umfeld.modellDa()).Vorschlaege(); zahl > 0 {
			warum := saysHomesFragen
			if len(fs) > 0 {
				warum = saysHomesNaechst + frageSatz(fs[0])
			}
			out = append(out, homesAufgabe{ID: "fragen", Titel: "Im Archiv: " + anzahl(zahl, "Frage", "Fragen") + " offen", Warum: warum, Link: basis + "/tresor"})
		}
	}
	if innen.darf(tresor.Kontakte, tresor.Lesen) && innen.darf(tresor.Notizen, tresor.Lesen) {
		if zahl, erst := w.pflege.zaehlen(innen, n, w.jetzt(), false), w.pflege.erster(); zahl > 0 {
			warum := saysHomesPflege
			if erst != "" {
				warum = saysHomesNaechst + erst
			}
			out = append(out, homesAufgabe{ID: "aufraeumen", Titel: anzahl(zahl, "Ding", "Dinge") + " zum Aufräumen im Archiv", Warum: warum, Link: basis + "/tresor/pflege"})
		}
	}
	return out, true
}

// frageSatz is a question as one line: „Wie steht Ben zu Clara?“.
func frageSatz(f Frage) string {
	switch {
	case f.Gruppe != nil:
		return "Wie würdest du die Gruppe „" + f.Gruppe.Titel + "“ nennen?"
	case f.Kennen:
		return "Kennen sich " + f.X.Name + " und " + f.Y.Name + "?"
	case f.Ich:
		return "Wie steht " + f.X.Name + " zu dir?"
	}
	return "Wie steht " + f.X.Name + " zu " + f.Y.Name + "?"
}

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// The status file laib reads (laib docs/STATE.md: at, detail, findings;
// further fields reach laib_status as eigenes). It lies outside the vault
// and holds nothing secret: the state, what waits in the inbox and in the
// Briefkasten, when what was packed and imported, the last error as a
// sentence.

const wartenZuLange = 3 * 24 * time.Hour

type befund struct {
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix"`
}

type quelleStatus struct {
	Wartend      int        `json:"wartend"`
	AeltesteSeit *time.Time `json:"aelteste_seit,omitempty"`
}

type briefkastenStatus struct {
	Wartend   int        `json:"wartend"`
	Aeltestes *time.Time `json:"aeltestes,omitempty"`
}

type statusDatei struct {
	At            string                  `json:"at"`
	Detail        string                  `json:"detail"`
	Findings      []befund                `json:"findings,omitempty"`
	Person        string                  `json:"person"`
	Zustand       Zustand                 `json:"zustand"`
	Bereit        bool                    `json:"bereit,omitempty"`
	Wartend       map[string]quelleStatus `json:"wartend"`
	Briefkasten   briefkastenStatus       `json:"briefkasten"`
	LetztesPaket  map[string]Gepackt      `json:"letztes_paket"`
	LetzterImport map[string]Eingelesen   `json:"letzter_import"`
	LetzterFehler *Fehler                 `json:"letzter_fehler,omitempty"`
	Packt         string                  `json:"packt,omitempty"`   // the backup being packed now
	Liefert       string                  `json:"liefert,omitempty"` // the packet going into msgvault now
}

// Wartende counts the backups per source that wait in the inbox.
func wartende(e *Eingang) map[string]quelleStatus {
	out := map[string]quelleStatus{}
	for _, q := range Quellen {
		l, err := e.Liste(q)
		if err != nil {
			log.Printf("archiv: Eingang %s: %v", q, err)
		}
		qs := quelleStatus{Wartend: len(l)}
		for _, d := range l {
			if qs.AeltesteSeit == nil || d.Seit.Before(*qs.AeltesteSeit) {
				s := d.Seit
				qs.AeltesteSeit = &s
			}
		}
		out[q] = qs
	}
	return out
}

func briefkastenStand(post string) briefkastenStatus {
	p := Pakete(post)
	s := briefkastenStatus{Wartend: len(p)}
	if len(p) > 0 {
		if t, ok := paketZeit(p[0]); ok {
			s.Aeltestes = &t
		}
	}
	return s
}

func datum(t time.Time) string { return t.Local().Format("02.01.2006 15:04") }

// Lage is everything the status is made of.
type Lage struct {
	Person           string
	Stand            Stand
	Wartend          map[string]quelleStatus
	Briefkasten      briefkastenStatus
	Protokoll        *Protokoll
	Packt, Liefert   string
	FehlenSchluessel bool // Signal backups wait for the passphrase or the number
	URL              string
	Jetzt            time.Time
}

func statusBauen(l Lage) statusDatei {
	ein, fehler, zuSeit := l.Protokoll.Kopie()
	st := l.Stand
	s := statusDatei{At: l.Jetzt.Format(time.RFC3339), Person: l.Person, Zustand: st.Zustand, Bereit: st.Bereit,
		Wartend: l.Wartend, Briefkasten: l.Briefkasten, LetztesPaket: l.Protokoll.Pakete(), LetzterImport: ein,
		LetzterFehler: fehler, Packt: l.Packt, Liefert: l.Liefert}
	wo := "unter " + l.URL
	if l.URL == "" {
		wo = "im Archiv"
	}
	signal := l.Wartend["signal"]
	var teile []string
	switch st.Zustand {
	case Uneingerichtet:
		teile = append(teile, "Noch nicht eingerichtet")
	case Verschlossen:
		teile = append(teile, "Abgeschlossen")
	case Offen:
		if st.Bereit {
			teile = append(teile, "Offen")
		} else {
			teile = append(teile, "Öffnet")
		}
	}
	if n := l.Briefkasten.Wartend; n > 0 {
		teile = append(teile, anzahl(n, "Paket wartet", "Pakete warten")+" im Briefkasten")
	}
	if signal.Wartend > 0 {
		teile = append(teile, anzahl(signal.Wartend, "Signal-Sicherung wartet", "Signal-Sicherungen warten")+" im Eingang")
	}
	for _, q := range []string{"whatsapp", "discord"} {
		if n := l.Wartend[q].Wartend; n > 0 {
			teile = append(teile, anzahl(n, quellenName[q]+"-Datei wartet", quellenName[q]+"-Dateien warten")+" (noch kein Import)")
		}
	}
	if e, ok := ein["signal"]; ok {
		teile = append(teile, fmt.Sprintf("Signal zuletzt am %s ins Archiv übernommen (%d Nachrichten)", datum(e.Am), e.Zahlen.Nachrichten()))
	}
	if l.Packt != "" {
		teile = append(teile, "packt gerade "+l.Packt)
	}
	if l.Liefert != "" {
		teile = append(teile, "übernimmt gerade "+l.Liefert)
	}
	s.Detail = strings.Join(teile, "; ")

	// packets waiting longer than three days while the archive is not open
	if st.Zustand != Offen && l.Briefkasten.Wartend > 0 && l.Briefkasten.Aeltestes != nil && l.Jetzt.Sub(*l.Briefkasten.Aeltestes) > wartenZuLange {
		pakete := anzahl(l.Briefkasten.Wartend, "Paket mit neuen Nachrichten wartet", "Pakete mit neuen Nachrichten warten")
		detail := fmt.Sprintf("Dein Archiv ist zu, %s seit %s.", pakete, datum(*l.Briefkasten.Aeltestes))
		if zuSeit != nil {
			detail = fmt.Sprintf("Dein Archiv ist seit %s zu, %s.", datum(*zuSeit), pakete)
		}
		s.Findings = append(s.Findings, befund{Severity: "gelb", Detail: detail, Fix: "Öffne dein Archiv " + wo + " mit deinem Archiv-Passwort; dann übernimmt es die Pakete."})
	}
	if st.Zustand == Uneingerichtet && signal.Wartend > 0 && signal.AeltesteSeit != nil && l.Jetzt.Sub(*signal.AeltesteSeit) > wartenZuLange {
		s.Findings = append(s.Findings, befund{Severity: "gelb", Detail: fmt.Sprintf("Dein Archiv ist noch nicht eingerichtet, %s.", anzahl(signal.Wartend, "Sicherung wartet", "Sicherungen warten")),
			Fix: "Richte dein Archiv " + wo + " ein."})
	}
	if l.FehlenSchluessel && st.Zustand != Uneingerichtet {
		s.Findings = append(s.Findings, befund{Severity: "gelb", Detail: "Signal-Sicherungen warten, aber die Signal-Passphrase oder deine Handynummer fehlt.",
			Fix: "Öffne dein Archiv " + wo + " und gib sie unter „Schlüssel“ ein."})
	}
	if fehler != nil {
		fix := "Sag dem Admin Bescheid (laib logs)."
		if fehler.Satz == saysSignalFalsch {
			fix = "Öffne dein Archiv " + wo + " und gib die Signal-Passphrase unter „Schlüssel“ neu ein."
		}
		s.Findings = append(s.Findings, befund{Severity: "gelb", Detail: fehler.Satz, Fix: fix})
	}
	if st.Fehler != "" {
		s.Findings = append(s.Findings, befund{Severity: "gelb", Detail: st.Fehler, Fix: "laib logs"})
	}
	return s
}

func statusSchreiben(datei string, s statusDatei) {
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := schreibenAtomar(datei, b, 0o644); err != nil {
		log.Printf("archiv: status: %v", err)
	}
}

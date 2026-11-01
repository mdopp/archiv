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
// and holds nothing secret: the state, how many backups wait, when what
// was imported, the last error as a sentence.

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

type statusDatei struct {
	At            string                  `json:"at"`
	Detail        string                  `json:"detail"`
	Findings      []befund                `json:"findings,omitempty"`
	Person        string                  `json:"person"`
	Zustand       Zustand                 `json:"zustand"`
	Bereit        bool                    `json:"bereit,omitempty"`
	Wartend       map[string]quelleStatus `json:"wartend"`
	LetzterImport map[string]Eingelesen   `json:"letzter_import"`
	LetzterFehler *Fehler                 `json:"letzter_fehler,omitempty"`
	Laeuft        string                  `json:"laeuft,omitempty"`
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

func datum(t time.Time) string { return t.Local().Format("02.01.2006 15:04") }

// statusBauen is the whole status from what the parts know.
func statusBauen(person string, st Stand, w map[string]quelleStatus, prot *Protokoll, laeuft string, keinSchluessel bool, url string, jetzt time.Time) statusDatei {
	ein, fehler, zuSeit := prot.Kopie()
	s := statusDatei{At: jetzt.Format(time.RFC3339), Person: person, Zustand: st.Zustand, Bereit: st.Bereit,
		Wartend: w, LetzterImport: ein, LetzterFehler: fehler, Laeuft: laeuft}
	wo := "unter " + url
	if url == "" {
		wo = "im Archiv"
	}
	signal := w["signal"]
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
	if signal.Wartend > 0 {
		teile = append(teile, fmt.Sprintf("%d Signal-Sicherungen warten", signal.Wartend))
	}
	for _, q := range []string{"whatsapp", "discord"} {
		if n := w[q].Wartend; n > 0 {
			teile = append(teile, fmt.Sprintf("%d %s-Dateien warten (noch kein Import)", n, quellenName[q]))
		}
	}
	if e, ok := ein["signal"]; ok {
		teile = append(teile, fmt.Sprintf("Signal zuletzt am %s eingelesen (%d Nachrichten)", datum(e.Am), e.Zahlen.Nachrichten()))
	}
	if laeuft != "" {
		teile = append(teile, "liest gerade "+laeuft)
	}
	s.Detail = strings.Join(teile, "; ")

	// a backup waiting longer than three days while the archive is not open
	if st.Zustand != Offen && signal.Wartend > 0 && signal.AeltesteSeit != nil && jetzt.Sub(*signal.AeltesteSeit) > wartenZuLange {
		var detail string
		switch {
		case st.Zustand == Uneingerichtet:
			detail = fmt.Sprintf("Dein Archiv ist noch nicht eingerichtet, %d Sicherungen warten.", signal.Wartend)
		case zuSeit != nil:
			detail = fmt.Sprintf("Dein Archiv ist seit %s zu, %d Sicherungen warten.", datum(*zuSeit), signal.Wartend)
		default:
			detail = fmt.Sprintf("Dein Archiv ist zu, %d Sicherungen warten seit %s.", signal.Wartend, datum(*signal.AeltesteSeit))
		}
		s.Findings = append(s.Findings, befund{Severity: "gelb", Detail: detail, Fix: "Öffne dein Archiv " + wo + " mit deinem Archiv-Passwort; dann liest es die Sicherungen ein."})
	}
	if st.Zustand == Offen && keinSchluessel {
		s.Findings = append(s.Findings, befund{Severity: "gelb", Detail: "Signal-Sicherungen warten, aber die Signal-Passphrase oder deine Telefonnummer fehlt.",
			Fix: "Gib sie " + wo + " unter „Schlüssel“ ein."})
	}
	if fehler != nil {
		fix := "Sag dem Admin Bescheid (laib logs)."
		if fehler.Satz == saysSignalFalsch {
			fix = "Gib die Signal-Passphrase " + wo + " unter „Schlüssel“ neu ein."
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

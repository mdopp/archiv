package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/mdopp/archiv/apps/archiv/tresor"
)

// The programs (archiv ADR 0001 §2): today's features, each with the
// areas it asks for. They still run in this process (archiv#68 is step
// 1); each holds only its own key, so a feature reaches only what the
// person granted its program for this session. Moving one out into its
// own program changes this list, never the vault.

const (
	progImporte      = "importe"
	progNachrichten  = "nachrichten"
	progStammbaum    = "stammbaum"
	progDatenpfleger = "datenpfleger"
)

const (
	l  = tresor.Lesen
	s  = tresor.Schreiben
	ls = tresor.Lesen | tresor.Schreiben
)

type programm struct {
	id, name, wozu string
	will           tresor.Rechte // the most the person can grant it
}

var programme = []programm{
	{progImporte, "Importe", "liest deine Sicherungen, Mails, Kontakte und Kalender ins Archiv ein und räumt alte Mails beim Anbieter auf",
		tresor.Rechte{tresor.Nachrichten: ls, tresor.Kontakte: ls}},
	{progNachrichten, "Nachrichten", "zeigt und durchsucht deine Nachrichten und fragt, ob zwei dieselbe Person sind",
		tresor.Rechte{tresor.Nachrichten: ls, tresor.Kontakte: l}},
	{progStammbaum, "Umfeld und Stammbaum", "rechnet aus, wer dir nahesteht, stellt die Fragen dazu und zeichnet den Stammbaum",
		tresor.Rechte{tresor.Beziehungen: ls, tresor.Kontakte: l, tresor.Nachrichten: l, tresor.Fotos: ls}},
	{progDatenpfleger, "Datenpfleger", "findet, was in Kontakten, Kalendern und Fotos aufzuräumen ist, und merkt sich dein „Passt so“",
		tresor.Rechte{tresor.Notizen: ls, tresor.Kontakte: l, tresor.Beziehungen: l}},
}

// bereichNamen are the areas as the page says them.
var bereichNamen = map[tresor.Bereich]string{
	tresor.Nachrichten: "Nachrichten", tresor.Kontakte: "Kontakte", tresor.Beziehungen: "Beziehungen",
	tresor.Fotos: "Fotos", tresor.Notizen: "Notizen",
}

// Programme are the programs' ways into the vault.
type Programme struct {
	Importe, Nachrichten, Stammbaum, Datenpfleger Innen
}

func neueProgramme(t *Tresor) Programme {
	return Programme{
		Importe:      Innen{t.Programm(progImporte)},
		Nachrichten:  Innen{t.Programm(progNachrichten)},
		Stammbaum:    Innen{t.Programm(progStammbaum)},
		Datenpfleger: Innen{t.Programm(progDatenpfleger)},
	}
}

// alleWuensche is every program with all it asks for: the grant of a
// first opening, until the person chooses otherwise.
func alleWuensche() tresor.Freigaben {
	f := tresor.Freigaben{}
	for _, p := range programme {
		r := tresor.Rechte{}
		for b, x := range p.will {
			r[b] = x
		}
		f[p.id] = r
	}
	return f
}

// beschnitten is f within what each program asks for: a form cannot
// grant a program more, nor a program that does not exist.
func beschnitten(f tresor.Freigaben) tresor.Freigaben {
	out := tresor.Freigaben{}
	for _, p := range programme {
		r := tresor.Rechte{}
		for b, x := range f[p.id] {
			if y := x & p.will[b]; y != 0 {
				r[b] = y
			}
		}
		if len(r) > 0 {
			out[p.id] = r
		}
	}
	return out
}

// Freigabenwahl keeps the person's last choice outside the vault (which
// program may which area: no data of hers), so the next opening offers
// it again. Datei "" keeps nothing.
type Freigabenwahl struct {
	Datei string
	mu    sync.Mutex
}

// Letzte is the last choice, or every program with all it asks for.
func (fw *Freigabenwahl) Letzte() tresor.Freigaben {
	if fw == nil || fw.Datei == "" {
		return alleWuensche()
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	b, err := os.ReadFile(fw.Datei)
	if err != nil {
		return alleWuensche()
	}
	var f tresor.Freigaben
	if err := json.Unmarshal(b, &f); err != nil {
		log.Printf("archiv: %s: %v (alle Programme wie gewünscht)", fw.Datei, err)
		return alleWuensche()
	}
	return beschnitten(f)
}

func (fw *Freigabenwahl) merken(f tresor.Freigaben) {
	if fw == nil || fw.Datei == "" {
		return
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	b, _ := json.Marshal(f)
	if err := schreibenAtomar(fw.Datei, b, 0o600); err != nil {
		log.Printf("archiv: Freigaben merken: %v", err)
	}
}

// ausFormular is the grant of the opening form: a box per program, area
// and right (f.<program>.<area>.l / .s). A form without the boxes (Homes'
// button, an old page) grants what was granted last time.
func (fw *Freigabenwahl) ausFormular(r *http.Request) tresor.Freigaben {
	if r.PostFormValue("freigaben") == "" {
		return fw.Letzte()
	}
	f := tresor.Freigaben{}
	for _, p := range programme {
		rr := tresor.Rechte{}
		for b, x := range p.will {
			var y tresor.Recht
			if x.Hat(l) && r.PostFormValue("f."+p.id+"."+string(b)+".l") != "" {
				y |= l
			}
			if x.Hat(s) && r.PostFormValue("f."+p.id+"."+string(b)+".s") != "" {
				y |= s
			}
			if y != 0 {
				rr[b] = y
			}
		}
		f[p.id] = rr
	}
	f = beschnitten(f)
	fw.merken(f)
	return f
}

// lauf is a program's background work while the vault is open.
type lauf struct {
	i Innen
	f func(context.Context, Innen)
}

// laufen runs them side by side, each with its own program's key, all
// ending with the vault; a program the person granted nothing in this
// session does not run.
func laufen(ls ...lauf) func(context.Context) {
	return func(ctx context.Context) {
		var wg sync.WaitGroup
		for _, l := range ls {
			if l.i.auf() {
				wg.Add(1)
				go func() { defer wg.Done(); l.f(ctx, l.i) }()
			}
		}
		wg.Wait()
	}
}

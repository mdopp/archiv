package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"
)

// Verstorben (owner 2026-11-11): the person marks a contact as deceased,
// one tap on its page, undone the same way. The archive then asks
// nothing about her (no „Kennen sich …?“, no relation hint, no place in
// the tree, no town, employer or birthday), and „Lange nichts gehört“
// never names her. She stays in the net and the tree, a plain „†“ after
// her name (Netz.Anzeige). Kept in the vault: umfeld/verstorben.json.

const verstorbenZeichen = " †"

func (i Innen) umfeldVerstorben() string { return filepath.Join(i.Umfeld(), "verstorben.json") }

// markeLaden is a mark of nodes kept in the vault (ruhen, verstorben).
func markeLaden(pfad string) map[string]time.Time {
	out := map[string]time.Time{}
	if b, err := os.ReadFile(pfad); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// marke sets (an) or clears id in the mark feld names and keeps the mark
// in the open vault at pfad.
func (u *Umfeld) marke(pfad, id string, an bool, feld func(*Netz) *map[string]time.Time) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	n.mu.Lock()
	m := feld(n)
	if *m == nil {
		*m = map[string]time.Time{}
	}
	if an {
		(*m)[id] = n.now()
	} else {
		delete(*m, id)
	}
	alle := maps.Clone(*m)
	n.totMarken()
	n.mu.Unlock()
	return jsonImTresor(pfad, alle)
}

// Verstorben keeps in the open vault that id has died (an), or not.
func (u *Umfeld) Verstorben(innen Innen, id string, an bool) error {
	return u.marke(innen.umfeldVerstorben(), id, an, func(n *Netz) *map[string]time.Time { return &n.verstorben })
}

// totMarken sets each node's Tot by verstorben; n.mu is held or n is new.
func (n *Netz) totMarken() {
	for id, k := range n.knoten {
		k.Tot = !n.verstorben[id].IsZero()
		n.knoten[id] = k
	}
}

// befragbar: the archive may ask about id: fragbar, and not deceased; n.mu is held.
func (n *Netz) befragbar(id string) bool {
	return n.fragbar(id) && !n.knoten[id].Tot
}

// mitKreuz is a name as a page shows it: „Opa Karl †“ for one deceased.
func (k Knoten) mitKreuz() string {
	if k.Tot {
		return k.Name + verstorbenZeichen
	}
	return k.Name
}

// verstorbenZeile is the person page's one tap: „Ist verstorben“, or for
// one marked „Verstorben“ and the tap that takes it back.
func verstorbenZeile(k Knoten) string {
	tu, text, satz := "verstorben", "Ist verstorben", ""
	if k.Tot {
		tu, text, satz = "lebt", "Markierung entfernen", "† Verstorben. Das Archiv fragt nichts mehr zu "+k.Name+". "
	}
	return fmt.Sprintf(`<form method=post action="%s" class=knoepfe id=verstorben><input type=hidden name=id value="%s"><p class=k>%s<button class=k name=tu value=%s data-laeuft="Merkt es sich …">%s</button></p></form>`,
		personPfad, esc(k.ID), esc(satz), tu, esc(text))
}

func (p personForm) verstorben(an bool) {
	if err := p.w.Umfeld.Verstorben(p.innen, p.id, an); err != nil {
		p.w.antwortFehler(p.rw, p.r, p.zurueck, err)
		return
	}
	p.weiter(umfeldMitte(p.id) + "&m=" + map[bool]string{true: "verstorben", false: "lebt"}[an])
}

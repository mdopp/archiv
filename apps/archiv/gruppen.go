package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Group categories (archiv#27, owner 2026-11-11: „Vorher sollten aktive
// Gruppen vom Benutzer kategorisiert werden: Verein, Freundeskreis,
// Familie, Arbeit, Klasse, Elterngruppe, Nachbarschaft, Reisegruppe,
// Politik, ... Was schon Brotkrümel für die Art der Beziehung sein
// können/sollten.“). An active group chat — one that counts (gespraeche.go:
// 3 to 30 members), has a title, two members the phone books know and a
// message in the last gruppeAktiv — is a question of its own: „Wie würdest
// du die Gruppe „…“ nennen?“, its title and its members' initials, never a
// message. Its title may suggest the category (beziehungswoerter.yml,
// gruppe). One tap keeps the category in the vault (umfeld/gruppen.json);
// it is never written to an address book.
//
// What a category does (breadcrumbs, never a relation by itself):
//   - Familie: its members join the family circle (fragen.go), so pairs
//     among them are asked; a pair in it gets „Sonstige verwandt“ as a
//     suggestion and „Familie (Gruppe „…“)“ as a reason.
//   - Arbeit: „Kollege/Kollegin (Gruppe „…“)“ as a reason and a suggestion.
//   - Elterngruppe, Klasse: „Eltern aus „…““, „aus der Klasse „…““.
//   - Nachbarschaft, Freundeskreis: Nachbar/in, Freund/in as reason and
//     suggestion; Verein, Reisegruppe, Politik a reason.
// A title's suggestion counts as the category for reasons and suggestions
// until the person answers; only the person's own Familie widens the circle.

const (
	gruppeAktiv      = 365 * 24 * time.Hour // a group with no message for longer is not asked
	kategorieFamilie = "familie"
	kategorieSonst   = "sonstiges"
)

// kategorien are the chips of a group question, in their order.
var kategorien = []string{kategorieFamilie, "freundeskreis", "verein", "arbeit", "klasse", "elterngruppe", "nachbarschaft", "reisegruppe", "politik", kategorieSonst}

var kategorieWorte = map[string]string{kategorieFamilie: "Familie", "freundeskreis": "Freundeskreis", "verein": "Verein", "arbeit": "Arbeit",
	"klasse": "Klasse", "elterngruppe": "Elterngruppe", "nachbarschaft": "Nachbarschaft", "reisegruppe": "Reisegruppe", "politik": "Politik",
	kategorieSonst: "Sonstiges"}

// kategorieArt is the kind a category suggests for a pair in the group.
var kategorieArt = map[string]string{kategorieFamilie: "verwandt", "arbeit": "kollege", "nachbarschaft": "nachbar", "freundeskreis": "freund"}

// gruppenAntwort is the person's category of a group, or Später.
type gruppenAntwort struct {
	Kategorie string    `json:"k"`
	Am        time.Time `json:"am"`
}

// GruppenFrage is „Wie würdest du die Gruppe „Titel“ nennen?“: the
// conversation's id, its title, the members the phone books know, and the
// category its title suggests („“ none) with the word that says so.
type GruppenFrage struct {
	ID, Titel       string
	Mitglieder      []Knoten
	Vorschlag, Wort string
	zuletzt         time.Time
}

func gruppeKey(id string) string { return "gruppe:" + id }

func (i Innen) umfeldGruppen() string { return filepath.Join(i.Umfeld(), "gruppen.json") }

func gruppenLaden(innen Innen) map[string]gruppenAntwort {
	out := map[string]gruppenAntwort{}
	if b, err := os.ReadFile(innen.umfeldGruppen()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// kategorieVon is the group's category: the person's (gesagt), else its
// title's; "" none (Sonstiges and Später are none). n.mu is held.
func (n *Netz) kategorieVon(id string) (kat string, gesagt bool) {
	if a, da := n.gruppen[id]; da && a.Kategorie != artSpaeter {
		return map[bool]string{true: a.Kategorie}[a.Kategorie != kategorieSonst], true
	}
	if g, da := n.gespraech[id]; da {
		_, kat := woerter.imTitel(g.Titel)
		return kat, false
	}
	return "", false
}

// mitglieder are the group's members as shown nodes, by name; n.mu is held.
func (n *Netz) mitglieder(g netzGespraech) (out []Knoten) {
	for _, key := range g.Wer {
		if id := n.von[key]; id != "" && n.sichtbar(id) && !slices.ContainsFunc(out, func(k Knoten) bool { return k.ID == id }) {
			out = append(out, n.knoten[id])
		}
	}
	slices.SortFunc(out, func(a, b Knoten) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// gruppenFragen are the active groups not yet named (Später included),
// the newest first. n.mu is held.
func (n *Netz) gruppenFragen() (out []Frage) {
	gs := slices.SortedFunc(maps.Values(n.gespraech), func(a, b netzGespraech) int {
		return cmp.Or(b.Zuletzt.Compare(a.Zuletzt), strings.Compare(a.ID, b.ID))
	})
	for _, g := range gs {
		if g.Art != gespraechChat || !g.zaehlt() || strings.TrimSpace(g.Titel) == "" || n.now().Sub(g.Zuletzt) > gruppeAktiv {
			continue
		}
		if a, da := n.gruppen[g.ID]; da && a.Kategorie != artSpaeter {
			continue
		}
		wer := n.mitglieder(g)
		if len(wer) < 2 {
			continue
		}
		wort, kat := woerter.imTitel(g.Titel)
		out = append(out, Frage{Gruppe: &GruppenFrage{ID: g.ID, Titel: g.Titel, Mitglieder: wer, Vorschlag: kat, Wort: wort, zuletzt: g.Zuletzt}})
	}
	return out
}

// Einordnen keeps the person's category of the group id (or Später); the
// group must be asked now. It answers the question as it was asked and the
// categories as they now are, for the vault.
func (n *Netz) Einordnen(id, kat string) (Frage, map[string]gruppenAntwort, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	fs := n.gruppenFragen()
	i := slices.IndexFunc(fs, func(f Frage) bool { return f.Gruppe.ID == id })
	if i < 0 || kat != artSpaeter && !slices.Contains(kategorien, kat) {
		return Frage{}, nil, errFrageWeg
	}
	n.gruppen[id] = gruppenAntwort{Kategorie: kat, Am: n.now()}
	return fs[i], maps.Clone(n.gruppen), nil
}

// Ausordnen forgets the category of the group id; its question may come again.
func (n *Netz) Ausordnen(id string) (map[string]gruppenAntwort, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, da := n.gruppen[id]; !da {
		return nil, errFrageWeg
	}
	delete(n.gruppen, id)
	return maps.Clone(n.gruppen), nil
}

// Eingeordnet is a group the person named, as the page lists it.
type Eingeordnet struct {
	ID, Titel, Kategorie string
	Am                   time.Time
}

// Eingeordnete are the groups the person named (no Später), the newest first.
func (n *Netz) Eingeordnete() (out []Eingeordnet) {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, a := range n.gruppen {
		if a.Kategorie == artSpaeter {
			continue
		}
		out = append(out, Eingeordnet{ID: id, Titel: cmp.Or(n.gespraech[id].Titel, "eine Gruppe"), Kategorie: a.Kategorie, Am: a.Am})
	}
	slices.SortFunc(out, func(a, b Eingeordnet) int { return cmp.Or(b.Am.Compare(a.Am), strings.Compare(a.ID, b.ID)) })
	return out
}

// Eingeordnetes is the group id as the person named it.
func (n *Netz) Eingeordnetes(id string) (Eingeordnet, bool) {
	for _, e := range n.Eingeordnete() {
		if e.ID == id {
			return e, true
		}
	}
	return Eingeordnet{}, false
}

// eingeordnetSatz is a named group as a sentence: „„Familie Dopp“ ist für dich: Familie.“
func eingeordnetSatz(e Eingeordnet) string {
	return fmt.Sprintf("„%s“ ist für dich: %s.", e.Titel, kategorieWorte[e.Kategorie])
}

// familienGruppen are the members of the groups the person named Familie;
// n.mu is held.
func (n *Netz) familienGruppen() (out []string) {
	for id, a := range n.gruppen {
		if a.Kategorie != kategorieFamilie {
			continue
		}
		for _, k := range n.mitglieder(n.gespraech[id]) {
			out = append(out, k.ID)
		}
	}
	return out
}

// gruppenGrund is a reason a category gives, „“ none.
func gruppenGrund(kat, titel string) string {
	t := "„" + titel + "“"
	switch kat {
	case kategorieFamilie:
		return "Familie (Gruppe " + t + ")"
	case "arbeit":
		return kurzWort("kollege") + " (Gruppe " + t + ")"
	case "elterngruppe":
		return "Eltern aus " + t
	case "klasse":
		return "aus der Klasse " + t
	case "verein":
		return "im Verein " + t
	case "freundeskreis":
		return "Freundeskreis " + t
	case "nachbarschaft":
		return kurzWort("nachbar") + " (Gruppe " + t + ")"
	case "reisegruppe":
		return "Reisegruppe " + t
	case "politik":
		return "Politik (Gruppe " + t + ")"
	}
	return ""
}

// gruppenGruende are the reasons of the tie's named groups, at most two,
// the person's own categories first. n.mu is held.
func (n *Netz) gruppenGruende(k Kante) (out []string) {
	type grund struct {
		satz   string
		gesagt bool
	}
	var gs []grund
	for _, id := range k.Gruppen {
		kat, gesagt := n.kategorieVon(id)
		if s := gruppenGrund(kat, n.gespraech[id].Titel); s != "" && !slices.ContainsFunc(gs, func(g grund) bool { return g.satz == s }) {
			gs = append(gs, grund{s, gesagt})
		}
	}
	slices.SortStableFunc(gs, func(a, b grund) int {
		return cmp.Compare(map[bool]int{true: 0, false: 1}[a.gesagt], map[bool]int{true: 0, false: 1}[b.gesagt])
	})
	for _, g := range gs[:min(2, len(gs))] {
		out = append(out, g.satz)
	}
	return out
}

// gruppenVorschlag is the kind the pair's shared groups suggest, and why:
// the person's own categories before a title's, Familie first. n.mu is held.
func (n *Netz) gruppenVorschlag(k *Kante) (string, string) {
	beste, weil, rang := "", "", 99
	for _, id := range k.Gruppen {
		kat, gesagt := n.kategorieVon(id)
		art := kategorieArt[kat]
		if art == "" {
			continue
		}
		r := slices.Index(kategorien, kat)
		if !gesagt {
			r += len(kategorien)
		}
		if r < rang {
			t := "„" + n.gespraech[id].Titel + "“"
			beste, rang = art, r
			weil = "beide in der Gruppe " + t
			if gesagt {
				weil += " (für dich: " + kategorieWorte[kat] + ")"
			}
		}
	}
	return beste, weil
}

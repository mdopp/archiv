package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A person made by hand (owner 2026-11-11): a name at „Zu wem?“ that
// matches nobody becomes, in one tap on a kind, a person who lives only in
// the vault (umfeld/angelegt.json, never a card in an address book) and her
// relation; Rückgängig takes back both. Her page renames or deletes her.
// When a phone-book card of the same name turns up, the archive asks once
// „Ist <Name> aus deinem Telefonbuch dieselbe Person?“: Ja moves her
// relations to that card's person, Nein keeps both and is not asked again.
// Never merged without that yes.

const angelegtPrefix = "hand:"

// handPerson is a person made by hand: her name, when (the instant of the
// relation made with her: the toast knows her as new by it), and the
// phone-book persons she is not (Nein).
type handPerson struct {
	Name  string    `json:"name"`
	Am    time.Time `json:"am"`
	Nicht []string  `json:"nicht,omitempty"`
}

func (i Innen) umfeldAngelegt() string { return filepath.Join(i.Umfeld(), "angelegt.json") }

func angelegtLaden(innen Innen) map[string]handPerson {
	out := map[string]handPerson{}
	if b, err := os.ReadFile(innen.umfeldAngelegt()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func istAngelegt(id string) bool { return strings.HasPrefix(id, angelegtPrefix) }

// angelegtSetzen gives the net the persons made by hand.
func (n *Netz) angelegtSetzen(ps map[string]handPerson) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.angelegt = ps
	n.bauen()
}

// angelegtBauen makes them nodes; n.mu is held or n is new.
func (n *Netz) angelegtBauen() {
	for id, p := range n.angelegt {
		n.knoten[id] = Knoten{ID: id, Name: p.Name}
	}
}

// Angelegt is the person made by hand of id.
func (n *Netz) Angelegt(id string) (handPerson, bool) {
	if n == nil {
		return handPerson{}, false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	p, ok := n.angelegt[id]
	return p, ok
}

// angelegtFertig is the net's state after a change: n.mu is held.
func (n *Netz) angelegtFertig() (map[string]antwort, map[string]handPerson, error) {
	n.bauen()
	return maps.Clone(n.antworten), maps.Clone(n.angelegt), nil
}

// Anlegen makes the person name and her relation: she is art of y. The
// answer about the pair is its key.
func (n *Netz) Anlegen(name, y, art string) (string, map[string]antwort, map[string]handPerson, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	name = strings.Join(strings.Fields(name), " ")
	if n.selbst[y] {
		y = n.ich
	}
	if klar(name) == "" || !slices.Contains(arten, art) || !n.fragbar(y) {
		return "", nil, nil, errFrageWeg
	}
	zufall := make([]byte, 8)
	rand.Read(zufall)
	id, am := angelegtPrefix+hex.EncodeToString(zufall), n.now()
	if n.angelegt == nil {
		n.angelegt = map[string]handPerson{}
	}
	n.angelegt[id] = handPerson{Name: name, Am: am}
	n.bauen()
	vorher := map[string]bool{}
	for _, a := range n.abgeleitete() {
		vorher[paarKey(a.X, a.Y)] = true
	}
	key := paarKey(id, y)
	n.antworten[key] = antwort{X: id, Art: art, Am: am}
	n.folgern(key, vorher)
	alle, ps, err := n.angelegtFertig()
	return key, alle, ps, err
}

// ihreSchluessel are the answers' keys about id; n.mu is held.
func (n *Netz) ihreSchluessel(id string) (out []string) {
	for key := range n.antworten {
		if x, y, _ := strings.Cut(key, "|"); x == id || y == id {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

// Ihre are the keys of the answers about id.
func (n *Netz) Ihre(id string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ihreSchluessel(id)
}

// AngelegtWeg deletes the person id and every answer about her, and what
// followed from them; leer: only when no answer is left about her (the
// Rückgängig of the tap that made her).
func (n *Netz) AngelegtWeg(id string, leer bool) (map[string]antwort, map[string]handPerson, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, da := n.angelegt[id]; !da {
		return nil, nil, errFrageWeg
	}
	keys := n.ihreSchluessel(id)
	if leer && len(keys) > 0 {
		return maps.Clone(n.antworten), maps.Clone(n.angelegt), nil
	}
	for _, key := range keys {
		delete(n.antworten, key)
		maps.DeleteFunc(n.antworten, func(_ string, a antwort) bool { return a.Aus == key })
	}
	delete(n.angelegt, id)
	return n.angelegtFertig()
}

// Umbenennen gives the person id the name.
func (n *Netz) Umbenennen(id, name string) (map[string]antwort, map[string]handPerson, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p, da := n.angelegt[id]
	name = strings.Join(strings.Fields(name), " ")
	if !da || klar(name) == "" {
		return nil, nil, errFrageWeg
	}
	p.Name = name
	n.angelegt[id] = p
	return n.angelegtFertig()
}

// GleichePerson is a person made by hand and a phone-book person of her name.
type GleichePerson struct {
	Neu, Karte Knoten
}

// GleichePersonen are the persons made by hand whom a phone-book person
// of the same name (umlauts alike) may be, unless the person said Nein.
func (n *Netz) GleichePersonen() (out []GleichePerson) {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, id := range slices.Sorted(maps.Keys(n.angelegt)) {
		p := n.angelegt[id]
		for _, k := range slices.Sorted(maps.Keys(n.knoten)) {
			kn := n.knoten[k]
			if kn.Kontakt != "" && !istAngelegt(k) && n.fragbar(k) && klar(kn.Name) == klar(p.Name) && !slices.Contains(p.Nicht, k) {
				out = append(out, GleichePerson{Neu: n.knoten[id], Karte: kn})
				break
			}
		}
	}
	return out
}

// Dieselbe answers „Ist <Name> aus deinem Telefonbuch dieselbe Person?“:
// ja moves every answer about id to ziel (where ziel has one already,
// that stays) and deletes her; nein keeps both and asks no more.
func (n *Netz) Dieselbe(id, ziel string, ja bool) (map[string]antwort, map[string]handPerson, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p, da := n.angelegt[id]
	if _, z := n.knoten[ziel]; !da || !z || istAngelegt(ziel) {
		return nil, nil, errFrageWeg
	}
	if !ja {
		p.Nicht = append(p.Nicht, ziel)
		n.angelegt[id] = p
		return n.angelegtFertig()
	}
	neu := map[string]string{} // old key → new key
	for _, key := range n.ihreSchluessel(id) {
		x, y, _ := strings.Cut(key, "|")
		anderer := x
		if x == id {
			anderer = y
		}
		a := n.antworten[key]
		delete(n.antworten, key)
		if anderer == ziel {
			continue
		}
		nk := paarKey(ziel, anderer)
		if a.X == id {
			a.X = ziel
		}
		if _, schon := n.antworten[nk]; !schon {
			n.antworten[nk], neu[key] = a, nk
		}
	}
	for key, a := range n.antworten {
		if nk, ok := neu[a.Aus]; ok {
			a.Aus = nk
			n.antworten[key] = a
		}
	}
	delete(n.angelegt, id)
	return n.angelegtFertig()
}

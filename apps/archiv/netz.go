package main

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The net: who stands close to whom, and through what (archiv#27; moved
// here from Homes' internal/netz, homes ADR 0002, with the same numbers).
// It holds the ties among all the people in the person's photos, not
// only the person's own, and lives only in the open vault (umfeld.go).
//
// A photo day at one place is an event; a pair counts once per event,
// less in a big group (1/(n-1)), less the longer ago (half-life
// netzHalbwert), a little more for each year they meet in. A tie from
// photos alone shows from two days on. The order divides by how often
// both appear at all (hub), so a child on every photo is not close to all.
// The phone books add the same address, the same company and a relation
// a phone wrote (telefonbuch.go).

const (
	netzHalbwert  = 4 * 365.25 * 24 * time.Hour
	netzJahrBonus = 0.15  // per distinct year a pair meets in
	netzZelle     = 0.009 // a place cell, in degrees of latitude: about 1 km
	netzMinTage   = 2     // a tie from photos shows from this many days on
	kurzTeile     = 3     // reasons a list's row says (Kurz)
)

// The kinds of phone-book tie, and what each adds to a pair's strength.
const (
	bandAdresse   = "adresse"
	bandFirma     = "firma"
	bandBeziehung = "beziehung"
	bandKennen    = "kennen" // the person said the two know each other (fragen.go, artKennen)
)

var festGewicht = map[string]float64{bandAdresse: 1.5, bandFirma: 0.4, bandBeziehung: 3, bandName: 1, bandUeber: 0.5, bandKennen: 1}

// firmenPlatzhalter are ORG values that say nothing about whom one knows
// (klar): no tie, no question, no reason.
var firmenPlatzhalter = map[string]bool{"privat": true, "private": true, "keine": true, "kein": true, "none": true, "na": true, "nv": true,
	"selbststaendig": true, "selbstaendig": true, "freiberuflich": true, "freelancer": true, "student": true, "studentin": true,
	"schueler": true, "schuelerin": true, "rentner": true, "rentnerin": true, "home": true, "zuhause": true, "familie": true,
	"family": true, "arbeit": true, "work": true, "firma": true, "company": true, "unbekannt": true, "unknown": true}

// Hiding: Aus is not shown and restorable; Nicht is taken out of the
// photos before any tie is computed.
const (
	verbergenAus   = "aus"
	verbergenNicht = "nicht"
)

// netzFoto is one photo of the table: its id, when (an instant), where,
// who (Immich person ids).
type netzFoto struct {
	ID    string    `json:"i"`
	T     time.Time `json:"t"`
	Lat   *float64  `json:"la,omitempty"`
	Lng   *float64  `json:"lo,omitempty"`
	Stadt string    `json:"s,omitempty"`
	Wer   []string  `json:"p"`
}

// netzKontakt is a contact of the phone books: its key (haushalt/<uid>,
// privat/<uid>), name and Immich person ("" none); for the suggestions
// (fragen.go) its family name, birthday and streets.
type netzKontakt struct {
	Key      string   `json:"k"`
	Name     string   `json:"n"`
	Person   string   `json:"p,omitempty"`
	Family   string   `json:"f,omitempty"`
	Geb      string   `json:"g,omitempty"`
	Strassen []string `json:"s,omitempty"`
	Mails    []string `json:"m,omitempty"` // to find a LinkedIn connection's card (sozial.go)
	Firma    string   `json:"o,omitempty"` // ORG: no employer is suggested (herkunft.go)
	// Spitz are the card's NICKNAMEs („Hasi“): the person search finds by
	// them and the person's page says them. A round kept before has none.
	Spitz []string `json:"sp,omitempty"`
}

// festBand is a phone-book tie between two contacts' keys; for a
// relation, A's card says B is A's Wie.
type festBand struct {
	A   string `json:"a"`
	B   string `json:"b"`
	Art string `json:"art"`
	Wie string `json:"wie,omitempty"`
	// Gesagt: the person's answer (fragen.go), A and B nodes, Wie a kind
	Gesagt bool `json:"-"`
}

// netzTabelle is what a round read, kept in the vault.
type netzTabelle struct {
	Format   string            `json:"format"`
	Version  int               `json:"version"`
	Geholt   time.Time         `json:"geholt"`
	Fotos    []netzFoto        `json:"fotos"`
	Namen    map[string]string `json:"namen"` // the named persons, by id
	Kontakte []netzKontakt     `json:"kontakte"`
	Fest     []festBand        `json:"fest"`
	Ich      string            `json:"ich,omitempty"`      // the person's own contact
	IchAuch  []string          `json:"ich_auch,omitempty"` // other cards that are the person's too (selbstKarten)
	Haushalt []string          `json:"haushalt,omitempty"` // the household book's cards as last read
	Geb      map[string]string `json:"geb,omitempty"`      // Immich's birthDate, by named person
	// Gespraeche are msgvault's group chats and mail threads, matched to
	// the contacts (gespraeche.go); kept as last read while msgvault is busy.
	Gespraeche []netzGespraech `json:"gespraeche,omitempty"`
}

// Knoten is a person: an Immich person (ID its id) or a contact without
// one (ID its key); Kontakt its contact ("" none).
type Knoten struct {
	ID, Name, Kontakt string
}

// Kante is a tie: Staerke for the picture, Rang for the order; Tage the
// distinct photo days and Seit the first one's year (0 none); Fest what
// the phone books say; Chats the group chats both are in and Mails the
// messages of the mail threads both are in, Faeden how many threads
// (gespraeche.go); Gruppen those group chats' ids (gruppen.go).
type Kante struct {
	A, B                 string
	Staerke, Rang        float64
	Tage, Seit           int
	Fest                 []festBand
	Chats, Mails, Faeden int
	Gruppen              []string
}

// belegt: the tie has the evidence a tie needs to show at all: two photo
// days, something a phone book or the person says, or two conversations.
// A shared company alone is none (owner 2026-11-11): it is a question
// (fragen.go, kennenFragen), never a tie.
func (k *Kante) belegt() bool {
	return k.Tage >= netzMinTage || slices.ContainsFunc(k.Fest, func(f festBand) bool { return f.Art != bandFirma }) || k.Chats+k.Faeden >= gespraecheMin
}

// Netz is the person's contact net, built from a table.
type Netz struct {
	Loc *time.Location // the house's: a photo's day; nil time.Local
	Now func() time.Time

	mu        sync.Mutex
	st        netzTabelle
	verborgen map[string]string  // by node: Aus or Nicht
	antworten map[string]antwort // by pair (paarKey): the person's answers, fragen.go
	merkmale  map[string]merkmale
	knoten    map[string]Knoten
	von       map[string]string // node by contact key
	kanten    map[string][]*Kante
	ich       string
	// gespraech are the table's conversations by id, paarGruppen the group
	// chats each pair shares (by paarKey, tie or not), gruppen the person's
	// categories (gruppen.go), gelesen what the box's model read in the
	// messages and hinweise its relations by pair (hinweise.go).
	gespraech   map[string]netzGespraech
	paarGruppen map[string][]string
	gruppen     map[string]gruppenAntwort
	// gruppenModell is what the box's model read of the groups (gruppenmodell.go)
	gruppenModell map[string]gruppenLesung
	// gebHinweise are the birthdays the wishes suggest, gebAntworten the
	// person's answers to them, by node (geburtstage.go)
	gebHinweise  map[string]gebHinweis
	gebAntworten map[string]gebAntwort
	// herkunft are the towns and employers the model read in mail, by
	// node|field, herkunftAntworten the person's answers (herkunft.go);
	// paare the couples „wir“ messages suggest by pair, paareWeg those the
	// person discarded (paare.go)
	herkunft          map[string]herkunftVorschlag
	herkunftAntworten map[string]herkunftAntwort
	paare             map[string]paarVorschlag
	paareWeg          map[string]time.Time
	gelesen           map[string]gelesen
	hinweise          map[string]paarHinweis
	// sozial are the friends and connections of the social exports,
	// sozialGrund their reasons by node (sozial.go, sozialBauen).
	sozial      []sozialEintrag
	sozialGrund map[string][]string
	// selbst are the person's own node and the nodes that are her too: a
	// card of hers (IchAuch), an Immich person or a card named as she is
	// (umlauts alike). None is ever in „Steht nahe“ or a question but as „du“.
	selbst map[string]bool
	// such are the names each shown node is found by (suche.go)
	such map[string][]suchName
	// musterBaender are what the cards' names say („Mama von Lena“, namensmuster.go)
	musterBaender []namensBand
	// angelegt are the persons made by hand, by id (angelegt.go)
	angelegt map[string]handPerson
	// firmen are the pairs whose cards name one company, by paarKey: its
	// ORG as written; a placeholder or one on many cards left out (festDazu)
	firmen map[string]string
}

// neuesNetz builds the net of st with the hidden ones and the answers.
func neuesNetz(st netzTabelle, verborgen map[string]string, antworten map[string]antwort, loc *time.Location, now func() time.Time) *Netz {
	n := &Netz{Loc: loc, Now: now, st: st, verborgen: maps.Clone(verborgen), antworten: maps.Clone(antworten)}
	if n.verborgen == nil {
		n.verborgen = map[string]string{}
	}
	if n.antworten == nil {
		n.antworten = map[string]antwort{}
	}
	if n.st.Namen == nil {
		n.st.Namen = map[string]string{}
	}
	n.gruppen, n.gelesen, n.hinweise, n.gruppenModell = map[string]gruppenAntwort{}, map[string]gelesen{}, map[string]paarHinweis{}, map[string]gruppenLesung{}
	n.gebHinweise, n.gebAntworten = map[string]gebHinweis{}, map[string]gebAntwort{}
	n.herkunft, n.herkunftAntworten = map[string]herkunftVorschlag{}, map[string]herkunftAntwort{}
	n.paare, n.paareWeg = map[string]paarVorschlag{}, map[string]time.Time{}
	n.bauen()
	return n
}

func (n *Netz) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// ereignis is one day at one place: its day, year, instant and who.
type ereignis struct {
	tag  string
	jahr int
	t    time.Time
	wer  map[string]bool
}

// paar is a pair as it is summed up.
type paar struct {
	s, ueber    float64 // ueber: shared events over both's, geometric
	tage, jahre map[string]bool
	geteilt     int
	seit        int
}

// bauen rebuilds the nodes and the ties from st; n.mu is held or n is new.
func (n *Netz) bauen() {
	nicht := map[string]bool{}
	for id, art := range n.verborgen {
		nicht[id] = art == verbergenNicht
	}
	n.knoten, n.von, n.kanten = map[string]Knoten{}, map[string]string{}, map[string][]*Kante{}
	n.gespraech = map[string]netzGespraech{}
	for _, g := range n.st.Gespraeche {
		n.gespraech[g.ID] = g
	}
	for id, name := range n.st.Namen {
		n.knoten[id] = Knoten{ID: id, Name: name}
	}
	for _, k := range n.st.Kontakte {
		id := cmp.Or(k.Person, k.Key)
		kn, da := n.knoten[id]
		if !da {
			kn = Knoten{ID: id, Name: k.Name}
		}
		if kn.Kontakt == "" || strings.HasPrefix(k.Key, "haushalt/") && !strings.HasPrefix(kn.Kontakt, "haushalt/") {
			kn.Kontakt = k.Key
		}
		n.knoten[id], n.von[k.Key] = kn, id
	}
	n.musterBauen()
	n.sozialBauen()
	n.angelegtBauen()
	n.ich = n.von[n.st.Ich]
	n.selbstBauen()
	alle := map[[2]string]*Kante{}
	for key, p := range n.fotoPaare(nicht) {
		if len(p.tage) >= netzMinTage {
			s := p.s + netzJahrBonus*float64(len(p.jahre))
			alle[key] = &Kante{A: key[0], B: key[1], Staerke: s, Rang: s * (0.25 + 0.75*p.ueber), Tage: len(p.tage), Seit: p.seit}
		}
	}
	n.festDazu(alle, nicht)
	n.musterDazu(alle, nicht)
	n.gespraecheDazu(alle, nicht)
	n.gesagtDazu(alle, nicht)
	maps.DeleteFunc(alle, func(_ [2]string, k *Kante) bool { return !k.belegt() }) // a company alone, perhaps with one chat
	n.merkmaleBauen()
	n.suchBauen()
	for _, k := range alle {
		n.kanten[k.A], n.kanten[k.B] = append(n.kanten[k.A], k), append(n.kanten[k.B], k)
	}
}

// fotoPaare sums the photos up by pair, those in nicht left out.
func (n *Netz) fotoPaare(nicht map[string]bool) map[[2]string]*paar {
	loc := cmp.Or(n.Loc, time.Local)
	evs := map[string]*ereignis{}
	for _, f := range n.st.Fotos {
		t := f.T.In(loc)
		key := t.Format("2006-01-02") + "|" + ort(f)
		e := evs[key]
		if e == nil {
			e = &ereignis{tag: t.Format("2006-01-02"), jahr: t.Year(), t: f.T, wer: map[string]bool{}}
			evs[key] = e
		}
		for _, id := range f.Wer {
			if n.st.Namen[id] != "" && !nicht[id] {
				e.wer[id] = true
			}
		}
	}
	je, paare := map[string]int{}, map[[2]string]*paar{}
	for _, e := range evs {
		wer := slices.Sorted(maps.Keys(e.wer))
		alter := max(0, n.now().Sub(e.t).Hours()) / netzHalbwert.Hours()
		w := math.Pow(0.5, alter) / float64(max(1, len(wer)-1))
		for i, a := range wer {
			je[a]++
			for _, b := range wer[i+1:] {
				p := paare[[2]string{a, b}]
				if p == nil {
					p = &paar{tage: map[string]bool{}, jahre: map[string]bool{}, seit: e.jahr}
					paare[[2]string{a, b}] = p
				}
				p.s, p.geteilt, p.seit = p.s+w, p.geteilt+1, min(p.seit, e.jahr)
				p.tage[e.tag], p.jahre[strconv.Itoa(e.jahr)] = true, true
			}
		}
	}
	for key, p := range paare { // the hub: a child on every photo shares many, out of many
		p.ueber = float64(p.geteilt) / math.Sqrt(float64(je[key[0]]*je[key[1]]))
	}
	return paare
}

// ort is a photo's place: its cell of about 1 km, else its city, else none.
func ort(f netzFoto) string {
	if f.Lat != nil && f.Lng != nil && (*f.Lat != 0 || *f.Lng != 0) {
		breit := netzZelle / math.Max(0.1, math.Cos(*f.Lat*math.Pi/180))
		return strconv.Itoa(int(math.Floor(*f.Lat/netzZelle))) + "," + strconv.Itoa(int(math.Floor(*f.Lng/breit)))
	}
	return strings.ToLower(f.Stadt)
}

// festDazu adds the phone books' ties to alle, between the contacts'
// nodes, those in nicht left out; the same tie twice (both books) once.
// A company that says nothing (firmaAllgemein) is left out; the others
// are kept in firmen, for the questions.
func (n *Netz) festDazu(alle map[[2]string]*Kante, nicht map[string]bool) {
	n.firmen = map[string]string{}
	karten := map[string]map[string]bool{} // the people (nodes) each company is on the cards of
	for _, k := range n.st.Kontakte {
		if o := klar(k.Firma); o != "" {
			if karten[o] == nil {
				karten[o] = map[string]bool{}
			}
			karten[o][n.von[k.Key]] = true
		}
	}
	for _, f := range n.st.Fest {
		a, b := n.von[f.A], n.von[f.B]
		if a == "" || b == "" || a == b || nicht[a] || nicht[b] {
			continue
		}
		if f.Art == bandFirma {
			if f.Wie = n.firmaVon(f); firmaAllgemein(f.Wie, karten) {
				continue
			}
			n.firmen[paarKey(a, b)] = f.Wie
		}
		key := [2]string{min(a, b), max(a, b)}
		k := alle[key]
		if k == nil {
			k = &Kante{A: key[0], B: key[1]}
			alle[key] = k
		}
		if slices.ContainsFunc(k.Fest, func(g festBand) bool { return g.Art == f.Art && g.Wie == f.Wie && n.von[g.A] == a }) {
			continue
		}
		if !slices.ContainsFunc(k.Fest, func(g festBand) bool { return g.Art == f.Art }) { // a kind counts once
			k.Staerke, k.Rang = k.Staerke+festGewicht[f.Art], k.Rang+festGewicht[f.Art]
		}
		k.Fest = append(k.Fest, f)
	}
}

// firmaVon is the company a firm band names: its own (Wie), else, from a
// table read before it kept one, A's card's ORG; n.mu is held.
func (n *Netz) firmaVon(f festBand) string {
	if f.Wie != "" {
		return f.Wie
	}
	for _, k := range n.st.Kontakte {
		if k.Key == f.A {
			return k.Firma
		}
	}
	return ""
}

// firmaAllgemein: org links no two people in particular: a placeholder,
// or on the cards of more than maxFirma people (karten, by klar ORG).
func firmaAllgemein(org string, karten map[string]map[string]bool) bool {
	o := klar(org)
	return o == "" || firmenPlatzhalter[o] || len(karten[o]) > maxFirma
}

// selbstBauen finds the nodes that are the person (selbst); n.mu is held or n is new.
func (n *Netz) selbstBauen() {
	n.selbst = map[string]bool{}
	if n.ich == "" {
		return
	}
	n.selbst[n.ich] = true
	namen := map[string]bool{klar(n.knoten[n.ich].Name): true}
	for _, k := range n.st.Kontakte {
		if k.Key == n.st.Ich {
			namen[klar(k.Name)] = true
		}
	}
	for _, key := range n.st.IchAuch {
		if id := n.von[key]; id != "" {
			n.selbst[id] = true
		}
	}
	for id, k := range n.knoten {
		if namen[klar(k.Name)] {
			n.selbst[id] = true
		}
	}
}

// fragbar: id is shown and not one of the person's other selves (the
// person herself is, as „du“); n.mu is held.
func (n *Netz) fragbar(id string) bool {
	return n.sichtbar(id) && (!n.selbst[id] || id == n.ich)
}

// Anzeige is a node's name as a list shows it: „Du“ for the person.
func (n *Netz) Anzeige(k Knoten) string {
	if k.ID != "" && k.ID == n.Ich() {
		return "Du"
	}
	return k.Name
}

// Verbindung is a tie as seen from one end: the other, and the tie.
type Verbindung struct {
	Mit   Knoten
	Kante Kante
}

func andererVon(e *Kante, id string) string {
	if e.A == id {
		return e.B
	}
	return e.A
}

// Nahe are the k ties of id ranked first, those hidden left out.
func (n *Netz) Nahe(id string, k int) []Verbindung {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []Verbindung
	for _, e := range n.kanten[id] {
		other := andererVon(e, id)
		if n.verborgen[other] == "" && n.verborgen[id] == "" && (!n.selbst[other] || other == n.ich) { // her other cards never; herself as „Du“ (Anzeige)
			out = append(out, Verbindung{n.knoten[other], *e})
		}
	}
	slices.SortFunc(out, func(a, b Verbindung) int {
		return cmp.Or(cmp.Compare(b.Kante.Rang, a.Kante.Rang), strings.Compare(a.Mit.Name, b.Mit.Name))
	})
	return out[:min(k, len(out))]
}

// Zwischen are the ties among ids, each as the two indexes into ids.
func (n *Netz) Zwischen(ids []string) (out [][2]int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i, a := range ids {
		for _, e := range n.kanten[a] {
			if j := slices.Index(ids, andererVon(e, a)); j > i {
				out = append(out, [2]int{i, j})
			}
		}
	}
	return out
}

// Person is node id; ok false for none.
func (n *Netz) Person(id string) (Knoten, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	k, ok := n.knoten[id]
	return k, ok
}

// Von is the node of the contact key ("haushalt/<uid>"); "" none.
func (n *Netz) Von(key string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.von[key]
}

// Ich is the person's own node; "" when no card of the phone books is theirs.
func (n *Netz) Ich() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ich
}

// Mitte is where the picture starts: the person's own node, else the
// one with the most ties; "" without any.
func (n *Netz) Mitte() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.kanten[n.ich]) > 0 {
		return n.ich
	}
	best := ""
	for id, ks := range n.kanten {
		if len(ks) > len(n.kanten[best]) || len(ks) == len(n.kanten[best]) && id < best {
			best = id
		}
	}
	return best
}

// Groesse is how many people and ties the net holds.
func (n *Netz) Groesse() (personen, kanten int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ks := range n.kanten {
		kanten += len(ks)
	}
	return len(n.kanten), kanten / 2
}

// Verborgen is a hidden node and how (Aus, Nicht).
type Verborgen struct {
	Knoten
	Art string
}

// Verborgene are the hidden nodes, by name.
func (n *Netz) Verborgene() (out []Verborgen) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, art := range n.verborgen {
		out = append(out, Verborgen{cmp.Or(n.knoten[id], Knoten{ID: id, Name: id}), art})
	}
	slices.SortFunc(out, func(a, b Verborgen) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Verbergen hides id (Aus, Nicht), or with "" shows it again; Nicht's
// change rebuilds the ties at once from the table. It answers the hidden
// ones as they now are, for the vault.
func (n *Netz) Verbergen(id, art string) map[string]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	vorher := n.verborgen[id]
	if art == "" {
		delete(n.verborgen, id)
	} else {
		n.verborgen[id] = art
	}
	if vorher == verbergenNicht || art == verbergenNicht {
		n.bauen()
	}
	return maps.Clone(n.verborgen)
}

// Kurz is the tie in one short line for a list's row, as seen from
// mitte: what the other is of mitte, if that is known, then the first
// kurzTeile reasons, one group's category at most, the known relation
// never a second time (owner
// 2026-11-11: „weniger mit mehr“). The rest is one tap away.
func (n *Netz) Kurz(mitte string, k Kante) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	art, _, _ := n.artZwischen(andererVon(&k, mitte), mitte)
	var out []string
	if slices.Contains(arten, art) {
		out = append(out, kurzWort(art))
	} else if art == artKennen {
		out = append(out, saysKennenGesagt)
	}
	gs := n.gruende(mitte, k, art, 1)
	return strings.Join(append(out, gs[:min(kurzTeile, len(gs))]...), " · ")
}

// gruende are the tie's reasons as seen from mitte: the photo days and
// since when, the group chats and mails, at most gruppen of the groups'
// categories, then what the phone books say (ausserdem). n.mu is held.
func (n *Netz) gruende(mitte string, k Kante, art string, gruppen int) []string {
	var out []string
	if k.Tage > 0 {
		out = append(out, fmt.Sprintf("%s auf deinen Fotos seit %d", anzahlT(k.Tage, "Tag", "Tage"), k.Seit))
	}
	out = append(out, gespraechGruende(k)...)
	gg := n.gruppenGruende(k)
	out = append(out, gg[:min(gruppen, len(gg))]...)
	return append(out, n.ausserdem(mitte, k, art)...)
}

// ausserdem is what no count says about the tie, as seen from mitte: the
// social exports and the phone books. art is the kind the page already
// states for the pair ("" none): the answer or the phone-book relation
// behind it is not said again. n.mu is held.
func (n *Netz) ausserdem(mitte string, k Kante, art string) []string {
	out := slices.Clone(n.sozialGruende(k))
	for _, f := range k.Fest {
		if a := buchArt(f.Wie); art != "" && (f.Gesagt || f.Art == bandBeziehung && a != "" && (a == art || gegen[a] == art)) {
			continue
		}
		if g := n.buchGrund(mitte, f); !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out
}

// buchGrund is what a phone book (or the person) says of a tie, as a
// sentence with its value (owner 2026-11-11: „Warum wurde Horst eine
// Beziehung zu Christian attestiert?“): which company, which street,
// which card's name, which card's relation. n.mu is held.
func (n *Netz) buchGrund(mitte string, f festBand) string {
	switch {
	case f.Gesagt:
		return gesagtGrund(mitte, f)
	case f.Art == bandName || f.Art == bandUeber: // A and B are nodes; A's card's name says it
		wer := n.knoten[f.A].Name
		if klar(wer) == klar(f.Wie) {
			return "Der Kontaktname sagt: „" + f.Wie + "“"
		}
		return n.wessen(f.A, "Dein", "Kontaktname") + " sagt: „" + f.Wie + "“"
	case f.Art == bandAdresse && f.Wie != "":
		return "Gleiche Adresse auf beiden Karten: " + f.Wie
	case f.Art == bandAdresse:
		return "Gleiche Adresse auf beiden Karten"
	case f.Art == bandFirma:
		return strings.TrimSpace("Beide Kontaktkarten nennen die Firma " + n.firmaVon(f))
	}
	a, b := n.von[f.A], n.von[f.B] // a relation: A's card names B
	satz := n.wessen(a, "Deine", "Karte") + " nennt " + map[bool]string{true: "dich", false: n.knoten[b].Name}[b == n.ich]
	if f.Wie != "" {
		satz += " als " + f.Wie
	}
	return satz
}

// wessen is „Horsts Karte“, „Hans’ Karte“, or with dein „Deine Karte“
// for the person's: the node id's thing; n.mu is held.
func (n *Netz) wessen(id, dein, was string) string {
	if id == n.ich {
		return dein + " " + was
	}
	name := n.knoten[id].Name
	if slices.ContainsFunc([]string{"s", "x", "z", "ß"}, func(e string) bool { return strings.HasSuffix(strings.ToLower(name), e) }) {
		return name + "’ " + was
	}
	return name + "s " + was
}

// anzahlT is anzahl with the thousands point.
func anzahlT(n int, eins, mehr string) string {
	if n == 1 {
		return "1 " + eins
	}
	return tausender(n) + " " + mehr
}

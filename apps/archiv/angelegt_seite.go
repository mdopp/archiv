package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// The pages of the persons made by hand (angelegt.go): „<Name> anlegen“
// where „Zu wem?“ found nobody, her page's Umbenennen and Löschen, and
// „Ist <Name> aus deinem Telefonbuch dieselbe Person?“.

const (
	personPfad          = "/tresor/umfeld/person"
	saysAnlegenNiemand  = "Niemand heißt so in deinen Telefonbüchern oder auf deinen Fotos. Ein Tipp legt die Person an, nur in deinem Archiv:"
	saysAngelegtHinweis = "Von dir angelegt: steht nur in deinem Archiv, in keinem Adressbuch."
	saysAngelegtNichtZu = "Das ließ sich nicht ändern: Homes antwortet gerade nicht. Versuch es gleich noch einmal."
)

// anlegenAbschnitt is „<Name> anlegen“: name is art of y, one tap on a
// kind makes her and the relation, then back to zurueck with the toast.
func anlegenAbschnitt(n *Netz, y Knoten, name, zurueck string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<section class=karte id=anlegen><h2>%s</h2><p class=k>%s</p><form method=post action="%s" class="frage sofort"><input type=hidden name=tu value=anlegen><input type=hidden name=name value="%s"><input type=hidden name=y value="%s"><input type=hidden name=zurueck value="%s">`,
		esc(name+" anlegen"), esc(saysAnlegenNiemand), personPfad, esc(name), esc(y.ID), esc(zurueck))
	fmt.Fprintf(&b, `<p>%s</p><div class=chips>`, esc(name+" ist für "+map[bool]string{true: "dich", false: y.Name}[y.ID == n.Ich()]+" …"))
	for _, a := range haeufig[:5] {
		b.WriteString(chip(a, kurzWort(a), ""))
	}
	b.WriteString(`<details class=mehr><summary>mehr …</summary><div class=chips>`)
	for _, a := range arten {
		if !slices.Contains(haeufig[:5], a) {
			b.WriteString(chip(a, kurzWort(a), ""))
		}
	}
	b.WriteString(`</div></details></div></form></section>`)
	return b.String()
}

// angelegtNeuSatz is the toast of the tap that made her: „Wilma angelegt, Kind von dir“.
func angelegtNeuSatz(g Gegeben) string {
	return g.X.Name + " angelegt, " + kurzWort(g.Art) + " von " + duOder(g.Y, g.Ich)
}

// angelegtAktionen is her page's Umbenennen and Löschen (two taps: never gone on one).
func angelegtAktionen(k Knoten) string {
	return fmt.Sprintf(`<p class=k>%s</p><details class=karte id=umbenennen><summary>Umbenennen</summary><form method=post action="%s" class=feld><input type=hidden name=tu value=umbenennen><input type=hidden name=id value="%s"><label>Name<input name=name required autocomplete=off value="%s"></label>%s</form></details>`+
		`<details class=karte id=loeschen><summary>Löschen</summary><form method=post action="%s" class=feld><input type=hidden name=tu value=loeschen><input type=hidden name=id value="%s"><p class=k>%s</p>%s</form></details>`,
		esc(saysAngelegtHinweis), personPfad, esc(k.ID), esc(k.Name), knopf("Speichern", "Speichert …", ""),
		personPfad, esc(k.ID), esc(k.Name+" verschwindet aus deinem Archiv, mit allen Beziehungen, die du angegeben hast."), knopf(k.Name+" löschen", "Löscht …", ""))
}

// dieselbeAbschnitt asks once for each person made by hand (only id's
// when set) whom a phone-book person of her name may be.
func dieselbeAbschnitt(b *strings.Builder, n *Netz, nur, zurueck string) {
	var gs []GleichePerson
	for _, g := range n.GleichePersonen() {
		if nur == "" || g.Neu.ID == nur {
			gs = append(gs, g)
		}
	}
	if len(gs) == 0 {
		return
	}
	b.WriteString(`<section class=karte id=dieselbe><h2>Dieselbe Person?</h2><ul>`)
	for _, g := range gs {
		fmt.Fprintf(b, `<li class=tu><div>%s<span class=k>%s</span></div><form method=post action="%s" class=knoepfe><input type=hidden name=tu value=dieselbe><input type=hidden name=id value="%s"><input type=hidden name=ziel value="%s"><input type=hidden name=zurueck value="%s"><button name=ja value=1 data-laeuft="Führt zusammen …">Ja</button><button name=ja value=0 class=leise data-laeuft="Merkt es …">Nein</button></form></li>`,
			esc("Ist "+g.Karte.Name+" aus deinem Telefonbuch dieselbe Person?"), esc("Ja: Was du zu "+g.Neu.Name+" angegeben hast, gilt dann für den Kontakt. Nein: Es bleiben zwei."),
			personPfad, esc(g.Neu.ID), esc(g.Karte.ID), esc(zurueck))
	}
	b.WriteString(`</ul></section>`)
}

// personPost is every form of the persons made by hand.
func (w *Web) personPost(rw http.ResponseWriter, r *http.Request) {
	innen, ok := w.Tresor.Innen()
	if !ok || w.Umfeld.Netz() == nil {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
		return
	}
	p := personForm{w: w, rw: rw, r: r, innen: innen, zurueck: zurueckNach(r.PostFormValue("zurueck")), id: r.PostFormValue("id")}
	switch r.PostFormValue("tu") {
	case "anlegen":
		p.anlegen()
	case "umbenennen":
		p.umbenennen()
	case "loeschen":
		p.loeschen()
	case "dieselbe":
		p.dieselbe()
	case "verstorben", "lebt":
		p.verstorben(r.PostFormValue("tu") == "verstorben")
	default:
		w.zurueckFehler(rw, r, p.zurueck, http.StatusBadRequest, saysFrageOhneWahl)
	}
}

// personForm is one post of personPost.
type personForm struct {
	w           *Web
	rw          http.ResponseWriter
	r           *http.Request
	innen       Innen
	zurueck, id string
}

// tu keeps what f did in the vault; false: the error is answered.
func (p personForm) tu(f func(n *Netz) (map[string]antwort, map[string]handPerson, error)) bool {
	if err := p.w.Umfeld.Angelegt(p.innen, f); err != nil {
		p.w.antwortFehler(p.rw, p.r, p.zurueck, err)
		return false
	}
	return true
}

// ausHomes takes back from Homes what it wrote of the answers keys (a
// change to her is written again after it); false: the error is answered.
func (p personForm) ausHomes(keys []string) bool {
	for _, key := range keys {
		if err := p.w.ausHomes(p.r.Context(), p.innen, key); err != nil {
			log.Printf("archiv: Umfeld: eine Antwort nicht aus Homes: %v", err)
			p.w.zurueckFehler(p.rw, p.r, p.zurueck, http.StatusBadGateway, saysAngelegtNichtZu)
			return false
		}
	}
	return true
}

// eintragen has Homes write the answers keys and what followed from them.
func (p personForm) eintragen(keys []string) {
	for _, key := range keys {
		p.w.eintragen(p.r.Context(), p.innen, key)
		for _, d := range p.w.Umfeld.Netz().Gefolgerte(key) {
			p.w.eintragen(p.r.Context(), p.innen, d.Key)
		}
	}
}

func (p personForm) weiter(ziel string) { http.Redirect(p.rw, p.r, ziel, http.StatusSeeOther) }

func (p personForm) anlegen() {
	key := ""
	if !p.tu(func(n *Netz) (map[string]antwort, map[string]handPerson, error) {
		k, a, ps, err := n.Anlegen(p.r.PostFormValue("name"), p.r.PostFormValue("y"), p.r.PostFormValue("art"))
		key = k
		return a, ps, err
	}) {
		return
	}
	p.eintragen([]string{key})
	p.weiter(mitAnfrage(p.zurueck, url.Values{"getan": {key}}))
}

func (p personForm) umbenennen() {
	keys := p.w.Umfeld.Netz().Ihre(p.id)
	if !p.ausHomes(keys) || !p.tu(func(n *Netz) (map[string]antwort, map[string]handPerson, error) {
		return n.Umbenennen(p.id, p.r.PostFormValue("name"))
	}) {
		return
	}
	p.eintragen(keys)
	p.weiter(umfeldMitte(p.id) + "&m=umbenannt")
}

func (p personForm) loeschen() {
	if !p.ausHomes(p.w.Umfeld.Netz().Ihre(p.id)) || !p.tu(func(n *Netz) (map[string]antwort, map[string]handPerson, error) { return n.AngelegtWeg(p.id, false) }) {
		return
	}
	p.weiter("/tresor/umfeld?m=person-weg")
}

func (p personForm) dieselbe() {
	ja, ziel := p.r.PostFormValue("ja") == "1", p.r.PostFormValue("ziel")
	if !ja {
		if p.tu(func(n *Netz) (map[string]antwort, map[string]handPerson, error) { return n.Dieselbe(p.id, ziel, false) }) {
			p.weiter(mitAnfrage(p.zurueck, url.Values{"m": {"person-nein"}}))
		}
		return
	}
	if !p.ausHomes(p.w.Umfeld.Netz().Ihre(p.id)) || !p.tu(func(n *Netz) (map[string]antwort, map[string]handPerson, error) { return n.Dieselbe(p.id, ziel, true) }) {
		return
	}
	p.eintragen(p.w.Umfeld.Netz().Ihre(ziel))
	if strings.HasPrefix(p.zurueck, "/tresor/umfeld") { // her page is gone: the phone book's person's
		p.zurueck = umfeldMitte(ziel)
	}
	p.weiter(mitAnfrage(p.zurueck, url.Values{"m": {"person-ja"}}))
}

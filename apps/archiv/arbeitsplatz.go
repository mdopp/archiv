package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
)

// The open archive as a workplace (owner 2026-11-10: „eine Seite, auf der
// gearbeitet wird“, in Homes' patterns): the search on top, then Fragen
// (one at a time, fragen_seite.go), Steht nahe, the Stammbaum
// (stammbaum.go) and Gruppen (erkannt_seite.go); under Fragen one line
// of what the archive recognised by itself (erkannt.go). The sources are one line — „✓ aktuell“ or
// what needs the person — and their details and setup are a page of their
// own (/tresor/quellen), open or closed.

const (
	saysStammbaumLeer   = "Noch kein Stammbaum: Er wächst aus deinen Antworten unter „Fragen“ und aus den Beziehungen in den Telefonbüchern."
	saysUmfeldNochNicht = "Fragen stellt dein Archiv, sobald es aus deinen Fotos weiß, wer wem nahe steht."
	saysKeineFragen     = "Gerade gibt es keine offenen Fragen. Neue kommen, wenn das Archiv dein Umfeld neu rechnet."
	saysNachgebessert   = "Alles nachgebessert für jetzt."
)

// tabs are the workplace's parts, in their order: path value, word.
var tabs = [][2]string{{"fragen", "Fragen"}, {"nahe", "Steht nahe"}, {"stammbaum", "Stammbaum"}, {"gruppen", "Gruppen"}}

// arbeitsplatz writes the tabs and the chosen one (?tab=, Fragen by
// default). Fragen shows the visit's portion (Netz.Portion) and counts
// only it, never the backlog; when it is done, a calm line and „weitere
// Fragen“ (?mehr=1) for whoever wants more, one at a time.
func (w *Web) arbeitsplatz(b *strings.Builder, r *http.Request, bereit bool) {
	n := w.Umfeld.Netz()
	fs, weitere := n.Portion(w.Umfeld.Besuch(neuerBesuch(r)))
	mehr := r.URL.Query().Get("mehr") != ""
	tab := r.URL.Query().Get("tab")
	b.WriteString(`<nav class=unterseiten aria-label=Bereiche>`)
	for i, t := range tabs {
		aktiv := t[0] == tab || i == 0 && !slices.ContainsFunc(tabs[1:], func(t [2]string) bool { return t[0] == tab })
		zahl := ""
		if t[0] == "fragen" && len(fs) > 0 {
			zahl = fmt.Sprintf(` <span class=anzahl>%d</span>`, len(fs))
		}
		fmt.Fprintf(b, `<a href="/tresor?tab=%s"%s>%s%s</a>`, t[0], map[bool]string{true: ` aria-current=page`}[aktiv], t[1], zahl)
		if aktiv {
			tab = t[0]
		}
	}
	b.WriteString(`</nav>`)
	switch {
	case n == nil:
		w.umfeldFehlt(b)
	case tab == "stammbaum":
		stammbaumTab(b, n)
	case tab == "nahe":
		naheListe(b, n)
	case tab == "gruppen":
		gruppenTab(b, n, r.URL.Query().Get("alle") != "")
		b.WriteString(w.toast(n, r.URL.Query().Get("getan"), gruppenTabPfad))
	case len(fs) > 0:
		b.WriteString(w.frageKarte(n, fs[0], len(fs), false))
		if len(fs) > 1 {
			b.WriteString(`<div class=naechste aria-hidden=true></div>`)
		}
	case mehr && len(weitere) > 0:
		b.WriteString(w.frageKarte(n, weitere[0], 0, true))
	case len(weitere) > 0:
		fmt.Fprintf(b, `<div class=gut><span class=punkt></span><p>%s</p></div><p class=weiter-in><a class=leise href="/tresor?tab=fragen&amp;mehr=1">weitere Fragen</a></p>`, esc(saysNachgebessert))
	default:
		fmt.Fprintf(b, `<div class=gut><span class=punkt></span><p>%s</p></div>`, esc(saysKeineFragen))
	}
	if tab != "fragen" {
		return
	}
	w.erkanntZeile(b, n)
	if gs := n.Gegebene(); len(gs) > 0 {
		fmt.Fprintf(b, `<p class=weiter-in><a href="/tresor/umfeld#antworten">Deine Antworten (%d)</a><span class=k>Jede lässt sich dort zurücknehmen.</span></p>`, len(gs))
	}
	b.WriteString(w.toast(n, r.URL.Query().Get("getan"), "/tresor"))
	if bereit {
		w.personenKarte(b)
	}
}

// neuerBesuch: the person came to the page from outside — a link, a
// bookmark, the address typed — not from the archive's own pages, its
// answers or its live updates (the browser's Sec-Fetch headers).
func neuerBesuch(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin"
}

// umfeldFehlt says why there is no net yet, and what to do.
func (w *Web) umfeldFehlt(b *strings.Builder) {
	u := w.Umfeld
	innen, _ := w.Tresor.Innen()
	_, hat := HatImmichSchluessel(innen)
	switch {
	case !u.Eingerichtet():
		fmt.Fprintf(b, `<p class=ruhig><span class="punkt grau"></span>%s</p>`, esc(saysImmichNichtEingerichtet))
	case !hat:
		fmt.Fprintf(b, `<p class=ruhig><span class="punkt gelb"></span>%s</p><p class=weiter-in><a href="/tresor/quellen#karte-umfeld">Immich-Schlüssel eintragen</a></p>`, esc(saysUmfeldNochNicht))
	default:
		b.WriteString(`<div id=umfeld-stand data-live>`)
		umfeldStandText(b, u.Stand(), hat)
		b.WriteString(`</div>`)
	}
}

// naheListe is „Steht nahe“: the closest to the person, each a row with
// its face and name, a tap puts it in the middle of the Umfeld page (its
// detail); its reasons and a bar of the tie's strength, a tap opens the
// edge page („Was verbindet die beiden?“).
func naheListe(b *strings.Builder, n *Netz) {
	mitte := n.Mitte()
	vs := n.Nahe(mitte, 12)
	if len(vs) == 0 {
		b.WriteString(`<p class=ruhig><span class="punkt grau"></span>Noch keine Verbindungen: Ein Paar zählt, sobald es an zwei Tagen zusammen auf deinen Fotos ist oder die Telefonbücher etwas über die beiden sagen.</p>`)
		return
	}
	titel, wer := "Steht dir nahe", "dich"
	if k, _ := n.Person(mitte); mitte != n.Ich() {
		titel, wer = "Steht "+k.Name+" nahe", k.Name
	}
	fmt.Fprintf(b, `<section class=karte><h2>%s</h2><ul class=leute>`, esc(titel))
	for _, v := range vs {
		fmt.Fprintf(b, `<li class=zwei><a href="%s">%s<span class=wer>%s</span></a><a class=warum href="%s" aria-label="%s"><span class=k>%s</span><span class=balken><b style="width:%.0f%%"></b></span></a></li>`,
			esc(umfeldMitte(v.Mit.ID)), gesicht(v.Mit.Name), esc(n.Anzeige(v.Mit)), esc(kanteAdresse(mitte, v.Mit.ID)), esc("Was verbindet "+wer+" und "+v.Mit.Name+"?"),
			esc(n.Gruende(mitte, v.Kante)), 100*v.Kante.Staerke/vs[0].Kante.Staerke)
	}
	b.WriteString(`</ul></section><p class=weiter-in><a href="/tresor/umfeld">Bild, alle und Ausgeblendete</a><span class=k>Tipp auf eine Person: wer ihr nahe steht.</span></p>`)
}

// sucheForm is the search on top, q in it.
func sucheForm(q string) string {
	return `<form method=get action="/tresor/suche" role=search><input type=search name=q value="` + esc(q) + `" aria-label="Suche" placeholder="Suche in Nachrichten, Mails, Kontakten …"><button data-laeuft="Sucht …">Suchen</button></form>`
}

// msgvaultSuche is msgvault's own page with the words (its web UI keeps
// its state in the address: workspace, mode, explore).
func msgvaultSuche(q string) string {
	explore, _ := json.Marshal(map[string]any{"schemaVersion": 1, "query": q})
	return msgvaultEingang + "?" + url.Values{"workspace": {"everything"}, "mode": {"full_text"}, "explore": {string(explore)}}.Encode()
}

// suche is the search's page: the contacts whose name matches (umlauts
// and their spelled-out form, and case, count the same: owner 2026-11-10,
// „Jürgen“ finds „Juergen Burger“), each a search for its own spelling,
// then the search in every message, in the other spelling too.
func (w *Web) suche(rw http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	innen, offen := w.Tresor.Innen()
	if q == "" || !offen {
		http.Redirect(rw, r, "/tresor", http.StatusSeeOther)
		return
	}
	var b strings.Builder
	b.WriteString(sucheForm(q))
	if treffer := kontakteFinden(innen, q, 20); len(treffer) > 0 {
		b.WriteString(`<section class=karte><h2>Kontakte</h2><ul class=leute>`)
		for _, n := range treffer {
			fmt.Fprintf(&b, `<li><a href="%s">%s<span class=wer>%s<span class=k>Nachrichten mit diesem Namen</span></span></a></li>`, esc(msgvaultSuche(n)), gesicht(n), esc(n))
		}
		b.WriteString(`</ul></section>`)
	}
	fmt.Fprintf(&b, `<p class=weiter-in><a href="%s">In allen Nachrichten nach „%s“ suchen</a></p>`, esc(msgvaultSuche(q)), esc(q))
	if anders := ausgeschrieben(q); anders != q {
		fmt.Fprintf(&b, `<p class=weiter-in><a href="%s">Auch „%s“ suchen</a><span class=k>So schreiben manche Telefonbücher und Mails Umlaute.</span></p>`, esc(msgvaultSuche(anders)), esc(anders))
	}
	seite(rw, http.StatusOK, "Suche", b.String(), "")
}

// ausgeschrieben is q with its umlauts and ß spelled out: Jürgen → Juergen.
func ausgeschrieben(q string) string {
	return strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss").Replace(q)
}

// kontakteFinden are the names of the contacts in the open vault that
// hold every word of q (klar: umlauts, spelled out or not, and case alike).
func kontakteFinden(innen Innen, q string, hoechstens int) (out []string) {
	k, err := kontakteLaden(kontakteDatei(innen))
	if err != nil {
		return nil
	}
	for _, c := range k.Sortiert() {
		name := klar(c.Name)
		alle := name != ""
		for _, w := range strings.Fields(q) {
			alle = alle && strings.Contains(name, klar(w))
		}
		if alle && len(out) < hoechstens && !slices.Contains(out, c.Name) {
			out = append(out, c.Name)
		}
	}
	return out
}

// quellenZeile is the sources in one line: „✓ aktuell“, what runs, or the
// first thing that needs the person; a tap opens their page.
func (w *Web) quellenZeile(b *strings.Builder, offen bool) {
	was, klasse := "✓ aktuell", "gut"
	if p := w.quellenProbleme(offen); len(p) > 0 {
		was, klasse = p[0], "achtung"
		if len(p) > 1 {
			was += fmt.Sprintf(" (und %d mehr)", len(p)-1)
		}
	} else if w.Postbote.Laeuft() != "" || w.packt() != "" {
		was, klasse = "liest gerade ein …", ""
	}
	fmt.Fprintf(b, `<a class=quellen id=quellen-zeile data-live href="/tresor/quellen"><span>Signal · WhatsApp · Mail · SMS · Kontakte</span><b class=%q>%s</b></a>`, klasse, esc(was))
}

// quellenProbleme is what needs the person, the most pressing first.
func (w *Web) quellenProbleme(offen bool) (p []string) {
	ein, f, _ := w.Protokoll.Kopie()
	if f != nil {
		p = append(p, map[string]string{"signal": "Signal", "mail": "E-Mail", "sms": "SMS", "whatsapp": "WhatsApp", "sozial": "Facebook/LinkedIn"}[f.Quelle]+" meldet einen Fehler")
	}
	if w.fehlenSignalSchluessel() {
		p = append(p, "Signal: Schlüssel fehlt")
	}
	innen, auf := w.Tresor.Innen()
	if offen && auf && wartende(w.Eingang)["whatsapp"].Wartend > 0 {
		if _, err := os.Stat(innen.WhatsAppSchluessel()); err != nil {
			p = append(p, "WhatsApp: Schlüssel fehlt")
		}
	}
	if a := w.Abgleich; offen && a.Eingerichtet() {
		if st := a.Stand(); st.Fehler != "" || st.Kontakte.Fehler != "" || st.KalenderFehler != "" {
			p = append(p, "Kontakte: Abgleich gestört")
		}
	}
	if u := w.Umfeld; offen && auf && u.Eingerichtet() {
		if _, hat := HatImmichSchluessel(innen); !hat {
			p = append(p, "Fotos: Immich-Schlüssel fehlt")
		} else if u.Stand().Fehler != "" {
			p = append(p, "Fotos: Umfeld gestört")
		}
	}
	if _, mail := w.Schluessel.Hinterlegt(SchluesselIMAP); len(ein) == 0 && !mail && len(p) == 0 {
		p = append(p, "noch nichts eingerichtet")
	}
	return p
}

// quellenSeite is the sources' page: each card with what it holds and how
// to set it up; while closed, what can be said without the vault.
func (w *Web) quellenSeite(rw http.ResponseWriter, r *http.Request, code int, meldung, fehler string) {
	st := w.Tresor.Stand()
	if st.Zustand == Uneingerichtet {
		w.tresorSeite(rw, r, code, meldung, fehler)
		return
	}
	var b strings.Builder
	meldungenText(&b, meldung, fehler)
	offen := st.Zustand == Offen
	w.laeuftJetzt(&b)
	w.signalKarte(&b, offen)
	w.mailKarte(&b, offen)
	w.smsKarte(&b)
	w.whatsappKarte(&b)
	w.sozialKarte(&b, offen)
	w.kontakteKarte(&b, offen)
	if offen {
		w.umfeldKarte(&b)
		w.passkeyKarte(&b)
	} else {
		fmt.Fprintf(&b, `<p class=ruhig><span class="punkt grau"></span>%s Schlüssel ändern kannst du, wenn es offen ist.</p>`, saysAdminLiest)
	}
	seite(rw, code, "Quellen", b.String(), "")
}

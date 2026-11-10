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
// (one at a time, fragen_seite.go), Steht nahe, and the place of the
// Stammbaum, which comes later. The sources are one line — „✓ aktuell“ or
// what needs the person — and their details and setup are a page of their
// own (/tresor/quellen), open or closed.

const (
	saysStammbaumSpaeter = "Der Stammbaum kommt in einem späteren Schritt. Er wächst aus deinen Antworten unter „Fragen“."
	saysUmfeldNochNicht  = "Fragen stellt dein Archiv, sobald es aus deinen Fotos weiß, wer wem nahe steht."
	saysKeineFragen      = "Gerade gibt es keine offenen Fragen. Neue kommen, wenn das Archiv dein Umfeld neu rechnet."
)

// tabs are the workplace's parts, in their order: path value, word.
var tabs = [][2]string{{"fragen", "Fragen"}, {"nahe", "Steht nahe"}, {"stammbaum", "Stammbaum"}}

// arbeitsplatz writes the tabs and the chosen one (?tab=, Fragen by default).
func (w *Web) arbeitsplatz(b *strings.Builder, r *http.Request, bereit bool) {
	n := w.Umfeld.Netz()
	fs := n.Fragen()
	tab := r.URL.Query().Get("tab")
	b.WriteString(`<nav class=unterseiten aria-label=Bereiche>`)
	for i, t := range tabs {
		aktiv := t[0] == tab || i == 0 && tab != "nahe" && tab != "stammbaum"
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
	case tab == "stammbaum":
		fmt.Fprintf(b, `<p class=ruhig><span class="punkt grau"></span>%s</p>`, esc(saysStammbaumSpaeter))
	case n == nil:
		w.umfeldFehlt(b)
	case tab == "nahe":
		naheListe(b, n)
	case len(fs) == 0:
		fmt.Fprintf(b, `<div class=gut><span class=punkt></span><p>%s</p></div>`, esc(saysKeineFragen))
	default:
		b.WriteString(w.frageKarte(n, fs[0], len(fs)))
		if len(fs) > 1 {
			b.WriteString(`<div class=naechste aria-hidden=true></div>`)
		}
	}
	if tab != "fragen" {
		return
	}
	if gs := n.Gegebene(); len(gs) > 0 {
		fmt.Fprintf(b, `<p class=weiter-in><a href="/tresor/umfeld#antworten">Deine Antworten (%d)</a><span class=k>Jede lässt sich dort zurücknehmen.</span></p>`, len(gs))
	}
	b.WriteString(w.toast(n, r.URL.Query().Get("getan"), "/tresor"))
	if bereit {
		w.personenKarte(b)
	}
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
// its face, its reasons and a bar of the tie's strength; a tap puts it in
// the middle of the Umfeld page.
func naheListe(b *strings.Builder, n *Netz) {
	mitte := n.Mitte()
	vs := n.Nahe(mitte, 12)
	if len(vs) == 0 {
		b.WriteString(`<p class=ruhig><span class="punkt grau"></span>Noch keine Verbindungen: Ein Paar zählt, sobald es an zwei Tagen zusammen auf deinen Fotos ist oder die Telefonbücher etwas über die beiden sagen.</p>`)
		return
	}
	titel := "Steht dir nahe"
	if k, _ := n.Person(mitte); mitte != n.Ich() {
		titel = "Steht " + k.Name + " nahe"
	}
	fmt.Fprintf(b, `<section class=karte><h2>%s</h2><ul class=leute>`, esc(titel))
	for _, v := range vs {
		fmt.Fprintf(b, `<li><a href="%s">%s<span class=wer>%s<span class=k>%s</span></span><span class=balken><b style="width:%.0f%%"></b></span></a></li>`,
			esc(umfeldMitte(v.Mit.ID)), gesicht(v.Mit.Name), esc(v.Mit.Name), esc(n.Gruende(mitte, v.Kante)), 100*v.Kante.Staerke/vs[0].Kante.Staerke)
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
	return "/?" + url.Values{"workspace": {"everything"}, "mode": {"full_text"}, "explore": {string(explore)}}.Encode()
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
		p = append(p, map[string]string{"signal": "Signal", "mail": "E-Mail", "sms": "SMS", "whatsapp": "WhatsApp"}[f.Quelle]+" meldet einen Fehler")
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
	w.kontakteKarte(&b, offen)
	if offen {
		w.umfeldKarte(&b)
		w.passkeyKarte(&b)
	} else {
		fmt.Fprintf(&b, `<p class=ruhig><span class="punkt grau"></span>%s Schlüssel ändern kannst du, wenn es offen ist.</p>`, saysAdminLiest)
	}
	seite(rw, code, "Quellen", b.String(), "")
}

package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The mail card on the sources' page: the person's mailboxes, each with
// what it holds and, open, how to change, remove or clean it up; the
// household's document mailbox; a form for one more.

const (
	saysIMAPDoppelt  = "Dieses Postfach ist schon eingetragen. Ändern kannst du es bei seinem Eintrag."
	saysIMAPWeg      = "Dieses Postfach gibt es nicht mehr. Lade die Seite neu."
	saysIMAPNeuPW    = "Für einen anderen Server oder Benutzernamen braucht das Archiv auch das App-Passwort dazu."
	saysAufrDauer    = "Gib an, wie alt eine Mail mindestens sein muss: eine Zahl von Monaten oder Jahren, mindestens ein Monat."
	saysAufrVeraltet = "Die Zählung passt nicht mehr zu deiner Einstellung, oder sie fand nichts zu löschen. Warte, bis das Archiv neu gezählt hat, und tipp dann noch einmal."
	saysAufrNie      = "Beim Anbieter lässt sich das nicht rückgängig machen. In deinem Archiv bleiben die Mails, durchsuchbar wie bisher."
	saysMailNichtDa  = "Diese Mail ist (noch) nicht in deinem Archiv. Das Archiv holt Mails jede Stunde ab; kurz nach dem Ablegen ist sie vielleicht noch unterwegs."
)

// imap stores a mailbox: a new one (no konto) or a change of one.
func (w *Web) imap(rw http.ResponseWriter, r *http.Request) {
	sch, ok := w.offenesFach(rw, r)
	if !ok {
		return
	}
	konten, err := imapKonten(sch)
	if err != nil {
		log.Printf("archiv: IMAP-Zugang unlesbar: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	id := r.PostFormValue("konto")
	name := strings.TrimSpace(r.PostFormValue("name"))
	host, benutzer, pw := strings.TrimSpace(r.PostFormValue("host")), strings.TrimSpace(r.PostFormValue("benutzer")), r.PostFormValue("passwort")
	port, err := imapPort(r.PostFormValue("port"))
	if err != nil {
		w.antwort(rw, r, http.StatusBadRequest, "", saysIMAPPort)
		return
	}
	if host == "" || benutzer == "" {
		w.antwort(rw, r, http.StatusBadRequest, "", saysIMAPFehlt)
		return
	}
	i := -1
	for j, z := range konten {
		if z.ID == id && id != "" {
			i = j
		} else if strings.EqualFold(z.Host, host) && strings.EqualFold(z.Benutzer, benutzer) && z.Port == port {
			w.antwort(rw, r, http.StatusConflict, "", saysIMAPDoppelt)
			return
		}
	}
	gewechselt := false
	var z imapZugang
	switch {
	case id != "" && i < 0:
		w.antwort(rw, r, http.StatusConflict, "", saysIMAPWeg)
		return
	case i < 0:
		if pw == "" {
			w.antwort(rw, r, http.StatusBadRequest, "", saysIMAPFehlt)
			return
		}
		z = imapZugang{ID: zufall()}
	default:
		z = konten[i]
		gewechselt = !strings.EqualFold(z.Host, host) || !strings.EqualFold(z.Benutzer, benutzer) || z.Port != port
		if gewechselt && pw == "" {
			w.antwort(rw, r, http.StatusBadRequest, "", saysIMAPNeuPW)
			return
		}
	}
	z.Name, z.Host, z.Port, z.Benutzer = name, host, port, benutzer
	if pw != "" {
		z.Passwort, z.Am = pw, time.Now()
	}
	if i < 0 {
		konten = append(konten, z)
	} else {
		konten[i] = z
	}
	if err := imapKontenSpeichern(sch, konten); err != nil {
		log.Printf("archiv: IMAP speichern: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	if gewechselt {
		if w.Mailholer != nil {
			w.Mailholer.KontoGewechselt(z.ID)
		}
		if innen, auf := w.Tresor.Innen(); auf && w.Aufraeumer != nil {
			w.Aufraeumer.Vergessen(innen, z.ID) // another mailbox: what the person allowed for the old one does not carry over
		}
	}
	if w.Mailholer != nil {
		w.Mailholer.NeuerSchluessel()
	}
	if w.Geaendert != nil {
		w.Geaendert()
	}
	http.Redirect(rw, r, "/tresor/quellen?m=imap#karte-mail", http.StatusSeeOther)
}

// imapWeg removes a mailbox. What it brought stays in the archive.
func (w *Web) imapWeg(rw http.ResponseWriter, r *http.Request) {
	sch, ok := w.offenesFach(rw, r)
	if !ok {
		return
	}
	konten, err := imapKonten(sch)
	if err != nil {
		log.Printf("archiv: IMAP-Zugang unlesbar: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	id := r.PostFormValue("konto")
	var rest []imapZugang
	for _, z := range konten {
		if z.ID != id {
			rest = append(rest, z)
		}
	}
	if id == "" || len(rest) == len(konten) {
		w.antwort(rw, r, http.StatusConflict, "", saysIMAPWeg)
		return
	}
	if rest == nil {
		rest = []imapZugang{}
	}
	if err := imapKontenSpeichern(sch, rest); err != nil {
		log.Printf("archiv: IMAP speichern: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	if w.Mailholer != nil {
		w.Mailholer.KontoWeg(id)
	}
	if innen, auf := w.Tresor.Innen(); auf && w.Aufraeumer != nil {
		w.Aufraeumer.Vergessen(innen, id)
	}
	if w.Geaendert != nil {
		w.Geaendert()
	}
	http.Redirect(rw, r, "/tresor/quellen?m=imap-weg#karte-mail", http.StatusSeeOther)
}

// kontoDa: the open vault and the person's mailbox of this id.
func (w *Web) kontoDa(rw http.ResponseWriter, r *http.Request) (Innen, string, bool) {
	innen, auf := w.Tresor.Innen()
	if !auf || w.Aufraeumer == nil {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
		return Innen{}, "", false
	}
	id := r.PostFormValue("konto")
	konten, _ := imapKonten(w.Schluessel)
	for _, z := range konten {
		if z.ID == id && id != "" {
			return innen, id, true
		}
	}
	w.antwort(rw, r, http.StatusConflict, "", saysIMAPWeg)
	return Innen{}, "", false
}

// aufraeumenEinstellen: on or off, and „älter als“, for one mailbox.
func (w *Web) aufraeumenEinstellen(rw http.ResponseWriter, r *http.Request) {
	innen, id, ok := w.kontoDa(rw, r)
	if !ok {
		return
	}
	an := r.PostFormValue("an") == "1"
	monate := aufraeumenMonate
	if e, da := w.Aufraeumer.Einstellungen(innen)[id]; da && e.Monate > 0 {
		monate = e.Monate
	}
	if an {
		n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("dauer")))
		if r.PostFormValue("einheit") == "jahre" {
			n *= 12
		}
		if err != nil || n < 1 || n > 600 {
			w.antwort(rw, r, http.StatusBadRequest, "", saysAufrDauer)
			return
		}
		monate = n
	}
	if err := w.Aufraeumer.Einstellen(innen, id, an, monate); err != nil {
		log.Printf("archiv: Aufräumen einstellen: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	m := "aufr-aus"
	if an {
		m = "aufr-an"
	}
	if w.Geaendert != nil {
		w.Geaendert()
	}
	http.Redirect(rw, r, "/tresor/quellen?m="+m+"#karte-mail", http.StatusSeeOther)
}

// aufraeumenLos is the person's tap after the dry run.
func (w *Web) aufraeumenLos(rw http.ResponseWriter, r *http.Request) {
	innen, id, ok := w.kontoDa(rw, r)
	if !ok {
		return
	}
	err := w.Aufraeumer.Freigeben(innen, id)
	switch {
	case errors.Is(err, errNichtFrei):
		w.antwort(rw, r, http.StatusConflict, "", saysAufrVeraltet)
		return
	case err != nil:
		log.Printf("archiv: Aufräumen freigeben: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	if w.Geaendert != nil {
		w.Geaendert()
	}
	http.Redirect(rw, r, "/tresor/quellen?m=aufr-los#karte-mail", http.StatusSeeOther)
}

// mailZeigen: /tresor/mail?id=<Message-ID>, the link Paperless keeps on a
// document that came by mail: that mail in msgvault's own page.
func (w *Web) mailZeigen(rw http.ResponseWriter, r *http.Request) {
	id := messageID(r.URL.Query().Get("id"))
	if w.Tresor.APIKey() == "" || w.Personen == nil || w.Personen.API == nil {
		http.Redirect(rw, r, "/tresor?m=mail-zu", http.StatusSeeOther)
		return
	}
	n, err := w.Personen.API.MailNummer(r.Context(), id)
	if err != nil {
		log.Printf("archiv: Mail suchen: %v", err)
	}
	if n == 0 {
		seite(rw, http.StatusNotFound, "Mail", `<p class="meldung fehler">`+saysMailNichtDa+`</p><p><a href="/tresor">Zu deinem Archiv</a></p>`, "")
		return
	}
	http.Redirect(rw, r, "/messages/"+strconv.FormatInt(n, 10), http.StatusSeeOther)
}

// alterText: 24 → „2 Jahre“, 18 → „18 Monate“.
func alterText(monate int) string {
	if monate%12 == 0 {
		return anzahl(monate/12, "Jahr", "Jahre")
	}
	return anzahl(monate, "Monat", "Monate")
}

func (w *Web) mailKarte(b *strings.Builder, offen bool, qa quelleAuf) {
	konten, err := imapKonten(w.Schluessel)
	if err != nil {
		log.Printf("archiv: IMAP-Zugang unlesbar: %v", err)
	}
	quelleKopf(b, qa, "mail", "E-Mail", fehltZeile(w.letzteZeile("mail", "Mail", "Mails"), len(konten) == 0, "kein Postfach eingetragen"))
	w.imAchiv(b, "mail", "Mail", "Mails")
	_, f, _ := w.Protokoll.Kopie()
	fehler := func(konto string, einziges bool) {
		if f != nil && f.Quelle == "mail" && (f.Datei == konto || f.Datei == "" && einziges) {
			fmt.Fprintf(b, `<p class="meldung fehler">%s <span class=k>(%s)</span></p>`, esc(f.Satz), datum(f.Am))
			if f.Mal >= DreimalRot {
				fmt.Fprintf(b, `<p class=k>Das ist jetzt %d-mal hintereinander passiert. Der Admin-Agent der Box ist benachrichtigt und sieht sich den Grund an.</p>`, f.Mal)
			}
		}
	}
	if len(konten) == 0 {
		b.WriteString(`<p class=zugang><span class=fehlt>Kein Postfach eingetragen</span></p>`)
	}
	innen, auf := w.Tresor.Innen()
	var einst map[string]Aufraeumen
	if offen && auf && w.Aufraeumer != nil {
		einst = w.Aufraeumer.Einstellungen(innen)
	}
	for _, z := range ohnePasswort(konten) {
		b.WriteString(`<div class=postfach>`)
		wo := esc(z.Host)
		if z.Port > 0 {
			wo += ":" + strconv.Itoa(z.Port)
		}
		seit := ""
		if !z.Am.IsZero() {
			seit = " · App-Passwort hinterlegt am " + datum(z.Am)
		} else if t, ok := w.Schluessel.Hinterlegt(SchluesselIMAP); ok {
			seit = " · App-Passwort hinterlegt am " + datum(t)
		}
		titel := ""
		if z.Name != "" {
			titel = "<b>" + esc(z.Name) + "</b>: "
		}
		fmt.Fprintf(b, `<p class=zugang>%sPostfach <b>%s</b> auf %s%s</p>`, titel, esc(z.Benutzer), wo, seit)
		fehler(z.Benutzer, len(konten) == 1)
		if offen && auf {
			if einst != nil {
				w.aufraeumenKarte(b, z, einst[z.ID])
			}
			fmt.Fprintf(b, `<details><summary>Ändern oder entfernen</summary>
<p class=k>Was du hier speicherst, ersetzt den Eintrag für %s. Ein leeres App-Passwort bleibt, wie es ist.</p>
<form method=post action="/tresor/imap" class=feld><input type=hidden name=konto value="%s">
<label>Name (nur für dich, z. B. Arbeit)<input name=name autocomplete=off value="%s"></label>
<label>Server<input name=host autocomplete=off value="%s"></label>
<label>Port (leer: 993)<input name=port inputmode=numeric autocomplete=off value="%s"></label>
<label>Benutzername<input name=benutzer type=email autocomplete=off value="%s"></label>
<label>Neues App-Passwort<input name=passwort type=password autocomplete=new-password></label>
%s</form>
<form method=post action="/tresor/imap/weg" class=zeile><input type=hidden name=konto value="%s"><span class=k>Mails, die schon im Archiv sind, bleiben drin.</span>%s</form></details>`,
				esc(z.Anzeige()), esc(z.ID), esc(z.Name), esc(z.Host), portText(z.Port), esc(z.Benutzer), knopf("Ersetzen", "Speichere …", ""), esc(z.ID), knopf("Postfach entfernen", "Entferne …", "k"))
		}
		b.WriteString(`</div>`)
	}
	if w.Mailholer != nil && w.Mailholer.Dokumente != nil {
		d := w.Mailholer.Dokumente
		b.WriteString(`<div class=postfach>`)
		if _, ok := d.zugang(); ok {
			fmt.Fprintf(b, `<p class=zugang>Dokumente: Mails an <b>%s</b> holt das Archiv aus dem Dokumenten-Postfach des Haushalts mit, nur lesend; Paperless legt ihre Anhänge ab, im Archiv steht die Mail selbst.</p>`, esc(d.Adresse()))
		} else {
			fmt.Fprintf(b, `<p class=zugang>Dokumente: Mails an <b>%s</b> kann das Archiv mitholen, sobald der Admin es für dein Archiv freigibt.</p>`, esc(d.Adresse()))
		}
		fehler(d.Adresse(), false)
		b.WriteString(`</div>`)
	}
	if !offen {
		b.WriteString(quelleEnde)
		return
	}
	mailNeu(b, len(konten), qa.gescheitert("mail"))
}

// mailNeu is the form for a mailbox: open when there is none, or its save failed.
func mailNeu(b *strings.Builder, konten int, fehler bool) {
	summary, host := "Postfach eintragen", "imap.gmail.com"
	if konten > 0 {
		summary, host = "Weiteres Postfach hinzufügen", ""
	}
	fmt.Fprintf(b, `<details id=d-mail%s><summary>%s</summary>
<p class=k>Mit einem App-Passwort deines Mail-Anbieters, nicht deinem normalen Passwort (Gmail: Google-Konto → Sicherheit → App-Passwörter; dafür muss die Bestätigung in zwei Schritten an sein). Das Archiv holt jede Stunde die neuen Mails jedes Postfachs ab, auch wenn es zu ist, und ändert auf dem Server nichts, nicht einmal „gelesen“. Bei Gmail liest es „Alle Nachrichten“ (ohne Spam und Papierkorb); deine Gmail-Labels kommen nicht mit.</p>
<form method=post action="/tresor/imap" class=feld>
<label>Name (nur für dich, z. B. Privat oder Arbeit)<input name=name autocomplete=off></label>
<label>Server (für Gmail imap.gmail.com)<input name=host autocomplete=off value="%s"></label>
<label>Port (leer lassen: 993)<input name=port inputmode=numeric autocomplete=off></label>
<label>Benutzername, meist deine Mail-Adresse<input name=benutzer type=email autocomplete=off placeholder="z. B. name@gmail.com"></label>
<label>App-Passwort<input name=passwort type=password autocomplete=new-password></label>
%s</form><p class=hinweis>%s</p></details>`+quelleEnde, offenWenn(konten == 0 || fehler), summary, esc(host), knopf("Speichern", "Speichere …", ""), saysMailPreis)
}

func portText(p int) string {
	if p == 0 {
		return ""
	}
	return strconv.Itoa(p)
}

// aufraeumenKarte: deleting old mail at the provider, for one mailbox.
func (w *Web) aufraeumenKarte(b *strings.Builder, z imapZugang, e Aufraeumen) {
	monate := e.Monate
	if monate == 0 {
		monate = aufraeumenMonate
	}
	dauer := alterText(monate)
	laeuft := w.Aufraeumer.Laeuft() == z.Anzeige()
	formular := func(an bool, knopfText string) string {
		zahl, einheit := monate, "monate"
		if monate%12 == 0 {
			zahl, einheit = monate/12, "jahre"
		}
		gew := func(x string) string {
			if x == einheit {
				return " selected"
			}
			return ""
		}
		wert := "0"
		if an {
			wert = "1"
		}
		felder := ""
		if an {
			felder = fmt.Sprintf(`<label>Älter als<input name=dauer inputmode=numeric value="%d"></label>
<label>Einheit<select name=einheit><option value=monate%s>Monate</option><option value=jahre%s>Jahre</option></select></label>`, zahl, gew("monate"), gew("jahre"))
		}
		return fmt.Sprintf(`<form method=post action="/tresor/mail/aufraeumen" class=feld><input type=hidden name=konto value="%s"><input type=hidden name=an value="%s">%s%s</form>`,
			esc(z.ID), wert, felder, knopf(knopfText, "Speichere …", "k"))
	}
	if !e.An {
		fmt.Fprintf(b, `<details><summary>Alte Mails beim Anbieter löschen</summary>
<p class=k>Aus. Eingeschaltet löscht das Archiv beim Anbieter nur Mails, die älter sind als du sagst und nachweislich in deinem Archiv liegen (gleiche Message-ID und gleicher Inhalt). Nie markierte Mails (Stern), nie Entwürfe, nie etwas, das noch nicht im Archiv ist. Erst zählt es nur und fragt dich; danach löscht es einmal im Monat, solange dein Archiv offen ist. Gmail legt die Mails in seinen Papierkorb, der sich nach 30 Tagen leert.</p>
<p class=achtung>%s</p>%s</details>`, saysAufrNie, formular(true, "Einschalten und zählen"))
		return
	}
	b.WriteString(`<div class=aufraeumen><p><b>Alte Mails beim Anbieter löschen</b> <span class=k>an, älter als ` + esc(dauer) + `</span></p>`)
	switch {
	case laeuft:
		b.WriteString(`<p class=laeuft>Das Archiv sieht gerade die alten Mails dieses Postfachs durch …</p>`)
	case e.Freigabe == nil && (e.Probe == nil || e.Probe.Monate != monate):
		b.WriteString(`<p>Das Archiv zählt gleich, welche Mails älter als ` + esc(dauer) + ` sind und sicher in deinem Archiv liegen. Gelöscht wird erst, wenn du es hier bestätigst.</p>`)
	case e.Freigabe == nil && e.Probe.Fehler != "":
		fmt.Fprintf(b, `<p class="meldung fehler">%s <span class=k>(%s)</span></p>`, esc(e.Probe.Fehler), datum(e.Probe.Am))
	case e.Freigabe == nil && e.Probe.Alt == 0:
		b.WriteString(`<p>Keine Mail ist älter als ` + esc(dauer) + `. Es gibt nichts zu löschen; das Archiv zählt morgen wieder.</p>`)
	case e.Freigabe == nil:
		p := e.Probe
		frage := fmt.Sprintf("%s älter als %s, alle im Archiv – jetzt beim Anbieter löschen?", anzahlT(p.Alt, "Mail", "Mails"), dauer)
		if p.ImArchiv < p.Alt {
			frage = fmt.Sprintf("%s älter als %s. %s davon sind sicher im Archiv und würden gelöscht; %s noch nicht – die bleiben beim Anbieter. Jetzt löschen?",
				anzahlT(p.Alt, "Mail", "Mails"), dauer, tausender(p.ImArchiv), tausender(p.Alt-p.ImArchiv))
		}
		fmt.Fprintf(b, `<p>%s <span class=k>(gezählt am %s)</span></p><p class=achtung>%s</p>`, esc(frage), datum(p.Am), saysAufrNie)
		if p.ImArchiv > 0 {
			fmt.Fprintf(b, `<form method=post action="/tresor/mail/loeschen" class=feld><input type=hidden name=konto value="%s">%s</form>`, esc(z.ID),
				knopf(fmt.Sprintf("Jetzt %s beim Anbieter löschen", anzahlT(p.ImArchiv, "Mail", "Mails")), "Lösche …", ""))
		}
	default:
		b.WriteString(`<p>Läuft einmal im Monat, solange dein Archiv offen ist.</p>`)
		if l := e.Letzter; l != nil && !l.Am.Before(*e.Freigabe) {
			fmt.Fprintf(b, `<p>Zuletzt am %s: %s beim Anbieter gelöscht.</p>`, datum(l.Am), esc(anzahlT(l.Geloescht, "Mail", "Mails")))
			if l.Fehler != "" {
				fmt.Fprintf(b, `<p class="meldung fehler">%s</p>`, esc(l.Fehler))
			}
		} else {
			b.WriteString(`<p>Das erste Löschen beginnt gleich.</p>`)
		}
		fmt.Fprintf(b, `<p class=k>%s</p>`, saysAufrNie)
	}
	fmt.Fprintf(b, `<details><summary>Einstellung ändern</summary>%s%s</details></div>`, formular(true, "Speichern und neu zählen"), formular(false, "Ausschalten"))
}

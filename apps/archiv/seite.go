package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The pages, German and phone first, as Haus and Gesundheit: every
// button answers at once (it says what runs), every form ends on the page
// with a sentence, never a raw error.
//
// Who is who: the instance belongs to one person (ARCHIV_PERSON). Caddy
// lets a browser through after Authelia and adds the proxy secret and
// Remote-User; only that request is the person. An agent comes past
// Authelia under /p/<person>/ (Caddy strips the prefix) with msgvault's
// key as a bearer: it goes to msgvault unchanged while the vault is open.
// Anything else is refused.
//
// Paths: the supervisor's own pages live under /tresor. While the vault
// is open and msgvault answers, every other path is msgvault's own web UI
// (it wants to be at the root: its assets and API are absolute paths, and
// it refuses frames), with its key added by the supervisor so the person
// never sees it. While closed, / is the page to open it.

const proxyHeader = "X-Archiv-Proxy" // the header laib renders for the route's proxySecret

const (
	saysNichtAngemeldet = "Diese Seite gibt es nur nach der Anmeldung über die Startseite des Hauses."
	saysNichtDeins      = "Dieses Archiv gehört jemand anderem. Dein eigenes erreichst du über die Startseite des Hauses."
	saysAbgeschlossen   = "Das Archiv ist abgeschlossen. Die Person, der es gehört, muss es erst mit ihrem Archiv-Passwort öffnen."
	saysStartet         = "Das Archiv startet gerade. Versuch es in einer Minute noch einmal."
	saysPasswortFalsch  = "Das Archiv-Passwort stimmt nicht. Versuch es noch einmal."
	saysPasswortKurz    = "Das Archiv-Passwort braucht mindestens 10 Zeichen."
	saysPasswortAnders  = "Die beiden Passwörter sind verschieden. Gib es zweimal gleich ein."
	saysSchonAngelegt   = "Der Tresor ist schon angelegt."
	saysAnlegenFehler   = "Der Tresor ließ sich nicht anlegen. Sag dem Admin Bescheid."
	saysOeffnenFehler   = "Das Archiv ließ sich nicht öffnen. Sag dem Admin Bescheid."
	saysSchliessenFehl  = "Das Archiv ließ sich nicht abschließen. Versuch es noch einmal; hilft das nicht, sag dem Admin Bescheid."
	saysSpeichernFehler = "Das ließ sich auf der Box nicht speichern. Sag dem Admin Bescheid."
	saysFremdeSeite     = "Diese Anfrage kam von einer fremden Seite und wurde nicht ausgeführt."
	saysArchivWeg       = "Das Archiv-Programm antwortet gerade nicht. Lade die Seite gleich neu."
	saysVerloren        = "Wenn du das Archiv-Passwort verlierst, ist dein Archiv verloren. Niemand kann es wiederherstellen, auch der Admin nicht. Leg es deshalb gleich in Vaultwarden ab."
	saysAdminLiest      = "Solange dein Archiv offen ist, kann der Admin der Box es lesen. Abgeschlossen sieht niemand etwas, auch nicht in den Sicherungen der Box."
	saysMailPreis       = "Dafür liegt das App-Passwort auf der Box, außerhalb des Tresors. Wer Root auf der Box hat, kann damit deine Mails lesen. Du kannst das App-Passwort bei deinem Anbieter jederzeit widerrufen; dann holt das Archiv nichts mehr ab."
	saysBriefkasten     = "Neue Sicherungen liest die Box auch, wenn dein Archiv zu ist: Sie legt die neuen Nachrichten verschlossen in deinen Briefkasten, und beim Öffnen kommen sie ins Archiv. Dafür liegt deine Signal-Passphrase auf der Box, außerhalb des Tresors. Wer Root auf der Box hat, kann damit künftige Sicherungen entschlüsseln, sobald sie ankommen. Was schon in deinem Archiv ist, bleibt nur mit deinem Archiv-Passwort lesbar."
)

var meldungen = map[string]string{
	"angelegt": "Dein Tresor ist angelegt und offen. Hinterlege jetzt unten deine Signal-Passphrase und deine Nummer.",
	"offen":    "Das Archiv ist offen.",
	"zu":       "Das Archiv ist abgeschlossen.",
	"signal":   "Gespeichert. Wartende Signal-Sicherungen liest das Archiv gleich ein.",
	"imap":     "Gespeichert. Das Archiv holt deine Mails jetzt ab, danach jede Stunde.",
}

// Web is the whole HTTP side.
type Web struct {
	Person      string
	Secret      string
	PublicURL   string
	Tresor      *Tresor
	Briefkasten *Briefkasten
	Mailholer   *Mailholer
	Postbote    *Postbote
	Eingang     *Eingang
	Protokoll   *Protokoll
	Schluessel  Schluessel   // outside the vault: the import runs while it is closed
	HausSecret  string       // ARCHIV_HAUS_SECRET: Haus' way to the restore files
	Letzte      string       // /eingang/letzte
	Archiv      http.Handler // msgvault, see archivProxy
	Geaendert   func()
}

type sitzungArt int

const (
	keine sitzungArt = iota
	person
	fremd
)

func (w *Web) sitzung(r *http.Request) sitzungArt {
	got := r.Header.Get(proxyHeader)
	if w.Secret == "" || subtle.ConstantTimeCompare([]byte(got), []byte(w.Secret)) != 1 {
		return keine
	}
	if u := r.Header.Get("Remote-User"); u == "" || u != w.Person {
		return fremd
	}
	return person
}

func bearer(r *http.Request) bool {
	a := r.Header.Get("Authorization")
	return len(a) > 7 && strings.EqualFold(a[:7], "Bearer ")
}

// zaehltAlsNutzung: what keeps an open vault open. A browser tab left
// open polls msgvault's status in the background; that is not use.
func zaehltAlsNutzung(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	p := strings.ToLower(r.URL.Path)
	for _, s := range []string{"/health", "/api/session", "/status", "/stats", "/changes", "/operations", "/telemetry", "/events", "/tresor/stand"} {
		if strings.Contains(p, s) {
			return false
		}
	}
	return true
}

func (w *Web) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		fmt.Fprintln(rw, "ok")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/intern/") {
		w.intern(rw, r)
		return
	}
	switch w.sitzung(r) {
	case person:
		if r.Method == http.MethodPost && !gleicheHerkunft(r) {
			w.antwort(rw, r, http.StatusForbidden, "", saysFremdeSeite)
			return
		}
		if zaehltAlsNutzung(r) {
			w.Tresor.Aktiv()
		}
		if r.URL.Path == "/tresor" || strings.HasPrefix(r.URL.Path, "/tresor/") {
			w.seiten().ServeHTTP(rw, r)
			return
		}
		w.archivOderTuer(rw, r)
	case fremd:
		seite(rw, http.StatusForbidden, "Nicht dein Archiv", "<p>"+saysNichtDeins+"</p>", "")
	default:
		if bearer(r) {
			w.agent(rw, r)
			return
		}
		seite(rw, http.StatusUnauthorized, "Nicht angemeldet", "<p>"+saysNichtAngemeldet+"</p>", "")
	}
}

// gleicheHerkunft refuses a form posted from another site: a browser
// names its Origin, and it must be this host.
func gleicheHerkunft(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" && r.Header.Get("Sec-Fetch-Site") == "same-origin" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return strings.EqualFold(u.Host, host)
}

type schluesselKontext struct{}

// archivOderTuer: msgvault while open and answering, else the way in.
func (w *Web) archivOderTuer(rw http.ResponseWriter, r *http.Request) {
	st := w.Tresor.Stand()
	if key := w.Tresor.APIKey(); key != "" {
		w.Archiv.ServeHTTP(rw, r.WithContext(context.WithValue(r.Context(), schluesselKontext{}, key)))
		return
	}
	if r.URL.Path == "/" {
		w.tresorSeite(rw, r, http.StatusOK, "", "")
		return
	}
	if st.Zustand == Offen {
		seite(rw, http.StatusServiceUnavailable, "Archiv startet", "<p>"+saysStartet+`</p><p><a href="/tresor">Zum Tresor</a></p>`, `<div hidden data-auffrischen=5></div>`)
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		http.Redirect(rw, r, "/tresor", http.StatusSeeOther)
		return
	}
	seite(rw, http.StatusLocked, "Archiv abgeschlossen", "<p>"+saysAbgeschlossen+`</p><p><a href="/tresor">Archiv öffnen</a></p>`, "")
}

// agent: a bearer goes to msgvault unchanged (msgvault checks the key)
// while open; else 423 with the sentence, as JSON.
func (w *Web) agent(rw http.ResponseWriter, r *http.Request) {
	st := w.Tresor.Stand()
	if st.Zustand != Offen {
		jsonFehler(rw, http.StatusLocked, saysAbgeschlossen)
		return
	}
	if w.Tresor.APIKey() == "" {
		jsonFehler(rw, http.StatusServiceUnavailable, saysStartet)
		return
	}
	if zaehltAlsNutzung(r) {
		w.Tresor.Aktiv()
	}
	w.Archiv.ServeHTTP(rw, r)
}

func jsonFehler(rw http.ResponseWriter, code int, satz string) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(code)
	json.NewEncoder(rw).Encode(map[string]string{"fehler": satz})
}

func (w *Web) seiten() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tresor", func(rw http.ResponseWriter, r *http.Request) {
		w.tresorSeite(rw, r, http.StatusOK, meldungen[r.URL.Query().Get("m")], "")
	})
	mux.HandleFunc("GET /tresor/stand", w.stand)
	mux.HandleFunc("POST /tresor/anlegen", w.anlegen)
	mux.HandleFunc("POST /tresor/oeffnen", w.oeffnen)
	mux.HandleFunc("POST /tresor/abschliessen", w.abschliessen)
	mux.HandleFunc("POST /tresor/signal", w.signal)
	mux.HandleFunc("POST /tresor/imap", w.imap)
	return mux
}

// stand is the state as JSON, for Haus' „Archiv öffnen / abschließen“.
func (w *Web) stand(rw http.ResponseWriter, r *http.Request) {
	st := w.Tresor.Stand()
	s := statusBauen(w.lage(st))
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(s)
}

// lage is what the status and the page are made of.
func (w *Web) lage(st Stand) Lage {
	return Lage{Person: w.Person, Stand: st, Wartend: wartende(w.Eingang), Briefkasten: briefkastenStand(w.Briefkasten.Post), Protokoll: w.Protokoll,
		Packt: w.packt(), Liefert: w.Postbote.Laeuft(), FehlenSchluessel: w.Briefkasten.FehlenSchluessel(), URL: w.PublicURL, Jetzt: time.Now()}
}

// antwort ends a form: back to the page with the sentence.
// packt names what becomes a packet now: a backup, or the mail.
func (w *Web) packt() string {
	if l := w.Briefkasten.Laeuft(); l != "" {
		return l
	}
	if w.Mailholer != nil && w.Mailholer.Laeuft() {
		return "neue Mails"
	}
	return ""
}

func (w *Web) antwort(rw http.ResponseWriter, r *http.Request, code int, meldung, fehler string) {
	w.tresorSeite(rw, r, code, meldung, fehler)
}

func (w *Web) anlegen(rw http.ResponseWriter, r *http.Request) {
	pw1, pw2 := []byte(r.PostFormValue("passwort")), []byte(r.PostFormValue("wiederholung"))
	defer nullen(pw1)
	defer nullen(pw2)
	switch {
	case len([]rune(string(pw1))) < 10:
		w.antwort(rw, r, http.StatusBadRequest, "", saysPasswortKurz)
		return
	case subtle.ConstantTimeCompare(pw1, pw2) != 1:
		w.antwort(rw, r, http.StatusBadRequest, "", saysPasswortAnders)
		return
	}
	err := w.Tresor.Anlegen(r.Context(), pw1)
	switch {
	case err == nil:
		http.Redirect(rw, r, "/tresor?m=angelegt", http.StatusSeeOther)
	case errors.Is(err, ErrZustand):
		w.antwort(rw, r, http.StatusConflict, "", saysSchonAngelegt)
	default:
		log.Printf("archiv: anlegen: %v", schwaerzen(err.Error(), pw1))
		w.antwort(rw, r, http.StatusInternalServerError, "", saysAnlegenFehler)
	}
}

func (w *Web) oeffnen(rw http.ResponseWriter, r *http.Request) {
	pw := []byte(r.PostFormValue("passwort"))
	defer nullen(pw)
	if len(pw) == 0 {
		w.antwort(rw, r, http.StatusBadRequest, "", saysPasswortFalsch)
		return
	}
	err := w.Tresor.Oeffnen(r.Context(), pw)
	switch {
	case err == nil:
		http.Redirect(rw, r, "/tresor?m=offen", http.StatusSeeOther)
	case errors.Is(err, ErrFalschesPasswort):
		time.Sleep(time.Second) // a guess costs time, on top of scrypt
		w.antwort(rw, r, http.StatusForbidden, "", saysPasswortFalsch)
	case errors.Is(err, ErrZustand):
		w.antwort(rw, r, http.StatusConflict, "", "Es gibt noch keinen Tresor. Leg ihn zuerst an.")
	default:
		log.Printf("archiv: öffnen: %v", schwaerzen(err.Error(), pw))
		w.antwort(rw, r, http.StatusInternalServerError, "", saysOeffnenFehler)
	}
}

func (w *Web) abschliessen(rw http.ResponseWriter, r *http.Request) {
	if err := w.Tresor.Abschliessen(r.Context(), "von Hand"); err != nil {
		log.Printf("archiv: abschließen: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSchliessenFehl)
		return
	}
	http.Redirect(rw, r, "/tresor?m=zu", http.StatusSeeOther)
}

// offenesFach is the key folder (outside the vault), but only while the
// vault is open: that is how only the person can set a key.
func (w *Web) offenesFach(rw http.ResponseWriter, r *http.Request) (Schluessel, bool) {
	if _, ok := w.Tresor.Innen(); !ok {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
		return Schluessel{}, false
	}
	return w.Schluessel, true
}

func (w *Web) signal(rw http.ResponseWriter, r *http.Request) {
	sch, ok := w.offenesFach(rw, r)
	if !ok {
		return
	}
	pass, tel := r.PostFormValue("passphrase"), r.PostFormValue("telefon")
	if strings.TrimSpace(pass) == "" && strings.TrimSpace(tel) == "" {
		w.antwort(rw, r, http.StatusBadRequest, "", "Gib die Signal-Passphrase oder deine Nummer ein.")
		return
	}
	var p []byte
	var t string
	var err error
	if strings.TrimSpace(pass) != "" {
		if p, err = signalPassphrase(pass); err != nil {
			w.antwort(rw, r, http.StatusBadRequest, "", saysSignalFormat)
			return
		}
		defer nullen(p)
	}
	if strings.TrimSpace(tel) != "" {
		if t, err = telefonnummer(tel); err != nil {
			w.antwort(rw, r, http.StatusBadRequest, "", saysTelefonFormat)
			return
		}
	}
	if p != nil {
		err = sch.Speichern(SchluesselSignal, p)
	}
	if err == nil && t != "" {
		err = sch.Speichern(SchluesselTelefon, []byte(t))
	}
	if err != nil {
		log.Printf("archiv: Schlüssel speichern: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	w.Briefkasten.NeuerSchluessel()
	if w.Geaendert != nil {
		w.Geaendert()
	}
	http.Redirect(rw, r, "/tresor?m=signal", http.StatusSeeOther)
}

func (w *Web) imap(rw http.ResponseWriter, r *http.Request) {
	sch, ok := w.offenesFach(rw, r)
	if !ok {
		return
	}
	b, err := imapJSON(r.PostFormValue("host"), r.PostFormValue("benutzer"), r.PostFormValue("passwort"))
	if err != nil {
		w.antwort(rw, r, http.StatusBadRequest, "", saysIMAPFehlt)
		return
	}
	defer nullen(b)
	if err := sch.Speichern(SchluesselIMAP, b); err != nil {
		log.Printf("archiv: IMAP speichern: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	if w.Mailholer != nil {
		w.Mailholer.NeuerSchluessel()
	}
	if w.Geaendert != nil {
		w.Geaendert()
	}
	http.Redirect(rw, r, "/tresor?m=imap", http.StatusSeeOther)
}

// --- the page

func esc(s string) string { return html.EscapeString(s) }

func knopf(text, laeuft string, klasse string) string {
	k := ""
	if klasse != "" {
		k = " class=" + klasse
	}
	return fmt.Sprintf(`<button%s data-laeuft="%s">%s</button>`, k, esc(laeuft), esc(text))
}

func (w *Web) tresorSeite(rw http.ResponseWriter, r *http.Request, code int, meldung, fehler string) {
	st := w.Tresor.Stand()
	var b strings.Builder
	if meldung != "" {
		fmt.Fprintf(&b, `<p class="meldung ok" role=status>%s</p>`, esc(meldung))
	}
	if fehler != "" {
		fmt.Fprintf(&b, `<p class="meldung fehler" role=alert>%s</p>`, esc(fehler))
	}
	extra := ""
	switch st.Zustand {
	case Uneingerichtet:
		b.WriteString(`<h2>Dein Nachrichten-Archiv einrichten</h2>
<p>Hier landen deine Signal-Nachrichten und deine E-Mails, später auch WhatsApp: durchsuchbar und nur für dich. Alles liegt in einem Tresor, den nur dein Archiv-Passwort öffnet.</p>`)
		fmt.Fprintf(&b, `<p class=hinweis>%s</p><p class=hinweis>%s</p>`, saysVerloren, saysAdminLiest)
		fmt.Fprintf(&b, `<form method=post action="/tresor/anlegen" class=feld>
<input name=konto autocomplete=username value="%s" hidden>
<label>Archiv-Passwort<input type=password name=passwort autocomplete=new-password minlength=10 required></label>
<label>Noch einmal<input type=password name=wiederholung autocomplete=new-password minlength=10 required></label>
%s</form>`, esc("Archiv "+w.Person), knopf("Tresor anlegen", "Lege den Tresor an …", ""))
		b.WriteString(w.eingangText(false))
	case Verschlossen:
		b.WriteString(`<h2>Dein Archiv ist abgeschlossen</h2>`)
		b.WriteString(w.briefkastenText())
		b.WriteString(w.eingangText(false))
		fmt.Fprintf(&b, `<form method=post action="/tresor/oeffnen" class=feld>
<input name=konto autocomplete=username value="%s" hidden>
<label>Archiv-Passwort<input type=password name=passwort autocomplete=current-password required autofocus></label>
%s</form>`, esc("Archiv "+w.Person), knopf("Archiv öffnen", "Öffne das Archiv …", ""))
		fmt.Fprintf(&b, `<p class=k>%s</p>`, saysAdminLiest)
		w.letzteImporte(&b)
	case Offen:
		if !st.Bereit {
			if st.Fehler != "" {
				fmt.Fprintf(&b, `<p class="meldung fehler" role=alert>%s</p>`, esc(st.Fehler))
			} else {
				b.WriteString(`<h2>Dein Archiv startet …</h2><p class=k>Das dauert meist unter einer Minute. Die Seite lädt von selbst neu.</p>`)
				extra = `<div hidden data-auffrischen=4></div>`
			}
		} else {
			b.WriteString(`<h2>Dein Archiv ist offen</h2><p><a class=gross href="/">Archiv durchsuchen</a></p>`)
		}
		fmt.Fprintf(&b, `<form method=post action="/tresor/abschliessen">%s</form>`, knopf("Abschließen", "Schließe ab …", "k"))
		if !st.ZuUm.IsZero() {
			fmt.Fprintf(&b, `<p class=k>Es schließt sich von selbst nach %s ohne Nutzung, also um %s, wenn bis dahin nichts geschieht. %s</p>`,
				dauerText(w.Tresor.Leerlauf), st.ZuUm.Local().Format("15:04"), saysAdminLiest)
		}
		w.importStand(&b)
		if extra == "" && (w.Briefkasten.Laeuft() != "" || w.Postbote.Laeuft() != "" || (w.Mailholer != nil && w.Mailholer.Laeuft())) {
			extra = `<div hidden data-auffrischen=10></div>`
		}
		w.schluesselFormulare(&b)
	}
	seite(rw, code, "Archiv", b.String(), extra)
}

func dauerText(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 {
		if d == time.Hour {
			return "einer Stunde"
		}
		return fmt.Sprintf("%d Stunden", d/time.Hour)
	}
	return fmt.Sprintf("%d Minuten", d/time.Minute)
}

// eingangText says what waits in the inbox, in one paragraph.
func (w *Web) eingangText(offen bool) string {
	ws := wartende(w.Eingang)
	var teile []string
	if n := ws["signal"].Wartend; n > 0 {
		teile = append(teile, anzahl(n, "Signal-Sicherung", "Signal-Sicherungen"))
	}
	for _, q := range []string{"whatsapp", "discord"} {
		if n := ws[q].Wartend; n > 0 {
			teile = append(teile, anzahl(n, quellenName[q]+"-Datei", quellenName[q]+"-Dateien")+" (die liest das Archiv noch nicht ein)")
		}
	}
	if len(teile) == 0 {
		return `<p class=k>Im Eingang wartet nichts.</p>`
	}
	return fmt.Sprintf(`<p>Im Eingang warten %s.</p>`, esc(strings.Join(teile, ", ")))
}

// briefkastenText says how many packets wait for the opening.
func (w *Web) briefkastenText() string {
	bk := briefkastenStand(w.Briefkasten.Post)
	if bk.Wartend == 0 {
		return `<p class=k>Dein Briefkasten ist leer.</p>`
	}
	seit := ""
	if bk.Aeltestes != nil {
		seit = " (das älteste vom " + datum(*bk.Aeltestes) + ")"
	}
	was := "liegt 1 Paket mit neuen Nachrichten"
	if bk.Wartend > 1 {
		was = fmt.Sprintf("liegen %d Pakete mit neuen Nachrichten", bk.Wartend)
	}
	return fmt.Sprintf(`<p>In deinem Briefkasten %s%s. Wenn du das Archiv öffnest, kommen sie hinein.</p>`, was, seit)
}

func anzahl(n int, eins, mehr string) string {
	if n == 1 {
		return "1 " + eins
	}
	return fmt.Sprintf("%d %s", n, mehr)
}

func (w *Web) letzteImporte(b *strings.Builder) {
	ein, fehler, _ := w.Protokoll.Kopie()
	if g, ok := w.Protokoll.Pakete()["signal"]; ok {
		fmt.Fprintf(b, `<p class=k>Letzte Signal-Sicherung gelesen am %s: %s.</p>`, datum(g.Am), anzahl(g.Nachrichten, "neue Nachricht", "neue Nachrichten"))
	}
	if e, ok := ein["signal"]; ok {
		fmt.Fprintf(b, `<p class=k>Zuletzt ins Archiv übernommen am %s: %s.</p>`, datum(e.Am), anzahl(e.Zahlen.Nachrichten(), "Nachricht", "Nachrichten"))
	}
	if g, ok := w.Protokoll.Pakete()["mail"]; ok {
		fmt.Fprintf(b, `<p class=k>Mails zuletzt abgeholt am %s: %s.</p>`, datum(g.Am), anzahl(g.Nachrichten, "neue Mail", "neue Mails"))
	}
	if e, ok := ein["mail"]; ok {
		fmt.Fprintf(b, `<p class=k>Mails zuletzt ins Archiv übernommen am %s: %s.</p>`, datum(e.Am), anzahl(e.Zahlen.Mails, "Mail", "Mails"))
	}
	if fehler != nil {
		fmt.Fprintf(b, `<p class="meldung fehler">%s <span class=k>(%s)</span></p>`, esc(fehler.Satz), datum(fehler.Am))
	}
}

func (w *Web) importStand(b *strings.Builder) {
	b.WriteString(`<section><h3>Einlesen</h3>`)
	if l := w.Briefkasten.Laeuft(); l != "" {
		fmt.Fprintf(b, `<p class=laeuft aria-live=polite>Liest gerade %s …</p>`, esc(l))
	}
	if w.Mailholer != nil && w.Mailholer.Laeuft() {
		b.WriteString(`<p class=laeuft aria-live=polite>Holt gerade neue Mails ab …</p>`)
	}
	if l := w.Postbote.Laeuft(); l != "" {
		fmt.Fprintf(b, `<p class=laeuft aria-live=polite>Übernimmt gerade %s ins Archiv …</p>`, esc(l))
	}
	b.WriteString(w.briefkastenText())
	b.WriteString(w.eingangText(true))
	if w.Briefkasten.FehlenSchluessel() {
		b.WriteString(`<p class="meldung fehler">Signal-Sicherungen warten, aber die Signal-Passphrase oder deine Nummer fehlt. Gib sie unten ein.</p>`)
	}
	w.letzteImporte(b)
	b.WriteString(`</section>`)
}

func (w *Web) schluesselFormulare(b *strings.Builder) {
	if _, ok := w.Tresor.Innen(); !ok {
		return
	}
	sch := w.Schluessel
	hinterlegt := func(name string) string {
		if t, ok := sch.Hinterlegt(name); ok {
			return `<span class=ok>hinterlegt am ` + datum(t) + `</span>`
		}
		return `<span class=k>fehlt</span>`
	}
	fmt.Fprintf(b, `<section><h3>Schlüssel</h3><p class=k>Du kannst sie nur bei offenem Archiv eingeben; sie werden nie wieder angezeigt. Neu eingeben ersetzt sie.</p>
<p class=hinweis>%s</p>
<form method=post action="/tresor/signal" class=feld>
<p><b>Signal</b>: Passphrase %s · Nummer %s</p>
<label>Signal-Passphrase (30 Ziffern, aus Signal → Einstellungen → Chats → Chat-Sicherungen)<input name=passphrase inputmode=numeric autocomplete=off spellcheck=false></label>
<label>Deine Handynummer<input name=telefon type=tel autocomplete=tel placeholder="+49 151 2345678"></label>
%s</form>`, saysBriefkasten, hinterlegt(SchluesselSignal), hinterlegt(SchluesselTelefon), knopf("Speichern", "Speichere …", ""))
	fmt.Fprintf(b, `<form method=post action="/tresor/imap" class=feld>
<p><b>E-Mail (IMAP)</b>: %s</p>
<p class=k>Mit einem App-Passwort deines Mail-Anbieters, nicht deinem normalen Passwort (Gmail: Google-Konto → Sicherheit → App-Passwörter; dafür muss die Bestätigung in zwei Schritten an sein). Das Archiv holt jede Stunde die neuen Mails ab, auch wenn es zu ist, und ändert auf dem Server nichts, nicht einmal „gelesen“. Bei Gmail liest es „Alle Nachrichten“ (ohne Spam und Papierkorb); deine Gmail-Labels kommen nicht mit.</p>
<p class=hinweis>%s</p>
<label>Server<input name=host autocomplete=off placeholder="imap.gmail.com"></label>
<label>Benutzername<input name=benutzer autocomplete=off></label>
<label>App-Passwort<input name=passwort type=password autocomplete=new-password></label>
%s</form></section>`, hinterlegt(SchluesselIMAP), saysMailPreis, knopf("Speichern", "Speichere …", ""))
}

// seite wraps a page: the same head, one stylesheet, one small script.
func seite(rw http.ResponseWriter, code int, titel, body, extra string) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(code)
	fmt.Fprintf(rw, `<!doctype html><html lang=de><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><title>%s</title><style>%s</style>%s</head><body><header><h1><a href="/tresor">Archiv</a></h1></header><main>%s%s</main></body></html>`,
		esc(titel), css, skript, body, extra)
}

// skript: a pressed button says at once what runs (data-laeuft); a page
// marked data-auffrischen reloads itself while nobody types.
const skript = `<script>
document.addEventListener('submit',function(e){var b=e.submitter||e.target.querySelector('button');if(!b||!b.dataset.laeuft)return;setTimeout(function(){b.textContent=b.dataset.laeuft;b.disabled=true;b.setAttribute('aria-busy','true')},0)});
document.addEventListener('DOMContentLoaded',function(){var m=document.querySelector('[data-auffrischen]');if(!m)return;setInterval(function(){var a=document.activeElement;if(a&&(a.tagName==='INPUT'||a.tagName==='BUTTON'))return;var voll=[].some.call(document.querySelectorAll('input:not([hidden])'),function(i){return i.value});if(!voll)location.reload()},(+m.dataset.auffrischen||5)*1000)});
</script>`

const css = `
:root{--fg:#1d2730;--bg:#fff;--k:#5f6b78;--line:#e3e8ee;--akzent:#2b6f8f;--ausser:#b4432f;--ok:#2f7d4f;--flaeche:#f4f7f9}
@media(prefers-color-scheme:dark){:root{--fg:#e7ecf0;--bg:#141a1f;--k:#a3aeb9;--line:#2a333c;--akzent:#7fb6d2;--ausser:#e88a77;--ok:#6cc08b;--flaeche:#1b232a}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.45 system-ui,sans-serif}
header{padding:.6rem 1rem;border-bottom:1px solid var(--line)}header h1{margin:0;font-size:1.15rem}header a{color:inherit;text-decoration:none}
main{max-width:40rem;margin:0 auto;padding:1rem}
a{color:var(--akzent)}a.gross{font-size:1.1rem;font-weight:600}
h2{font-size:1.25rem;margin:.4rem 0 .6rem}h3{font-size:1rem;margin:1.6rem 0 .4rem}
.k{color:var(--k);font-size:.92rem}.ok{color:var(--ok)}
.hinweis{padding:.6rem .8rem;border-left:3px solid var(--akzent);background:var(--flaeche);border-radius:0 8px 8px 0}
.meldung{padding:.6rem .8rem;border-radius:8px;background:var(--flaeche)}.meldung.ok{border-left:3px solid var(--ok);color:inherit}.meldung.fehler{border-left:3px solid var(--ausser)}
.laeuft{font-weight:500}
form{margin:.8rem 0}.feld label{display:block;margin:.6rem 0;font-size:.95rem}
input{display:block;font:inherit;padding:.5rem .6rem;margin-top:.2rem;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:inherit;width:100%;max-width:420px;box-sizing:border-box}
button{font:inherit;padding:.55rem 1.1rem;border:1px solid var(--akzent);border-radius:8px;background:var(--akzent);color:#fff;min-height:44px}button.k{background:none;color:var(--fg);border-color:var(--line)}
button[aria-busy=true]{opacity:.6;cursor:progress}
section{margin-top:1.2rem;border-top:1px solid var(--line)}
`

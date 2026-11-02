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
	"os"
	"strconv"
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
	saysWAFormat        = "Der WhatsApp-Schlüssel hat 64 Zeichen, Ziffern und die Buchstaben a bis f. Leerzeichen dürfen dazwischen stehen."
	saysAdminLiest      = "Solange dein Archiv offen ist, kann der Admin der Box es lesen. Abgeschlossen sieht niemand etwas, auch nicht in den Sicherungen der Box."
	saysMailPreis       = "Dafür liegt das App-Passwort auf der Box, außerhalb des Tresors. Wer Root auf der Box hat, kann damit deine Mails lesen. Du kannst das App-Passwort bei deinem Anbieter jederzeit widerrufen; dann holt das Archiv nichts mehr ab."
	saysSignalHinweis   = "Signal-Sicherungen liest das Archiv, während es offen ist; bis dahin warten sie verschlüsselt im Eingang. Deine Signal-Passphrase liegt im Tresor: Ist das Archiv abgeschlossen, kann sie niemand lesen, auch der Admin nicht."
	saysSignalZu        = "Die Signal-Passphrase trägst du bei offenem Archiv ein. Sie liegt im Tresor; deshalb liest das Archiv Signal nur, wenn es offen ist."
)

var meldungen = map[string]string{
	"angelegt":    "Dein Tresor ist angelegt und offen. Hinterlege jetzt unten deine Signal-Passphrase und deine Nummer.",
	"offen":       "Das Archiv ist offen.",
	"zu":          "Das Archiv ist abgeschlossen.",
	"signal":      "Gespeichert. Wartende Signal-Sicherungen liest das Archiv gleich ein.",
	"imap":        "Gespeichert. Das Archiv holt deine Mails jetzt ab, danach jede Stunde.",
	"whatsapp":    "Gespeichert. Das Archiv liest deine WhatsApp-Sicherung gleich ein.",
	"passkey-weg": "Der Passkey ist entfernt; er öffnet das Archiv nicht mehr.",
	"person-ja":   "Gemerkt: Das ist dieselbe Person.",
	"person-nein": "Gemerkt: Das sind verschiedene Personen. Das Archiv fragt nicht wieder.",
	"zusammen":    "Die beiden sind jetzt eine Person.",
	"abgleich":    "Das Archiv gleicht deine Kontakte und Kalender jetzt ab.",
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
	Puls        *Puls // open pages listen here (/tresor/live)
	Passkeys    *Passkeys
	Abgleich    *Abgleich      // contacts and calendars from Radicale (nil: not set up)
	Personen    *Personenkarte // „Ist das dieselbe Person?“
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
	for _, s := range []string{"/health", "/api/session", "/status", "/stats", "/changes", "/operations", "/telemetry", "/events", "/tresor/stand", "/tresor/live"} {
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
		seite(rw, http.StatusServiceUnavailable, "Archiv startet", "<p>"+saysStartet+`</p><p><a href="/tresor">Zum Tresor</a></p>`, `<div hidden data-bis-bereit></div>`)
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
	mux.HandleFunc("GET /tresor/live", w.live)
	mux.HandleFunc("POST /tresor/anlegen", w.anlegen)
	mux.HandleFunc("POST /tresor/oeffnen", w.oeffnen)
	mux.HandleFunc("GET /tresor/passkey/optionen", w.passkeyOptionen)
	mux.HandleFunc("POST /tresor/oeffnen-passkey", w.oeffnenPasskey)
	mux.HandleFunc("POST /tresor/passkey/weg", w.passkeyWeg)
	mux.HandleFunc("POST /tresor/abschliessen", w.abschliessen)
	mux.HandleFunc("POST /tresor/signal", w.signal)
	mux.HandleFunc("POST /tresor/imap", w.imap)
	mux.HandleFunc("POST /tresor/whatsapp", w.whatsapp)
	mux.HandleFunc("POST /tresor/abgleich", w.abgleichJetzt)
	mux.HandleFunc("POST /tresor/personen/entscheiden", w.entscheiden)
	mux.HandleFunc("GET /tresor/personen/vorschau", w.vorschau)
	mux.HandleFunc("POST /tresor/personen/zusammen", w.zusammen)
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
		Packt: w.packt(), Liefert: w.Postbote.Laeuft(), FehlenSchluessel: w.fehlenSignalSchluessel(), URL: w.PublicURL, Jetzt: time.Now(), Abgleich: w.Abgleich}
}

// fehlenSignalSchluessel: Signal backups wait, and the passphrase or the
// number is not stored. The passphrase is inside the vault: while it is
// closed nobody can tell, so then only the number counts.
func (w *Web) fehlenSignalSchluessel() bool {
	if wartende(w.Eingang)["signal"].Wartend == 0 {
		return false
	}
	_, t := w.Schluessel.Hinterlegt(SchluesselTelefon)
	if innen, auf := w.Tresor.Innen(); auf {
		if _, p := signalHinterlegt(innen); !p {
			return true
		}
	}
	return !t
}

// signalHinterlegt says when the passphrase was stored in the open vault.
func signalHinterlegt(innen Innen) (time.Time, bool) {
	fi, err := os.Stat(innen.SignalPassphrase())
	if err != nil || fi.Size() == 0 {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// packt names what becomes a packet now: the mail.
func (w *Web) packt() string {
	if w.Mailholer != nil && w.Mailholer.Laeuft() {
		return "neue Mails"
	}
	return ""
}

// antwort ends a form: back to the page with the sentence.
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
	warZu := w.Tresor.Stand().Zustand == Verschlossen
	err := w.Tresor.Oeffnen(r.Context(), pw)
	switch {
	case err == nil && warZu && r.PostFormValue("pk_id") != "":
		// the password opened a closed vault: it is the right one to keep for the passkey
		w.antwort(rw, r, http.StatusOK, w.passkeyNachOeffnen(r, pw), "")
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

// signal stores the passphrase inside the vault (Innen.SignalPassphrase)
// and the number outside, both only while the vault is open.
func (w *Web) signal(rw http.ResponseWriter, r *http.Request) {
	sch, ok := w.offenesFach(rw, r)
	if !ok {
		return
	}
	innen, auf := w.Tresor.Innen()
	if !auf {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
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
		err = schreibenAtomar(innen.SignalPassphrase(), p, 0o600)
	}
	if err == nil && t != "" {
		err = sch.Speichern(SchluesselTelefon, []byte(t))
	}
	if err != nil {
		log.Printf("archiv: Schlüssel speichern: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	w.Postbote.NeuerSignalSchluessel()
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
	var alt imapZugang
	if roh, err := sch.Lesen(SchluesselIMAP); err == nil {
		json.Unmarshal(roh, &alt)
		nullen(roh)
		alt.Passwort = ""
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
		if alt.Benutzer != "" && (!strings.EqualFold(alt.Benutzer, strings.TrimSpace(r.PostFormValue("benutzer"))) || !strings.EqualFold(alt.Host, strings.TrimSpace(r.PostFormValue("host")))) {
			w.Mailholer.KontoGewechselt()
		}
		w.Mailholer.NeuerSchluessel()
	}
	if w.Geaendert != nil {
		w.Geaendert()
	}
	http.Redirect(rw, r, "/tresor?m=imap", http.StatusSeeOther)
}

// whatsapp stores the key inside the vault (as Signal's passphrase):
// WhatsApp's database holds the whole history every day, so it can wait
// for the opening, and the key then is never readable on a closed box.
func (w *Web) whatsapp(rw http.ResponseWriter, r *http.Request) {
	innen, ok := w.Tresor.Innen()
	if !ok {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
		return
	}
	k, err := whatsappSchluessel(r.PostFormValue("schluessel"))
	if err != nil {
		w.antwort(rw, r, http.StatusBadRequest, "", saysWAFormat)
		return
	}
	defer nullen(k)
	if err := os.WriteFile(innen.WhatsAppSchluessel(), k, 0o600); err != nil {
		log.Printf("archiv: WhatsApp-Schlüssel speichern: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	w.Postbote.NeuerWASchluessel()
	if w.Geaendert != nil {
		w.Geaendert()
	}
	http.Redirect(rw, r, "/tresor?m=whatsapp", http.StatusSeeOther)
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
		b.WriteString(`<h2>Dein Archiv ist abgeschlossen</h2><div id=briefkasten data-live>` + w.briefkastenText() + `</div>`)
		if len(w.Passkeys.Liste()) > 0 {
			b.WriteString(`<form method=post action="/tresor/oeffnen-passkey" id=mit-passkey hidden><input type=hidden name=id><input type=hidden name=prf>` + knopf("Mit Passkey öffnen", "Öffne das Archiv …", "") + `</form><p class=k id=oder-passwort hidden>oder mit dem Archiv-Passwort:</p>`)
		}
		fmt.Fprintf(&b, `<form method=post action="/tresor/oeffnen" class=feld id=mit-passwort>
<input name=konto autocomplete=username value="%s" hidden>
<label>Archiv-Passwort<input type=password name=passwort autocomplete=current-password required autofocus></label>
<label class=haken id=passkey-neu hidden><input type=checkbox name=pk_neu value=1> Künftig mit Passkey öffnen (dieses Gerät fragt nach Fingerabdruck oder PIN)</label>
<input type=hidden name=pk_id><input type=hidden name=pk_salt><input type=hidden name=pk_prf>
%s</form>`, esc("Archiv "+w.Person), knopf("Archiv öffnen", "Öffne das Archiv …", ""))
		fmt.Fprintf(&b, `<p class=k>%s Schlüssel ändern kannst du, wenn es offen ist.</p>`, saysAdminLiest)
		w.laeuftJetzt(&b)
		w.signalKarte(&b, false)
		w.mailKarte(&b, false)
		w.smsKarte(&b)
		w.whatsappKarte(&b)
		w.kontakteKarte(&b, false)
	case Offen:
		b.WriteString(`<div id=kopf data-live>`)
		if !st.Bereit {
			if st.Fehler != "" {
				fmt.Fprintf(&b, `<p class="meldung fehler" role=alert>%s</p>`, esc(st.Fehler))
			} else {
				b.WriteString(`<h2>Dein Archiv startet …</h2><p class=k>Das dauert meist unter einer Minute. Sobald es bereit ist, erscheint hier „Archiv durchsuchen“.</p>`)
			}
		} else {
			b.WriteString(`<h2>Dein Archiv ist offen</h2><p><a class=gross href="/">Archiv durchsuchen</a></p>`)
		}
		fmt.Fprintf(&b, `<form method=post action="/tresor/abschliessen">%s</form>`, knopf("Abschließen", "Schließe ab …", "k"))
		if !st.ZuUm.IsZero() {
			fmt.Fprintf(&b, `<p class=k>Es schließt sich von selbst nach %s ohne Nutzung, also um %s, wenn bis dahin nichts geschieht. %s</p>`,
				dauerText(w.Tresor.Leerlauf), st.ZuUm.Local().Format("15:04"), saysAdminLiest)
		}
		b.WriteString(`</div>`)
		w.laeuftJetzt(&b)
		b.WriteString(`<div id=briefkasten data-live>`)
		if briefkastenStand(w.Briefkasten.Post).Wartend > 0 && w.Postbote.Laeuft() == "" {
			b.WriteString(w.briefkastenText())
		}
		b.WriteString(`</div>`)
		w.signalKarte(&b, true)
		w.mailKarte(&b, true)
		w.smsKarte(&b)
		w.whatsappKarte(&b)
		w.kontakteKarte(&b, true)
		if st.Bereit {
			w.personenKarte(&b)
		}
		w.passkeyKarte(&b)
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

// --- what runs now

var monate = [...]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}

func monat(t time.Time) string {
	t = t.Local()
	return monate[t.Month()-1] + " " + strconv.Itoa(t.Year())
}

// tausender writes 41437 as 41.437.
func tausender(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "." + s[i:]
	}
	return s
}

func uhrzeit(t time.Time) string {
	if t.Local().YearDay() == time.Now().Local().YearDay() {
		return t.Local().Format("15:04")
	}
	return datum(t)
}

// laeuftJetzt says what runs now, each with what is known of its progress.
func (w *Web) laeuftJetzt(b *strings.Builder) bool {
	var zeilen []string
	if w.Mailholer != nil {
		if g := w.Mailholer.Gang(); g.Was != "" {
			fortschritt, text := "<progress></progress>", "Sie fragt dein Postfach, was neu ist."
			if g.Gesamt > 0 {
				fortschritt = fmt.Sprintf(`<progress value=%d max=%d></progress>`, g.Fertig, g.Gesamt)
				text = fmt.Sprintf("%s von %s Mails abgeholt", tausender(g.Fertig), tausender(g.Gesamt))
				if !g.Bis.IsZero() {
					text += ", bisher bis " + monat(g.Bis) + " (die ältesten kommen zuerst)"
				}
				text += "."
			}
			zeilen = append(zeilen, fmt.Sprintf(`<p><b>E-Mails abholen</b> <span class=k>seit %s</span>%s<span class=k>%s</span></p>`, uhrzeit(g.Seit), fortschritt, esc(text)))
		}
	}
	if g := w.Postbote.Gang(); g.Was != "" {
		was := "Nachrichten"
		if g.Quelle == "mail" {
			was = "Mails"
		} else if g.Quelle == "signal" {
			was = "Signal-Nachrichten"
		} else if g.Quelle == "sms" {
			was = "SMS und Anrufe vom Handy"
		} else if g.Quelle == "whatsapp" {
			was = "WhatsApp-Nachrichten"
		}
		menge := ""
		if g.Gesamt > 0 {
			menge = tausender(g.Gesamt) + " "
		}
		stand := "Wie weit es ist, meldet das Archiv erst am Ende."
		if g.Fertig > 0 && g.Gesamt == 0 { // WhatsApp and Signal count as they go, without knowing the whole
			stand = fmt.Sprintf("Bisher %s durchgesehen, davon %s neu im Archiv.", tausender(g.Fertig), tausender(g.Neu))
		} else if g.Quelle == "signal" {
			stand = "Erst entschlüsselt die Box die Sicherung, das dauert ein paar Minuten; dann zählt sie hier mit."
		}
		zeilen = append(zeilen, fmt.Sprintf(`<p><b>%s%s ins Archiv übernehmen</b> <span class=k>seit %s</span><progress></progress><span class=k>%s Bitte nicht abschließen, solange das läuft.</span></p>`, menge, was, uhrzeit(g.Seit), esc(stand)))
	}
	if len(zeilen) == 0 {
		b.WriteString(`<section class=jetzt id=jetzt data-live hidden></section>`) // the place, for the live page
		return false
	}
	b.WriteString(`<section class=jetzt id=jetzt data-live aria-live=polite><h3>Gerade in Arbeit</h3>` + strings.Join(zeilen, "") + `</section>`)
	return true
}

// --- one card per source

func (w *Web) fehlerVon(b *strings.Builder, quelle string) {
	_, f, _ := w.Protokoll.Kopie()
	if f != nil && f.Quelle == quelle {
		fmt.Fprintf(b, `<p class="meldung fehler">%s <span class=k>(%s)</span></p>`, esc(f.Satz), datum(f.Am))
		if f.Mal >= DreimalRot {
			fmt.Fprintf(b, `<p class=k>Das ist jetzt %d-mal hintereinander passiert. Der Admin-Agent der Box ist benachrichtigt und sieht sich den Grund an.</p>`, f.Mal)
		}
	}
}

func (w *Web) imAchiv(b *strings.Builder, quelle, eins, mehr string) {
	ein, _, _ := w.Protokoll.Kopie()
	e, ok := ein[quelle]
	if !ok {
		b.WriteString(`<p class=k>Noch nichts im Archiv.</p>`)
		return
	}
	n := e.Zahlen.Nachrichten()
	if quelle == "mail" {
		n = e.Zahlen.Mails
	}
	fmt.Fprintf(b, `<p>Zuletzt ins Archiv übernommen: %s, am %s.</p>`, esc(anzahl(n, eins, mehr)), datum(e.Am))
}

func (w *Web) signalKarte(b *strings.Builder, offen bool) {
	b.WriteString(`<section class=quelle id=karte-signal data-live><h3>Signal</h3>`)
	w.imAchiv(b, "signal", "Nachricht", "Nachrichten")
	if n := wartende(w.Eingang)["signal"].Wartend; n > 0 {
		fmt.Fprintf(b, `<p>%s im Eingang.</p>`, esc(anzahl(n, "Sicherung wartet", "Sicherungen warten")))
	}
	w.fehlerVon(b, "signal")
	sch := w.Schluessel
	innen, auf := w.Tresor.Innen()
	pt, pOK := time.Time{}, false
	if auf {
		pt, pOK = signalHinterlegt(innen)
	}
	_, tOK := sch.Hinterlegt(SchluesselTelefon)
	zugang := []string{}
	switch {
	case !offen || !auf:
		// inside the closed vault: the page cannot tell, and says where it is
	case pOK:
		zugang = append(zugang, "Passphrase hinterlegt am "+datum(pt))
	default:
		zugang = append(zugang, `<span class=fehlt>Passphrase fehlt</span>`)
	}
	if tOK {
		tel, _ := sch.Lesen(SchluesselTelefon)
		zugang = append(zugang, "Nummer "+esc(maskiert(string(tel))))
	} else {
		zugang = append(zugang, `<span class=fehlt>Nummer fehlt</span>`)
	}
	fmt.Fprintf(b, `<p class=zugang>%s</p>`, strings.Join(zugang, " · "))
	if !offen || !auf {
		fmt.Fprintf(b, `<p class=k>%s</p></section>`, saysSignalZu)
		return
	}
	komplett := pOK && tOK
	summary := "Signal einrichten"
	if komplett {
		summary = "Passphrase oder Nummer ändern"
	}
	fmt.Fprintf(b, `<details id=d-signal%s><summary>%s</summary>
<p class=k>Was du hier einträgst, ersetzt das Bisherige. Ein leeres Feld bleibt, wie es ist. Hinterlegtes zeigt die Seite nie wieder an.</p>
<form method=post action="/tresor/signal" class=feld>
<label>Signal-Passphrase (30 Ziffern, aus Signal → Einstellungen → Chats → Chat-Sicherungen)<input name=passphrase inputmode=numeric autocomplete=off spellcheck=false></label>
<label>Deine Handynummer<input name=telefon type=tel autocomplete=tel placeholder="z. B. +49 151 2345678"></label>
%s</form><p class=hinweis>%s</p></details></section>`, offenWenn(!komplett), summary, knopf(map[bool]string{true: "Ersetzen", false: "Speichern"}[komplett], "Speichere …", ""), saysSignalHinweis)
}

func (w *Web) mailKarte(b *strings.Builder, offen bool) {
	b.WriteString(`<section class=quelle id=karte-mail data-live><h3>E-Mail</h3>`)
	w.imAchiv(b, "mail", "Mail", "Mails")
	w.fehlerVon(b, "mail")
	sch := w.Schluessel
	var z imapZugang
	t, ok := sch.Hinterlegt(SchluesselIMAP)
	if ok {
		if roh, err := sch.Lesen(SchluesselIMAP); err == nil {
			json.Unmarshal(roh, &z)
			nullen(roh)
			z.Passwort = ""
		}
		fmt.Fprintf(b, `<p class=zugang>Postfach <b>%s</b> auf %s · App-Passwort hinterlegt am %s</p>`, esc(z.Benutzer), esc(z.Host), datum(t))
	} else {
		b.WriteString(`<p class=zugang><span class=fehlt>Kein Postfach eingetragen</span></p>`)
	}
	if !offen {
		b.WriteString(`</section>`)
		return
	}
	host, summary, erklaerung, knopfText := "imap.gmail.com", "Postfach eintragen", "", "Speichern"
	if ok {
		host, summary, knopfText = z.Host, "Neues App-Passwort oder anderes Postfach", "Ersetzen"
		erklaerung = fmt.Sprintf(`<p class=k>Das Archiv holt ein Postfach. Was du hier speicherst, ersetzt %s. Mails, die schon im Archiv sind, bleiben drin.</p>`, esc(z.Benutzer))
	}
	fmt.Fprintf(b, `<details id=d-mail%s><summary>%s</summary>%s
<p class=k>Mit einem App-Passwort deines Mail-Anbieters, nicht deinem normalen Passwort (Gmail: Google-Konto → Sicherheit → App-Passwörter; dafür muss die Bestätigung in zwei Schritten an sein). Das Archiv holt jede Stunde die neuen Mails ab, auch wenn es zu ist, und ändert auf dem Server nichts, nicht einmal „gelesen“. Bei Gmail liest es „Alle Nachrichten“ (ohne Spam und Papierkorb); deine Gmail-Labels kommen nicht mit.</p>
<form method=post action="/tresor/imap" class=feld>
<label>Server (für Gmail so lassen)<input name=host autocomplete=off value="%s"></label>
<label>Benutzername, meist deine Mail-Adresse<input name=benutzer type=email autocomplete=off value="%s" placeholder="z. B. name@gmail.com"></label>
<label>App-Passwort<input name=passwort type=password autocomplete=new-password></label>
%s</form><p class=hinweis>%s</p></details></section>`, offenWenn(!ok), summary, erklaerung, esc(host), esc(z.Benutzer), knopf(knopfText, "Speichere …", ""), saysMailPreis)
}

func (w *Web) smsKarte(b *strings.Builder) {
	b.WriteString(`<section class=quelle id=karte-sms data-live><h3>SMS und Anrufe</h3>`)
	ein, _, _ := w.Protokoll.Kopie()
	if e, ok := ein["sms"]; ok {
		fmt.Fprintf(b, `<p>Zuletzt ins Archiv übernommen: %s und %s, am %s.</p>`, esc(anzahl(e.Zahlen.SMS+e.Zahlen.MMS, "Nachricht", "Nachrichten")), esc(anzahl(e.Zahlen.Anrufe, "Anruf", "Anrufe")), datum(e.Am))
	} else {
		b.WriteString(`<p class=k>Noch nichts im Archiv.</p>`)
	}
	if n := wartende(w.Eingang)["sms"].Wartend; n > 0 {
		fmt.Fprintf(b, `<p>%s im Eingang, verschlüsselt. Beim Öffnen kommen sie ins Archiv.</p>`, esc(anzahl(n, "Datei wartet", "Dateien warten")))
	}
	w.fehlerVon(b, "sms")
	if _, ok := w.Schluessel.Hinterlegt(SchluesselTelefon); !ok {
		b.WriteString(`<p class=zugang><span class=fehlt>Deine Nummer fehlt</span> (bei Signal eintragen): ohne sie weiß das Archiv nicht, welche SMS du geschrieben hast.</p>`)
	}
	b.WriteString(`<p class=k>Kommen aus der Haus-App: Mehr → Nachrichten sichern → „SMS und Anrufliste übertragen“. Die App verschlüsselt sie schon auf dem Handy für dein Archiv; auf der Box kann sie niemand lesen, solange es zu ist.</p></section>`)
}

// kontakteKarte: the address book and the calendars. Their numbers live
// in the vault: while closed the card only says what happens.
func (w *Web) kontakteKarte(b *strings.Builder, offen bool) {
	b.WriteString(`<section class=quelle id=karte-kontakte data-live><h3>Kontakte und Kalender</h3>`)
	b.WriteString(`<p class=k>Die Haus-App spiegelt die Kontakte deines Handys jeden Tag in dein Adressbuch „Handy-Kontakte“ auf der Box (Mehr → Nachrichten sichern → „Kontakte übertragen“); deine Google-Kalender und deine eigenen Kalender liegen dort auch. Ist dein Archiv offen, liest es beides alle paar Stunden. Es ändert dort nichts.</p>`)
	a := w.Abgleich
	switch {
	case !a.Eingerichtet():
		fmt.Fprintf(b, `<p class=zugang><span class=fehlt>%s</span></p></section>`, esc(saysRadicaleFehlt))
		return
	case !offen:
		b.WriteString(`<p class=k>Was im Archiv ist, siehst du, wenn es offen ist: Die Termine liegen im Tresor.</p></section>`)
		return
	}
	st := a.Stand()
	if st.Laeuft {
		b.WriteString(`<p class=laeuft>Gleicht gerade ab …</p>`)
	}
	if st.Fehler != "" {
		fmt.Fprintf(b, `<p class="meldung fehler">%s</p>`, esc(st.Fehler))
	}
	// contacts
	k := st.Kontakte
	switch {
	case k.Fehler != "":
		fmt.Fprintf(b, `<p class="meldung fehler">%s</p>`, esc(k.Fehler))
	case k.Leer:
		fmt.Fprintf(b, `<p>%s</p>`, esc(saysAdressbuchLeer))
	}
	if !k.Am.IsZero() {
		was := fmt.Sprintf("Kontakte zuletzt abgeglichen um %s", uhrzeit(k.Am))
		if k.Neu+k.Geaendert+k.Entfernt > 0 {
			was += fmt.Sprintf(" (%d neu, %d geändert, %d auf dem Handy gelöscht)", k.Neu, k.Geaendert, k.Entfernt)
		}
		if st.Personen > 0 {
			was += "; " + anzahl(st.Personen, "Person", "Personen") + " im Archiv"
		}
		fmt.Fprintf(b, `<p>%s.</p>`, esc(was))
	}
	// calendars
	if st.KalenderFehler != "" {
		fmt.Fprintf(b, `<p class="meldung fehler">%s</p>`, esc(st.KalenderFehler))
	}
	if len(st.Kalender) == 0 {
		if !st.KalenderAm.IsZero() {
			b.WriteString(`<p class=k>Kein Kalender gefunden (Google-Kalender verbindest du in der Haus-App).</p>`)
		}
	} else {
		b.WriteString(`<ul class=kalender>`)
		for _, z := range st.Kalender {
			zeile := esc(kalenderName(z)) + ": " + esc(anzahl(z.Termine, "Termin", "Termine"))
			if z.Geloescht > 0 {
				zeile += esc(fmt.Sprintf(", davon %d an der Quelle gelöscht (bleiben im Archiv)", z.Geloescht))
			}
			if z.QuelleWegSeit != nil {
				zeile += esc(" — der Kalender ist an der Quelle gelöscht am " + datum(*z.QuelleWegSeit) + "; seine Termine bleiben im Archiv")
			}
			fmt.Fprintf(b, `<li>%s</li>`, zeile)
		}
		b.WriteString(`</ul>`)
		if !st.KalenderAm.IsZero() {
			fmt.Fprintf(b, `<p class=k>Zuletzt gelesen um %s. Im Archiv bleibt jeder Termin, den es einmal gesehen hat, auch wenn er an der Quelle gelöscht wird. In den Terminen suchen kannst du in einem späteren Schritt.</p>`, uhrzeit(st.KalenderAm))
		}
	}
	if !st.Laeuft {
		fmt.Fprintf(b, `<form method=post action="/tresor/abgleich">%s</form>`, knopf("Jetzt abgleichen", "Gleicht ab …", "k"))
	}
	b.WriteString(`</section>`)
}

func (w *Web) whatsappKarte(b *strings.Builder) {
	b.WriteString(`<section class=quelle id=karte-whatsapp data-live><h3>WhatsApp</h3>`)
	w.imAchiv(b, "whatsapp", "neue Nachricht", "neue Nachrichten")
	if n := wartende(w.Eingang)["whatsapp"].Wartend; n > 0 {
		fmt.Fprintf(b, `<p>%s im Eingang.</p>`, esc(anzahl(n, "Sicherung liegt", "Sicherungen liegen")))
	}
	w.fehlerVon(b, "whatsapp")
	innen, auf := w.Tresor.Innen()
	if !auf {
		b.WriteString(`<p class=k>Den Schlüssel trägst du bei offenem Archiv ein. Er liegt im Tresor; deshalb liest das Archiv WhatsApp nur, wenn es offen ist.</p></section>`)
		return
	}
	fi, err := os.Stat(innen.WhatsAppSchluessel())
	hat := err == nil
	if hat {
		fmt.Fprintf(b, `<p class=zugang>Schlüssel hinterlegt am %s</p>`, datum(fi.ModTime()))
	} else {
		b.WriteString(`<p class=zugang><span class=fehlt>Schlüssel fehlt</span></p>`)
	}
	if _, ok := w.Schluessel.Hinterlegt(SchluesselTelefon); !ok {
		b.WriteString(`<p class=zugang><span class=fehlt>Deine Nummer fehlt</span> (bei Signal eintragen).</p>`)
	}
	summary, knopfText := "Schlüssel eintragen", "Speichern"
	if hat {
		summary, knopfText = "Schlüssel ersetzen", "Ersetzen"
	}
	fmt.Fprintf(b, `<details id=d-whatsapp%s><summary>%s</summary>
<p class=k>Der 64-stellige Schlüssel deines WhatsApp-Backups, so wie WhatsApp ihn gezeigt hat (Leerzeichen sind egal). Er liegt im Tresor; nur bei offenem Archiv liest es deine WhatsApp-Sicherung, jeden Tag die neueste. Fotos und Videos aus WhatsApp kommen noch nicht ins Archiv.</p>
<form method=post action="/tresor/whatsapp" class=feld>
<label>WhatsApp-Schlüssel (64 Zeichen)<input name=schluessel autocomplete=off spellcheck=false autocapitalize=off></label>
%s</form></details>
<details id=d-whatsapp-passkey><summary>Kein 64-stelliger Schlüssel, sondern Passkey oder Passwort?</summary><p class=k>Aus einem Passkey lässt sich der Schlüssel nicht auslesen. So stellst du um:</p><ol>
<li>WhatsApp → Einstellungen → Chats → Chat-Backup → Ende-zu-Ende-verschlüsseltes Backup → <b>Deaktivieren</b> (mit deinem Passkey bestätigen).</li>
<li>Gleich danach wieder <b>Aktivieren</b> und „Stattdessen 64-stelligen Schlüssel verwenden“ wählen. Dazwischen liegt dein Backup unverschlüsselt bei Google, also direkt nacheinander.</li>
<li>Den Schlüssel in Vaultwarden ablegen und hier eintragen.</li>
<li>„Jetzt sichern“ tippen, in WhatsApp und danach in der Haus-App.</li></ol></details></section>`, offenWenn(!hat), summary, knopf(knopfText, "Speichere …", ""))
}

func offenWenn(b bool) string {
	if b {
		return " open"
	}
	return ""
}

// maskiert shows a number by its ends only: +49 … 67.
func maskiert(tel string) string {
	if len(tel) < 7 {
		return "…"
	}
	return tel[:3] + " … " + tel[len(tel)-2:]
}

// seite wraps a page: the same head, one stylesheet, one small script.
func seite(rw http.ResponseWriter, code int, titel, body, extra string) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(code)
	fmt.Fprintf(rw, `<!doctype html><html lang=de><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><title>%s</title><style>%s</style>%s</head><body><header><h1><a href="/tresor">Archiv</a></h1></header><main>%s%s</main></body></html>`,
		esc(titel), css, skript, body, extra)
}

// skript: a pressed button says at once what runs (data-laeuft). The page
// is live: it listens on /tresor/live and, on a change, fetches itself and
// swaps only the blocks marked data-live, by id — scroll position, an open
// <details> and a half-typed field stay as they are. A block the person is
// typing in waits for the next change. The page in front of msgvault
// while it starts (data-bis-bereit) asks /tresor/stand and goes on once
// msgvault answers.
const skript = `<script>
document.addEventListener('submit',function(e){var b=e.submitter||e.target.querySelector('button');if(!b||!b.dataset.laeuft)return;setTimeout(function(){b.textContent=b.dataset.laeuft;b.disabled=true;b.setAttribute('aria-busy','true')},0)});
document.addEventListener('DOMContentLoaded',function(){
if(!window.PublicKeyCredential||!window.fetch)return;
['passkey-neu','mit-passkey','oder-passwort'].forEach(function(i){var e=document.getElementById(i);if(e)e.hidden=false});
function ab(s){s=s.replace(/-/g,'+').replace(/_/g,'/');while(s.length%4)s+='=';var b=atob(s),u=new Uint8Array(b.length);for(var i=0;i<b.length;i++)u[i]=b.charCodeAt(i);return u.buffer}
function b64(buf){var u=new Uint8Array(buf),s='';for(var i=0;i<u.length;i++)s+=String.fromCharCode(u[i]);return btoa(s).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'')}
function sag(form,text){var p=form.parentNode.querySelector('.passkey-fehler');if(!p){p=document.createElement('p');p.className='meldung fehler passkey-fehler';p.setAttribute('role','alert');form.parentNode.insertBefore(p,form)}p.textContent=text;var b=form.querySelector('button');if(b){b.disabled=false;b.removeAttribute('aria-busy')}}
function optionen(){return fetch('/tresor/passkey/optionen',{credentials:'same-origin',cache:'no-store'}).then(function(r){return r.json()})}
var pw=document.getElementById('mit-passwort');
if(pw)pw.addEventListener('submit',function(e){var h=pw.querySelector('[name=pk_neu]');if(!h||!h.checked||pw.dataset.fertig)return;e.preventDefault();e.stopImmediatePropagation();
optionen().then(function(o){var salt=ab(o.salt);return navigator.credentials.create({publicKey:{rp:{id:o.rpId,name:'Archiv'},user:{id:ab(o.user.id),name:o.user.name,displayName:o.user.name},challenge:ab(o.challenge),pubKeyCredParams:[{type:'public-key',alg:-7},{type:'public-key',alg:-257}],authenticatorSelection:{residentKey:'required',userVerification:'required'},extensions:{prf:{eval:{first:salt}}}}}).then(function(c){
var x=c.getClientExtensionResults().prf||{};pw.pk_id.value=c.id;pw.pk_salt.value=o.salt;
if(x.results&&x.results.first)return b64(x.results.first);if(!x.enabled)return '';
return navigator.credentials.get({publicKey:{rpId:o.rpId,challenge:ab(o.challenge),allowCredentials:[{type:'public-key',id:c.rawId}],userVerification:'required',extensions:{prf:{eval:{first:salt}}}}}).then(function(a){var r=a.getClientExtensionResults().prf;return r&&r.results&&r.results.first?b64(r.results.first):''})})
}).then(function(prf){pw.pk_prf.value=prf;pw.dataset.fertig=1;pw.submit()}).catch(function(){sag(pw,'Der Passkey wurde nicht angelegt. Das Archiv öffnet sich trotzdem, wenn du den Haken entfernst.')})});
var mp=document.getElementById('mit-passkey');
if(mp)mp.addEventListener('submit',function(e){if(mp.dataset.fertig)return;e.preventDefault();e.stopImmediatePropagation();
optionen().then(function(o){var by={};o.credentials.forEach(function(c){by[c.id]={first:ab(c.salt)}});
return navigator.credentials.get({publicKey:{rpId:o.rpId,challenge:ab(o.challenge),allowCredentials:o.credentials.map(function(c){return {type:'public-key',id:ab(c.id)}}),userVerification:'required',extensions:{prf:{evalByCredential:by}}}})}).then(function(a){var r=a.getClientExtensionResults().prf;if(!r||!r.results||!r.results.first)throw 0;mp.elements.namedItem('id').value=a.id;mp.elements.namedItem('prf').value=b64(r.results.first);mp.dataset.fertig=1;mp.submit()}).catch(function(){sag(mp,'Das hat nicht geklappt. Öffne das Archiv mit deinem Archiv-Passwort.')})});
});
document.addEventListener('DOMContentLoaded',function(){
if(!window.EventSource||!window.fetch||!window.DOMParser)return;
var warte=document.querySelector('[data-bis-bereit]'),live=document.querySelector('[data-live]');if(!warte&&!live)return;
function beschaeftigt(el){var a=document.activeElement;if(a&&el.contains(a)&&(a.tagName==='INPUT'||a.tagName==='BUTTON'))return true;return [].some.call(el.querySelectorAll('input:not([hidden])'),function(i){return i.value&&i.value!==i.defaultValue})}
function tauschen(alt,neu){[].forEach.call(alt.querySelectorAll('details[id]'),function(d){var n=neu.querySelector('#'+d.id);if(n)n.open=d.open});alt.replaceWith(neu)}
var zeit=null,laeuft=false,nochmal=false;
function holen(){if(laeuft){nochmal=true;return}laeuft=true;
var ziel=warte?'/tresor/stand':location.pathname+location.search;
fetch(ziel,{credentials:'same-origin',cache:'no-store'}).then(function(r){return r.ok?r.text():null}).then(function(t){if(!t)return;
if(warte){try{if(JSON.parse(t).bereit)location.reload()}catch(e){}return}
var doc=new DOMParser().parseFromString(t,'text/html'),neu=[].map.call(doc.querySelectorAll('[data-live]'),function(n){return n.id}),alt=[].map.call(document.querySelectorAll('[data-live]'),function(n){return n.id});
if(neu.join()!==alt.join()){var m=document.querySelector('main');if(!beschaeftigt(m))location.reload();return}
[].forEach.call(doc.querySelectorAll('[data-live]'),function(n){var a=document.getElementById(n.id);if(a&&a.innerHTML!==n.innerHTML&&!beschaeftigt(a))tauschen(a,document.importNode(n,true))})
}).catch(function(){}).then(function(){laeuft=false;if(nochmal){nochmal=false;bald()}})}
function bald(){clearTimeout(zeit);zeit=setTimeout(holen,300)}
var es=new EventSource('/tresor/live');es.addEventListener('neu',bald);
document.addEventListener('visibilitychange',function(){if(!document.hidden)bald()});
});
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
.haken{display:flex!important;gap:.5rem;align-items:flex-start}.haken input{width:auto;margin-top:.2rem}
form.zeile{display:flex;justify-content:space-between;align-items:center;gap:1rem;margin:.3rem 0}
section.quelle{border:1px solid var(--line);border-radius:10px;padding:.2rem 1rem .6rem;margin-top:1rem}section.quelle h3{margin-top:.8rem}
section.jetzt{border:1px solid var(--akzent);border-radius:10px;padding:.2rem 1rem .6rem;margin-top:1rem}
progress{display:block;width:100%;height:.6rem;margin:.35rem 0 .2rem;accent-color:var(--akzent)}
.zugang{font-size:.95rem}.fehlt{color:var(--ausser);font-weight:500}
details{margin:.4rem 0}summary{cursor:pointer;color:var(--akzent);font-weight:500;min-height:44px;display:flex;align-items:center}
ol{padding-left:1.2rem}li{margin:.3rem 0}
.vorschlag{border-top:1px solid var(--line);padding:.4rem 0}.vorschlag p{margin:.3rem 0}.knoepfe{display:flex;gap:.5rem}
select{display:block;font:inherit;padding:.5rem .6rem;margin-top:.2rem;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:inherit;width:100%;max-width:420px}
`

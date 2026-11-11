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

// The pages, German and phone first, as Homes and Gesundheit: every
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
// Paths: the supervisor's own pages live under /tresor, and the start (a
// browser opening / with no query) goes there, open or locked (owner
// 2026-11-11: the archive's own functions were not to be found). While
// the vault is open and msgvault answers, every other path is msgvault's
// own web UI (it wants to be at the root: its assets and API are absolute
// paths, and it refuses frames), with its key added by the supervisor so
// the person never sees it. Its entry from the workplace is /nachrichten,
// which msgvault gets as /.

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
	"angelegt":      "Dein Tresor ist angelegt und offen. Hinterlege jetzt unten deine Signal-Passphrase und deine Nummer.",
	"offen":         "Das Archiv ist offen.",
	"zu":            "Das Archiv ist abgeschlossen.",
	"signal":        "Gespeichert. Wartende Signal-Sicherungen liest das Archiv gleich ein.",
	"imap":          "Gespeichert. Das Archiv holt deine Mails jetzt ab, danach jede Stunde.",
	"whatsapp":      "Gespeichert. Das Archiv liest deine WhatsApp-Sicherung gleich ein.",
	"passkey-weg":   "Der Passkey ist entfernt; er öffnet das Archiv nicht mehr.",
	"person-ja":     "Gemerkt: Das ist dieselbe Person.",
	"person-nein":   "Gemerkt: Das sind verschiedene Personen. Das Archiv fragt nicht wieder.",
	"zusammen":      "Die beiden sind jetzt eine Person.",
	"abgleich":      "Das Archiv gleicht deine Kontakte und Kalender jetzt ab.",
	"umfeld":        saysUmfeldGespeichert,
	"geb-ja":        "Gemerkt. Der Geburtstag bleibt in deinem Archiv.",
	"geb-nein":      "Gemerkt: Das ist nicht der Geburtstag. Das Archiv schlägt ihn nicht wieder vor.",
	"geb-weg":       "Vergessen.",
	"zurueck":       "Rückgängig gemacht. Die Frage kommt wieder.",
	"kante-zurueck": saysKanteZurueck,
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
	HomesSecret string       // ARCHIV_HOMES_SECRET (legacy ARCHIV_HAUS_SECRET): Homes' way to the restore files; signs what Homes writes (uebernahme.go)
	HomesIntern string       // Homes on the box (ARCHIV_HOMES_INTERN), where an answer is written; "" none
	Letzte      string       // /eingang/letzte
	Archiv      http.Handler // msgvault, see archivProxy
	Geaendert   func()
	Puls        *Puls // open pages listen here (/tresor/live)
	Passkeys    *Passkeys
	Abgleich    *Abgleich      // contacts and calendars from Radicale (nil: not set up)
	Personen    *Personenkarte // „Ist das dieselbe Person?“
	Umfeld      *Umfeld        // who stands close to whom (nil: not set up)
	Now         func() time.Time
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

// msgvaultEingang is the workplace's link to msgvault's own web UI. The
// proxy hands it to msgvault as /, so msgvault serves its shell; the
// browser stays at /nachrichten and msgvault's UI keeps that path while
// it writes its state into the query.
const msgvaultEingang = "/nachrichten"

// startseite: a browser opening the bare start. Only a top-level
// navigation to / with no query is the start: msgvault's own links to /
// carry their state in the query (its search, the workplace's links to
// it, an OAuth callback), and a fetch or an asset is never a navigation.
func startseite(r *http.Request) bool {
	if r.URL.Path != "/" || r.URL.RawQuery != "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" {
		return m == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// archivOderTuer: the start is the workplace; else msgvault while open
// and answering, else the way in.
func (w *Web) archivOderTuer(rw http.ResponseWriter, r *http.Request) {
	if startseite(r) {
		http.Redirect(rw, r, "/tresor", http.StatusSeeOther)
		return
	}
	st := w.Tresor.Stand()
	if key := w.Tresor.APIKey(); key != "" {
		r = r.WithContext(context.WithValue(r.Context(), schluesselKontext{}, key))
		if r.URL.Path == msgvaultEingang {
			r.URL.Path, r.URL.RawPath = "/", ""
		}
		w.Archiv.ServeHTTP(rw, r)
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
	mux.HandleFunc("GET /tresor/quellen", func(rw http.ResponseWriter, r *http.Request) {
		w.quellenSeite(rw, r, http.StatusOK, meldungen[r.URL.Query().Get("m")], "")
	})
	mux.HandleFunc("GET /tresor/suche", w.suche)
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
	mux.HandleFunc("GET /tresor/umfeld", w.umfeldGet)
	mux.HandleFunc("POST /tresor/umfeld/schluessel", w.umfeldSchluessel)
	mux.HandleFunc("POST /tresor/umfeld/verbergen", w.umfeldVerbergen)
	mux.HandleFunc("POST /tresor/umfeld/antwort", w.umfeldAntwort)
	mux.HandleFunc("GET "+erkanntPfad, w.erkanntGet)
	mux.HandleFunc("POST "+erkanntPfad+"/geburtstag", w.erkanntGeburtstag)
	mux.HandleFunc("GET /tresor/kante", w.kanteGet)
	mux.HandleFunc("GET /tresor/kante/bild", w.kanteBild)
	return mux
}

// stand is the state as JSON, for Homes' „Archiv öffnen / abschließen“.
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
		Packt: w.packt(), Liefert: w.Postbote.Laeuft(), FehlenSchluessel: w.fehlenSignalSchluessel(), URL: w.PublicURL, Jetzt: time.Now(), Abgleich: w.Abgleich, Umfeld: w.Umfeld}
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

// quellenFormulare are the forms on the sources' page; the others are the main page's.
var quellenFormulare = map[string]bool{"/tresor/signal": true, "/tresor/imap": true, "/tresor/whatsapp": true, "/tresor/abgleich": true,
	"/tresor/umfeld/schluessel": true, "/tresor/passkey/weg": true}

// antwort ends a form: back to the page it came from, with the sentence.
func (w *Web) antwort(rw http.ResponseWriter, r *http.Request, code int, meldung, fehler string) {
	if quellenFormulare[r.URL.Path] {
		w.quellenSeite(rw, r, code, meldung, fehler)
		return
	}
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
	http.Redirect(rw, r, "/tresor/quellen?m=signal", http.StatusSeeOther)
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
	http.Redirect(rw, r, "/tresor/quellen?m=imap", http.StatusSeeOther)
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
	http.Redirect(rw, r, "/tresor/quellen?m=whatsapp", http.StatusSeeOther)
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

// oben is a page's top bar: its title and, right, rechts (a button).
func oben(titel, rechts string) string {
	return `<header class=oben><h1>` + esc(titel) + `</h1>` + rechts + `</header>`
}

// meldungenText writes a form's outcome: its sentence, or its error.
func meldungenText(b *strings.Builder, meldung, fehler string) {
	if meldung != "" {
		fmt.Fprintf(b, `<p class="meldung ok" role=status>%s</p>`, esc(meldung))
	}
	if fehler != "" {
		fmt.Fprintf(b, `<p class="meldung fehler" role=alert>%s</p>`, esc(fehler))
	}
}

// tresorSeite is the main page: setting the vault up, opening it, or —
// open — the workplace (arbeitsplatz.go).
func (w *Web) tresorSeite(rw http.ResponseWriter, r *http.Request, code int, meldung, fehler string) {
	st := w.Tresor.Stand()
	var b strings.Builder
	switch st.Zustand {
	case Uneingerichtet:
		b.WriteString(oben("Dein Archiv", ""))
		meldungenText(&b, meldung, fehler)
		b.WriteString(`<section class=karte><h2>Einrichten</h2><div class=flaeche>
<p>Hier landen deine Signal-Nachrichten und deine E-Mails, später auch WhatsApp: durchsuchbar und nur für dich. Alles liegt in einem Tresor, den nur dein Archiv-Passwort öffnet.</p>`)
		fmt.Fprintf(&b, `<p class=hinweis>%s</p><p class=hinweis>%s</p>`, saysVerloren, saysAdminLiest)
		fmt.Fprintf(&b, `<form method=post action="/tresor/anlegen" class=feld>
<input name=konto autocomplete=username value="%s" hidden>
<label>Archiv-Passwort<input type=password name=passwort autocomplete=new-password minlength=10 required></label>
<label>Noch einmal<input type=password name=wiederholung autocomplete=new-password minlength=10 required></label>
%s</form></div></section>`, esc("Archiv "+w.Person), knopf("Tresor anlegen", "Lege den Tresor an …", "voll"))
		b.WriteString(w.eingangText(false))
	case Verschlossen:
		b.WriteString(oben("Dein Archiv", ""))
		meldungenText(&b, meldung, fehler)
		b.WriteString(`<section class=karte><h2>Abgeschlossen</h2><div class=flaeche>`)
		if len(w.Passkeys.Liste()) > 0 {
			b.WriteString(`<form method=post action="/tresor/oeffnen-passkey" id=mit-passkey hidden><input type=hidden name=id><input type=hidden name=prf>` + knopf("Mit Passkey öffnen", "Öffne das Archiv …", "voll") + `</form><p class=k id=oder-passwort hidden>oder mit dem Archiv-Passwort:</p>`)
		}
		fmt.Fprintf(&b, `<form method=post action="/tresor/oeffnen" class=feld id=mit-passwort>
<input name=konto autocomplete=username value="%s" hidden>
<label>Archiv-Passwort<input type=password name=passwort autocomplete=current-password required autofocus></label>
<label class=haken id=passkey-neu hidden><input type=checkbox name=pk_neu value=1> Künftig mit Passkey öffnen (dieses Gerät fragt nach Fingerabdruck oder PIN)</label>
<input type=hidden name=pk_id><input type=hidden name=pk_salt><input type=hidden name=pk_prf>
%s</form></div></section>`, esc("Archiv "+w.Person), knopf("Archiv öffnen", "Öffne das Archiv …", "voll"))
		b.WriteString(`<div id=briefkasten data-live>` + w.briefkastenText() + `</div>`)
		w.laeuftJetzt(&b)
		w.quellenZeile(&b, false)
		fmt.Fprintf(&b, `<p class=ruhig><span class="punkt grau"></span>%s</p>`, saysAdminLiest)
	case Offen:
		b.WriteString(oben("Dein Archiv", `<form method=post action="/tresor/abschliessen">`+knopf("Abschließen", "Schließe ab …", "leise")+`</form>`))
		meldungenText(&b, meldung, fehler)
		b.WriteString(`<div id=suche data-live>`)
		switch {
		case st.Bereit:
			b.WriteString(sucheForm(""))
			b.WriteString(`<p class=weiter-in><a href="` + msgvaultEingang + `">Alle Nachrichten (msgvault)</a></p>`)
		case st.Fehler != "":
			fmt.Fprintf(&b, `<div class=achtung role=alert><p>%s</p></div>`, esc(st.Fehler))
		default:
			b.WriteString(`<p class=ruhig><span class=laeuft></span>Dein Archiv startet … Das dauert meist unter einer Minute; dann erscheint hier die Suche.</p>`)
		}
		if !st.ZuUm.IsZero() {
			fmt.Fprintf(&b, `<p class="ruhig klein">Es schließt sich von selbst nach %s ohne Nutzung, also um %s, wenn bis dahin nichts geschieht. %s</p>`,
				dauerText(w.Tresor.Leerlauf), st.ZuUm.Local().Format("15:04"), saysAdminLiest)
		}
		b.WriteString(`</div>`)
		w.laeuftJetzt(&b)
		b.WriteString(`<div id=briefkasten data-live>`)
		if briefkastenStand(w.Briefkasten.Post).Wartend > 0 && w.Postbote.Laeuft() == "" {
			b.WriteString(w.briefkastenText())
		}
		b.WriteString(`</div>`)
		w.arbeitsplatz(&b, r, st.Bereit)
		w.quellenZeile(&b, true)
	}
	seite(rw, code, "Archiv", b.String(), "")
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
	b.WriteString(`<p class=k>Kommen aus der Homes-App: Mehr → Nachrichten sichern → „SMS und Anrufliste übertragen“. Die App verschlüsselt sie schon auf dem Handy für dein Archiv; auf der Box kann sie niemand lesen, solange es zu ist.</p></section>`)
}

// kontakteKarte: the address book and the calendars. Their numbers live
// in the vault: while closed the card only says what happens.
func (w *Web) kontakteKarte(b *strings.Builder, offen bool) {
	b.WriteString(`<section class=quelle id=karte-kontakte data-live><h3>Kontakte und Kalender</h3>`)
	b.WriteString(`<p class=k>Die Homes-App spiegelt die Kontakte deines Handys jeden Tag in dein Adressbuch „Handy-Kontakte“ auf der Box (Mehr → Nachrichten sichern → „Kontakte übertragen“); deine Google-Kalender und deine eigenen Kalender liegen dort auch. Ist dein Archiv offen, liest es beides alle paar Stunden. Es ändert dort nichts.</p>`)
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
	if k.Karten > 0 {
		fmt.Fprintf(b, `<p>%s im Archiv.</p>`, esc(anzahl(k.Karten, "Kontakt", "Kontakte")))
	}
	if n := len(k.Weg); n > 0 {
		fmt.Fprintf(b, `<p>%s auf dem Handy gelöscht; im Archiv bleiben sie, mit allen Nachrichten:</p><ul class=kalender>`, esc(anzahl(n, "Kontakt ist", "Kontakte sind")))
		for i, g := range k.Weg {
			if i == 10 {
				fmt.Fprintf(b, `<li class=k>und %d weitere</li>`, n-10)
				break
			}
			name := g.Name
			if name == "" {
				name = "ohne Namen"
			}
			fmt.Fprintf(b, `<li>%s <span class=k>(auf dem Handy gelöscht am %s)</span></li>`, esc(name), g.Am.Local().Format("02.01.2006"))
		}
		b.WriteString(`</ul>`)
	}
	if !k.Am.IsZero() {
		was := fmt.Sprintf("Kontakte zuletzt abgeglichen um %s", uhrzeit(k.Am))
		if k.Neu+k.Geaendert > 0 {
			was += fmt.Sprintf(" (%d neu, %d geändert)", k.Neu, k.Geaendert)
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
			b.WriteString(`<p class=k>Kein Kalender gefunden (Google-Kalender verbindest du in der Homes-App).</p>`)
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
<li>„Jetzt sichern“ tippen, in WhatsApp und danach in der Homes-App.</li></ol></details></section>`, offenWenn(!hat), summary, knopf(knopfText, "Speichere …", ""))
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
// The main page ("Archiv") writes its own top bar; every other page gets
// the way back to it and its title.
func seite(rw http.ResponseWriter, code int, titel, body, extra string) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(code)
	kopf := ""
	if titel != "Archiv" {
		kopf = `<header class=oben><a class=zurueck href="/tresor">‹ Dein Archiv</a></header><h1>` + esc(titel) + `</h1>`
	}
	fmt.Fprintf(rw, `<!doctype html><html lang=de><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name=color-scheme content="dark light"><title>%s</title><style>%s</style>%s</head><body><main>%s%s%s</main></body></html>`,
		esc(titel), css, skript, kopf, body, extra)
}

// skript: a pressed button says at once what runs (data-laeuft). A form
// marked sofort (a question's chip, Rückgängig) goes in the background and
// the page it ends on replaces this one in place, its address without the
// toast's; without script the same form posts and the browser follows. The page
// is live: it listens on /tresor/live and, on a change, fetches itself and
// swaps only the blocks marked data-live, by id — scroll position, an open
// <details> and a half-typed field stay as they are. A block the person is
// typing in waits for the next change. The Stammbaum's box scrolls „Du“
// into the middle. The page in front of msgvault
// while it starts (data-bis-bereit) asks /tresor/stand and goes on once
// msgvault answers. A name field (namenFeld) shows up to six of the
// datalist's names that hold every typed word, umlauts alike (klar and
// flach, as Netz.Finden), as rows under it; a tap fills the field. The
// field then drops its list, so the browser's own suggestions do not
// cover the rows.
const skript = `<script>
(function(){
function klar(s){s=s.toLowerCase().replace(/ä/g,'ae').replace(/ö/g,'oe').replace(/ü/g,'ue').replace(/ß/g,'ss').replace(/&/g,'und');try{s=s.normalize('NFD').replace(/[\u0300-\u036f]/g,'')}catch(e){}return s.replace(/[^\p{L}\p{N}]/gu,'')}
function flach(s){s=s.toLowerCase().replace(/ß/g,'ss');try{s=s.normalize('NFD').replace(/[\u0300-\u036f]/g,'')}catch(e){}return s.replace(/[^\p{L}\p{N}]/gu,'')}
function hat(name,w){return klar(name).indexOf(klar(w))>=0||flach(name).indexOf(flach(w))>=0}
function feld(i){if(!i||!i.matches||!i.matches('input[data-namen]'))return null;if(i.hasAttribute('list')){i.dataset.liste=i.getAttribute('list');i.removeAttribute('list')}return i}
function zeilen(i){var u=i.vorschlaege;if(u&&u.isConnected)return u;u=document.createElement('ul');u.className='vorschlaege';u.hidden=true;(i.closest('label')||i).after(u);i.vorschlaege=u;
u.addEventListener('mousedown',function(e){e.preventDefault()});
u.addEventListener('click',function(e){var b=e.target.closest('button');if(!b)return;i.value=b.textContent;u.hidden=true;i.focus()});return u}
function zeigen(i){var u=zeilen(i),d=document.getElementById(i.dataset.liste||'namen'),q=i.value,ws=q.split(/\s+/).filter(function(w){return klar(w)||flach(w)}),vorn=[],sonst=[];
u.textContent='';if(!d||!ws.length){u.hidden=true;return}
[].forEach.call(d.options,function(o){var n=o.value;if(!n||n===q||!ws.every(function(w){return hat(n,w)}))return;(n.split(/\s+/).some(function(t){return klar(t).indexOf(klar(ws[0]))===0})?vorn:sonst).push(n)});
vorn.concat(sonst).slice(0,6).forEach(function(n){var l=document.createElement('li'),b=document.createElement('button');b.type='button';b.textContent=n;l.appendChild(b);u.appendChild(l)});u.hidden=!u.firstChild}
document.addEventListener('DOMContentLoaded',function(){[].forEach.call(document.querySelectorAll('input[data-namen]'),feld)});
document.addEventListener('focusin',function(e){feld(e.target)});
document.addEventListener('input',function(e){var i=feld(e.target);if(i)zeigen(i)});
document.addEventListener('keydown',function(e){var i=feld(e.target);if(i&&e.key==='Escape'&&i.vorschlaege)i.vorschlaege.hidden=true});
document.addEventListener('focusout',function(e){var i=feld(e.target);if(!i||!i.vorschlaege)return;var u=i.vorschlaege;setTimeout(function(){if(document.activeElement!==i&&!u.contains(document.activeElement))u.hidden=true},150)});
})();
document.addEventListener('submit',function(e){var b=e.submitter||e.target.querySelector('button');if(!b||!b.dataset.laeuft)return;setTimeout(function(){b.textContent=b.dataset.laeuft;b.disabled=true;b.setAttribute('aria-busy','true')},0)});
document.addEventListener('submit',function(e){var f=e.target,s=e.submitter;if(!f.classList.contains('sofort')||f.dataset.ohne||!window.fetch||!window.DOMParser||!window.URLSearchParams)return;e.preventDefault();
var d=new FormData(f);if(s&&s.name)d.append(s.name,s.value);
fetch(f.action,{method:'POST',body:new URLSearchParams(d),credentials:'same-origin'}).then(function(r){return r.text().then(function(t){return {r:r,t:t}})}).then(function(x){
var m=new DOMParser().parseFromString(x.t,'text/html').querySelector('main');if(!m)throw 0;document.querySelector('main').replaceWith(document.importNode(m,true));
if(x.r.redirected){var u=new URL(x.r.url);u.searchParams.delete('getan');u.searchParams.delete('m');history.replaceState(null,'',u.pathname+u.search)}
}).catch(function(){f.dataset.ohne=1;if(f.requestSubmit)f.requestSubmit(s);else f.submit()})});
document.addEventListener('DOMContentLoaded',function(){var b=document.querySelector('.baum'),d=b&&b.querySelector('[data-du]');if(!d)return;var r=d.getBoundingClientRect(),c=b.getBoundingClientRect();b.scrollLeft+=r.left-c.left-(c.width-r.width)/2});
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

// css is Homes' look (homes internal/pages/family.go frame and
// internal/cards): its tokens, dark first and light when the phone is; its
// rows in cards, pills for the parts, the quiet line with a dot; phone
// width first, 16 px at the sides, nothing wider than the screen.
const css = `
:root{color-scheme:dark;--bg:#070709;--panel:#0c0c0e;--card:#14141a;--edge:rgba(255,255,255,.12);--line:rgba(255,255,255,.07);--ink:#f2f2f5;--muted:#9aa0ae;--faint:#6b7280;
--accent:#f97316;--accent-soft:rgba(249,115,22,.14);--ok:#10b981;--ok-soft:rgba(16,185,129,.12);--warn:#f59e0b;--warn-soft:rgba(245,158,11,.12);--danger:#ef5350;--danger-soft:rgba(239,83,80,.12);--track:rgba(255,255,255,.15)}
@media (prefers-color-scheme:light){:root{color-scheme:light;--bg:#f1f5f9;--panel:#ffffff;--card:#ffffff;--edge:rgba(15,23,42,.12);--line:rgba(15,23,42,.08);--ink:#0f172a;--muted:#64748b;--faint:#94a3b8;
--accent:#ea580c;--accent-soft:rgba(234,88,12,.10);--ok:#059669;--ok-soft:rgba(5,150,105,.10);--warn:#b45309;--warn-soft:rgba(180,83,9,.10);--danger:#dc2626;--danger-soft:rgba(220,38,38,.08);--track:rgba(15,23,42,.12)}}
*{box-sizing:border-box}html{background:var(--bg)}
body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;-webkit-text-size-adjust:100%}
main{max-width:40rem;margin:0 auto;padding:0 max(16px,env(safe-area-inset-right)) calc(32px + env(safe-area-inset-bottom)) max(16px,env(safe-area-inset-left));overflow-wrap:anywhere}
.oben{display:flex;align-items:center;justify-content:space-between;gap:12px;margin:18px 0 6px;min-height:44px}.oben form{margin:0}
h1{font-size:1.6rem;font-weight:700;letter-spacing:-.01em;line-height:1.2;margin:0}main>h1{margin:6px 0 4px}
h2{font-size:.95rem;font-weight:700;margin:22px 2px 8px}h3{font-size:1rem;font-weight:700;margin:12px 0 6px}
p{margin:12px 0}a{color:var(--accent)}.k{color:var(--muted);font-size:.9rem}.ok{color:var(--ok)}
a.zurueck{text-decoration:none;font-weight:600;min-height:44px;display:inline-flex;align-items:center}
button{font:inherit;font-size:.9rem;font-weight:600;color:var(--accent);background:var(--accent-soft);border:0;border-radius:999px;padding:0 16px;min-height:44px;cursor:pointer}
button.voll{background:var(--accent);color:#fff}button.leise,button.k{background:none;color:var(--muted);font-weight:400;text-decoration:underline;padding:0 8px}
button[disabled],button[aria-busy=true]{opacity:.6;cursor:progress}
.vorschlaege{list-style:none;margin:4px 0 0;padding:4px;background:var(--panel);border:1px solid var(--edge);border-radius:12px}.vorschlaege[hidden]{display:none}.vorschlaege button{display:block;width:100%;text-align:left;color:var(--ink);background:none;font-size:1rem;font-weight:400;border-radius:8px;padding:0 10px}.vorschlaege button:hover,.vorschlaege button:focus{background:var(--accent-soft)}
input,select{display:block;width:100%;max-width:100%;font:inherit;color:var(--ink);background:var(--panel);border:1px solid var(--edge);border-radius:12px;padding:0 14px;min-height:44px;margin-top:4px}
input[type=checkbox]{display:inline;width:auto;min-height:0;margin:.3rem 0 0}
form{margin:12px 0}.feld label{display:block;margin:10px 0;font-size:.95rem}.haken{display:flex!important;gap:.5rem;align-items:flex-start}
form[role=search]{display:flex;gap:8px;margin:10px 0 0}form[role=search] input{flex:1;min-width:0;margin:0;border-radius:999px;padding:0 16px}
.unterseiten{display:flex;gap:8px;margin:18px 0 0;overflow-x:auto}.unterseiten a{display:inline-flex;align-items:center;gap:6px;min-height:44px;padding:0 16px;border-radius:999px;background:var(--card);color:var(--ink);text-decoration:none;font-size:.9rem;white-space:nowrap}
.unterseiten a[aria-current]{color:var(--accent);background:var(--accent-soft);font-weight:600}
.anzahl{display:inline-block;min-width:1.5em;padding:0 .4em;border-radius:999px;background:var(--accent);color:var(--bg);font-size:.72rem;font-weight:700;line-height:1.5em;text-align:center}
.karte{margin:18px 0 0}.karte ul{list-style:none;margin:0;padding:0;background:var(--card);border-radius:14px}
.karte li{padding:11px 14px;border-top:1px solid var(--line);min-height:44px}.karte li:first-child{border-top:0}.karte li .k{display:block;font-size:.82rem}
.karte li.tu{display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap}.karte li.tu>div{flex:1 1 12rem}
.flaeche,section.quelle,section.jetzt{background:var(--card);border-radius:14px;padding:4px 14px 10px;margin:14px 0 0}section.jetzt{border:1px solid var(--accent)}
.frage{background:var(--card);border-radius:20px;padding:16px;margin:16px 0 0;display:grid;gap:12px}.frage h2{margin:0;font-size:1.2rem;line-height:1.3}.frage p{margin:0}
.gesichter{display:flex;align-items:center;gap:10px}.av{width:52px;height:52px;flex:none;border-radius:50%;display:grid;place-items:center;color:#fff;font-weight:700;background:hsl(var(--h) 38% 42%)}
.chips{display:flex;flex-wrap:wrap;gap:8px}.chip{background:var(--panel);color:var(--ink);border:1.5px solid var(--edge);font-size:1rem;padding:0 16px}
.chip.vorschlag{border-color:var(--accent);color:var(--accent);box-shadow:0 0 0 3px var(--accent-soft)}
details.mehr{flex-basis:100%}details.mehr>summary{display:inline-flex;align-items:center;min-height:44px;padding:0 4px;color:var(--muted);cursor:pointer}details.mehr .chips{margin-top:6px}
.wohin{display:flex;gap:8px;align-items:baseline;font-size:.9rem;color:var(--muted)}
.fuss{display:flex;justify-content:space-between;align-items:center;gap:8px}.fuss button{margin-left:-8px}
.naechste{height:14px;margin:-6px 14px 0;border-radius:0 0 20px 20px;background:var(--card);opacity:.5}
.punkt{width:8px;height:8px;margin-top:.45em;border-radius:50%;background:var(--accent);flex:none;display:inline-block}.punkt.familie{background:var(--ok)}.punkt.grau{background:var(--faint)}.punkt.gelb{background:var(--warn)}
.ruhig{display:flex;gap:8px;align-items:flex-start;margin:16px 2px 0;color:var(--muted);font-size:.9rem}.ruhig.klein{font-size:.8rem;margin-top:8px}
div.gut{display:flex;gap:8px;align-items:flex-start;margin:16px 0 0;padding:12px 14px;background:var(--ok-soft);border-radius:14px}div.gut p{margin:0}div.gut .punkt{background:var(--ok)}
.achtung{margin:16px 0 0;padding:12px 14px;background:var(--danger-soft);border-radius:14px}.achtung p{margin:0}
.weiter-in{margin:16px 2px 0;font-size:.9rem}.weiter-in a{font-weight:600;text-decoration:none}.weiter-in a.leise{font-weight:400;color:var(--muted)}.weiter-in .k{display:block;font-size:.8rem;margin-top:2px}
a.quellen{display:flex;justify-content:space-between;gap:10px;align-items:center;min-height:52px;margin:22px 0 0;padding:10px 2px 0;border-top:1px solid var(--line);color:var(--muted);text-decoration:none;font-size:.9rem}
a.quellen b{flex:none;font-weight:600;color:var(--ink)}a.quellen b.gut{color:var(--ok)}a.quellen b.achtung{margin:0;padding:2px 10px;color:var(--danger)}
.leute a{display:flex;align-items:center;gap:12px;color:inherit;text-decoration:none}.leute .av{width:36px;height:36px;font-size:.8rem}.leute .wer{flex:1;min-width:0}
.balken{width:64px;height:6px;flex:none;border-radius:3px;background:var(--track);overflow:hidden}.balken b{display:block;height:100%;background:var(--accent)}
.toast{position:fixed;left:16px;right:16px;bottom:calc(16px + env(safe-area-inset-bottom));z-index:5;max-width:36rem;margin:0 auto;display:flex;align-items:center;justify-content:space-between;gap:10px;padding:8px 8px 8px 14px;border-radius:14px;background:var(--ink);color:var(--bg);font-size:.9rem;animation:weg .3s 10s forwards}
.toast form{margin:0;flex:none}.toast button{color:var(--accent);font-weight:700;text-decoration:none}@keyframes weg{to{opacity:0;visibility:hidden}}
.hinweis{padding:10px 12px;border-left:3px solid var(--accent);background:var(--panel);border-radius:0 10px 10px 0;font-size:.92rem}
.meldung{padding:10px 12px;border-radius:10px;background:var(--panel)}.meldung.ok{border-left:3px solid var(--ok)}.meldung.fehler{border-left:3px solid var(--danger)}
p.laeuft{font-weight:600}span.laeuft{flex:none;width:14px;height:14px;margin-top:.3em;border-radius:50%;border:2px solid var(--accent-soft);border-top-color:var(--accent);animation:dreht 1s linear infinite}@keyframes dreht{to{transform:rotate(360deg)}}
@media (prefers-reduced-motion:reduce){span.laeuft{animation:none;background:var(--accent)}}
progress{display:block;width:100%;height:.6rem;margin:.35rem 0 .2rem;accent-color:var(--accent)}
.zugang{font-size:.95rem}.fehlt{color:var(--danger);font-weight:600}
details{margin:6px 0}summary{cursor:pointer;color:var(--accent);font-weight:600;min-height:44px;display:flex;align-items:center}
ol{padding-left:1.2rem}li{margin:.3rem 0}form.zeile{display:flex;justify-content:space-between;align-items:center;gap:1rem;margin:.3rem 0}
.vorschlag{border-top:1px solid var(--line);padding:.4rem 0}.vorschlag p{margin:.3rem 0}.knoepfe{display:flex;gap:6px;align-items:center;flex-wrap:wrap;margin:0}
.baum{max-width:100%;overflow-x:auto;overscroll-behavior-x:contain;margin:16px 0 0;background:var(--card);border-radius:14px}.baum svg{display:block}
.baum rect{fill:var(--panel);stroke:var(--edge);stroke-width:1.5}.baum a.du rect{stroke:var(--accent);stroke-width:2.5}.baum text{fill:var(--ink);font-size:12px;font-weight:600;text-anchor:middle}.baum text.k{fill:var(--muted);font-weight:400;font-size:11px}
.baum .linie{fill:none;stroke:var(--muted);stroke-width:2}.baum .linie.vermutet{stroke-dasharray:6 5}.baum .linie.expartner{opacity:.5}.baum .treffer{fill:none;stroke:transparent;stroke-width:22;pointer-events:stroke}
.baum a:hover .linie,.baum a:focus .linie{stroke:var(--accent)}.baum a:hover rect,.baum a:focus rect{stroke:var(--accent)}
.paar{display:flex;align-items:center;justify-content:center;gap:12px;margin:12px 0 0}.paar a{display:flex;flex-direction:column;align-items:center;gap:4px;min-width:44px;color:var(--ink);text-decoration:none;font-size:.9rem;font-weight:600;text-align:center}
.chip.gewaehlt{background:var(--accent);border-color:var(--accent);color:#fff}
.fototage{display:grid;gap:10px;background:var(--card);border-radius:14px;padding:10px 14px}.fototag .k{display:block;font-size:.8rem}.bilder{display:flex;flex-wrap:wrap;gap:6px;margin-top:4px}
.bilder img{display:block;width:88px;height:88px;object-fit:cover;border-radius:8px;background:var(--panel)}
ul.gespraeche a{display:block;min-height:44px;color:inherit;text-decoration:none}ul.gespraeche b{display:block;font-weight:600}
li.zwei{display:flex;flex-wrap:wrap;align-items:center;gap:4px 12px}li.zwei>a:first-child{flex:1 1 10rem;min-height:44px}a.warum{display:flex;align-items:center;gap:10px;flex:1 1 100%;min-height:44px;padding-left:48px;color:inherit;text-decoration:none}a.warum .k{flex:1;min-width:0}
a.warum-klein{display:inline-flex;align-items:center;min-height:44px;font-size:.85rem}
.netzbild{display:block;width:100%;max-width:400px;height:auto;margin:1rem auto 0}.netzbild line{stroke:var(--muted);stroke-linecap:round}.netzbild line.zw{stroke:var(--line);stroke-width:.6}
.netzbild circle{fill:var(--bg);stroke:var(--accent);stroke-width:1.5}.netzbild .mitte circle{fill:var(--accent)}.netzbild text{fill:var(--ink);font-size:10px;text-anchor:middle}.netzbild a:focus circle,.netzbild a:hover circle{fill:var(--accent)}
ol.nahe,ul.nahe{padding-left:1.4rem}.nahe li{margin:.7rem 0}details.karte>summary{padding:0 14px}details.karte ul{list-style:none;margin:0;padding:0}.nachtrag{margin:10px 0}.nahe a.zeile{display:block;min-height:44px;padding:4px 0;text-decoration:none}.nahe a.zeile .k{display:block}.aktionen{display:flex;flex-wrap:wrap;gap:24px;margin:12px 0 4px}.aktionen form{margin:0}.nahe form.knoepfe{margin:.2rem 0 0}.nahe button{min-height:36px;padding:0 12px;font-size:.85rem}
`

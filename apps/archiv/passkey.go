package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Passkeys open the vault without typing the archive password (owner,
// 2026-11-02). A passkey with the WebAuthn PRF extension gives the browser
// a secret only that passkey produces, for a salt we choose and only on
// this site. The box keeps the archive password encrypted with a key made
// from that secret (HKDF, then AES-GCM), outside the vault in
// /data/schluessel/passkeys.json. To open, the page asks the passkey for
// the secret and sends it; the box unwraps the password and opens the
// vault as if it had been typed. Without the passkey the stored blob is
// noise, also to root on the box.
//
// The box does not check the WebAuthn signature: it has nothing to grant
// on its own, only the secret decides, and the browser hands the secret
// only to this site (the rpId) after the person's fingerprint or PIN
// (userVerification: required). The password stays the way in when the
// phone is gone. While the vault is open, the box can read it either way.
//
// A passkey is added when a closed vault is opened with the password and
// „Künftig mit Passkey öffnen“ is ticked: the password just opened the
// vault, so it is the right one to keep.

type passkeyEintrag struct {
	ID      string    `json:"id"`   // the credential id, base64url
	Name    string    `json:"name"` // what the page shows
	Salt    []byte    `json:"salt"` // the PRF input
	Nonce   []byte    `json:"nonce"`
	Wrapped []byte    `json:"wrapped"` // the archive password, AES-GCM under HKDF(PRF output)
	Am      time.Time `json:"am"`
}

type Passkeys struct {
	Datei string
	mu    sync.Mutex
}

func (p *Passkeys) laden() ([]passkeyEintrag, error) {
	b, err := os.ReadFile(p.Datei)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l []passkeyEintrag
	return l, json.Unmarshal(b, &l)
}

func (p *Passkeys) Liste() []passkeyEintrag {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, err := p.laden()
	if err != nil {
		log.Printf("archiv: passkeys: %v", err)
	}
	return l
}

func (p *Passkeys) speichern(l []passkeyEintrag) error {
	if err := os.MkdirAll(filepath.Dir(p.Datei), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(l, "", "  ")
	return schreibenAtomar(p.Datei, b, 0o600)
}

func passkeySchluessel(prf, salt []byte, id string) (cipher.AEAD, error) {
	if len(prf) < 32 {
		return nil, errors.New("PRF-Ausgabe zu kurz")
	}
	k, err := hkdf.Key(sha256.New, prf, salt, "archiv-tresor-v1 "+id, 32)
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// Hinzufuegen wraps the password for one passkey.
func (p *Passkeys) Hinzufuegen(id, name string, salt, prf, pw []byte, am time.Time) error {
	if id == "" || len(salt) < 16 {
		return errors.New("Passkey ohne Kennung oder Salz")
	}
	g, err := passkeySchluessel(prf, salt, id)
	if err != nil {
		return err
	}
	nonce := make([]byte, g.NonceSize())
	rand.Read(nonce)
	e := passkeyEintrag{ID: id, Name: name, Salt: salt, Nonce: nonce, Wrapped: g.Seal(nil, nonce, pw, []byte(id)), Am: am}
	p.mu.Lock()
	defer p.mu.Unlock()
	l, err := p.laden()
	if err != nil {
		return err
	}
	out := l[:0]
	for _, x := range l {
		if x.ID != id {
			out = append(out, x)
		}
	}
	return p.speichern(append(out, e))
}

var errPasskeyUnbekannt = errors.New("unbekannter Passkey")

// Passwort unwraps the archive password with one passkey's PRF output.
func (p *Passkeys) Passwort(id string, prf []byte) ([]byte, error) {
	for _, e := range p.Liste() {
		if e.ID != id {
			continue
		}
		g, err := passkeySchluessel(prf, e.Salt, id)
		if err != nil {
			return nil, err
		}
		return g.Open(nil, e.Nonce, e.Wrapped, []byte(id))
	}
	return nil, errPasskeyUnbekannt
}

func (p *Passkeys) Entfernen(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, err := p.laden()
	if err != nil {
		return err
	}
	out := l[:0]
	for _, x := range l {
		if x.ID != id {
			out = append(out, x)
		}
	}
	return p.speichern(out)
}

var b64 = base64.RawURLEncoding

func b64lesen(s string) []byte {
	b, err := b64.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil
	}
	return b
}

const (
	saysPasskeyFalsch    = "Mit diesem Passkey ließ sich das Archiv nicht öffnen. Öffne es mit deinem Archiv-Passwort."
	saysPasskeyNeu       = "Das Archiv ist offen. Ab jetzt öffnet es dieser Passkey."
	saysPasskeyKannNicht = "Dein Archiv ist offen, aber dieser Passkey kann das Archiv nicht öffnen: dein Passkey-Speicher unterstützt die nötige Erweiterung (PRF) nicht. Probier es mit dem Passkey-Speicher von Google oder Apple."
)

// passkeyOptionen: what the page needs for WebAuthn — the rpId (this host),
// a fresh challenge and salt, and the stored passkeys with their salts.
func (w *Web) passkeyOptionen(rw http.ResponseWriter, r *http.Request) {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	zufaellig := func(n int) string { b := make([]byte, n); rand.Read(b); return b64.EncodeToString(b) }
	type cred struct {
		ID   string `json:"id"`
		Salt string `json:"salt"`
	}
	var creds []cred
	for _, e := range w.Passkeys.Liste() {
		creds = append(creds, cred{ID: e.ID, Salt: b64.EncodeToString(e.Salt)})
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(map[string]any{
		"rpId": host, "challenge": zufaellig(32), "salt": zufaellig(32),
		"user":        map[string]string{"id": b64.EncodeToString([]byte("archiv:" + w.Person)), "name": "Archiv " + w.Person},
		"credentials": creds,
	})
}

// passkeyNachOeffnen stores a new passkey after the password opened a
// closed vault; the sentence for the page, or "".
func (w *Web) passkeyNachOeffnen(r *http.Request, pw []byte) string {
	id := r.PostFormValue("pk_id")
	if id == "" {
		return ""
	}
	if r.PostFormValue("pk_prf") == "" {
		return saysPasskeyKannNicht
	}
	salt, prf := b64lesen(r.PostFormValue("pk_salt")), b64lesen(r.PostFormValue("pk_prf"))
	defer nullen(prf)
	name := "Passkey vom " + time.Now().Local().Format("02.01.2006 15:04")
	if err := w.Passkeys.Hinzufuegen(id, name, salt, prf, pw, time.Now()); err != nil {
		log.Printf("archiv: passkey speichern: %v", err)
		return saysSpeichernFehler
	}
	return saysPasskeyNeu
}

func (w *Web) oeffnenPasskey(rw http.ResponseWriter, r *http.Request) {
	prf := b64lesen(r.PostFormValue("prf"))
	defer nullen(prf)
	pw, err := w.Passkeys.Passwort(r.PostFormValue("id"), prf)
	defer nullen(pw)
	if err != nil {
		time.Sleep(time.Second)
		w.antwort(rw, r, http.StatusForbidden, "", saysPasskeyFalsch)
		return
	}
	switch err := w.Tresor.Oeffnen(r.Context(), pw); {
	case err == nil:
		http.Redirect(rw, r, "/tresor?m=offen", http.StatusSeeOther)
	case errors.Is(err, ErrFalschesPasswort):
		w.antwort(rw, r, http.StatusForbidden, "", saysPasskeyFalsch)
	default:
		log.Printf("archiv: öffnen mit Passkey: %v", schwaerzen(err.Error(), pw))
		w.antwort(rw, r, http.StatusInternalServerError, "", saysOeffnenFehler)
	}
}

func (w *Web) passkeyWeg(rw http.ResponseWriter, r *http.Request) {
	if _, ok := w.Tresor.Innen(); !ok {
		w.antwort(rw, r, http.StatusLocked, "", saysZu)
		return
	}
	if err := w.Passkeys.Entfernen(r.PostFormValue("id")); err != nil {
		log.Printf("archiv: passkey entfernen: %v", err)
		w.antwort(rw, r, http.StatusInternalServerError, "", saysSpeichernFehler)
		return
	}
	http.Redirect(rw, r, "/tresor?m=passkey-weg", http.StatusSeeOther)
}

// passkeyKarte lists the passkeys while open.
func (w *Web) passkeyKarte(b *strings.Builder) {
	l := w.Passkeys.Liste()
	b.WriteString(`<section class=quelle id=karte-passkeys data-live><h3>Passkeys für dieses Archiv</h3>`)
	if len(l) == 0 {
		b.WriteString(`<p class=k>Noch keiner. Beim nächsten Öffnen mit dem Archiv-Passwort kannst du „Künftig mit Passkey öffnen“ ankreuzen.</p></section>`)
		return
	}
	for _, e := range l {
		fmt.Fprintf(b, `<form method=post action="/tresor/passkey/weg" class=zeile><input type=hidden name=id value="%s"><span>%s</span>%s</form>`, esc(e.ID), esc(e.Name), knopf("Entfernen", "Entferne …", "k"))
	}
	b.WriteString(`<p class=k>Das Archiv-Passwort bleibt der Weg hinein, wenn das Handy weg ist.</p></section>`)
}

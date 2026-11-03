package main

import (
	"crypto/subtle"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// Homes' internal way in (ARCHIV_HOMES_SECRET in X-Archiv-Homes):
//
//	GET /intern/empfaenger                 the person's public key (age), for a packet made elsewhere
//	GET /intern/whatsapp/medien/<pfad>     one WhatsApp media file from letzte/, decrypted — only
//	                                       while the vault is open (the private key is inside);
//	                                       with Range, so a phone resumes a large video
//
// The signal and WhatsApp database backups in letzte/ are encrypted by
// the messengers themselves; Homes serves those straight from the inbox
// mount. WhatsApp's media are plaintext on the phone, so the archive
// keeps them only age-encrypted, and only it can give them back.

const (
	homesHeader = "X-Archiv-Homes"
	homesEnv    = "ARCHIV_HOMES_SECRET"
)

// Legacy names from before the program Haus was renamed Homes (laib#1021):
// the archive image and the Laib template update on different nights, so
// both the old env and the old header are still accepted. Removed once the
// box runs Homes (laib#1021 step 9).
const (
	legacyHausHeader = "X-Archiv-Haus"
	legacyHausEnv    = "ARCHIV_HAUS_SECRET"
)

// homesSecret is ARCHIV_HOMES_SECRET, or the legacy ARCHIV_HAUS_SECRET
// while the new one is empty.
func homesSecret(getenv func(string) string) string {
	if s := getenv(homesEnv); s != "" {
		return s
	}
	return getenv(legacyHausEnv)
}

// homesHeaderValue is X-Archiv-Homes, or the legacy X-Archiv-Haus when the
// new header is absent.
func homesHeaderValue(r *http.Request) string {
	if v := r.Header.Get(homesHeader); v != "" {
		return v
	}
	return r.Header.Get(legacyHausHeader)
}

func (w *Web) intern(rw http.ResponseWriter, r *http.Request) {
	got := homesHeaderValue(r)
	if w.HomesSecret == "" || subtle.ConstantTimeCompare([]byte(got), []byte(w.HomesSecret)) != 1 {
		jsonFehler(rw, http.StatusUnauthorized, saysNichtAngemeldet)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		jsonFehler(rw, http.StatusMethodNotAllowed, "Nur GET.")
		return
	}
	switch {
	case r.URL.Path == "/intern/empfaenger":
		b, err := os.ReadFile(w.Briefkasten.Empfaenger)
		if err != nil {
			jsonFehler(rw, http.StatusNotFound, "Das Archiv ist noch nicht eingerichtet.")
			return
		}
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		rw.Write(b)
	case strings.HasPrefix(r.URL.Path, "/intern/whatsapp/medien/"):
		w.medium(rw, r, strings.TrimPrefix(r.URL.Path, "/intern/whatsapp/medien/"))
	default:
		jsonFehler(rw, http.StatusNotFound, "Gibt es nicht.")
	}
}

func (w *Web) medium(rw http.ResponseWriter, r *http.Request, rel string) {
	rel = path.Clean("/" + rel)[1:]
	if rel == "" || rel == "." {
		jsonFehler(rw, http.StatusNotFound, "Gibt es nicht.")
		return
	}
	innen, offen := w.Tresor.Innen()
	if !offen {
		jsonFehler(rw, http.StatusLocked, "Das Archiv ist abgeschlossen. Öffne es, dann lassen sich die WhatsApp-Bilder und -Videos zurückholen.")
		return
	}
	id, err := identitaetLesen(innen.Identitaet())
	if err != nil {
		log.Printf("archiv: Medium: %v", err)
		jsonFehler(rw, http.StatusServiceUnavailable, saysStartet)
		return
	}
	f, err := os.OpenInRoot(filepath.Join(w.Letzte, "whatsapp", "Media"), filepath.FromSlash(rel)+".age")
	if err != nil {
		jsonFehler(rw, http.StatusNotFound, "Diese Datei gibt es nicht.")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		jsonFehler(rw, http.StatusNotFound, "Diese Datei gibt es nicht.")
		return
	}
	// decrypted chunk by chunk where it is read: a Range (a phone resuming a
	// large video) costs only the chunks it touches, and the plaintext's
	// length is known before the first byte (Content-Length)
	klar, groesse, err := age.DecryptReaderAt(f, fi.Size(), id)
	if err != nil {
		log.Printf("archiv: Medium %s: %v", rel, err)
		jsonFehler(rw, http.StatusInternalServerError, "Diese Datei ließ sich nicht entschlüsseln. Sag dem Admin Bescheid.")
		return
	}
	typ := mime.TypeByExtension(path.Ext(rel))
	if typ == "" {
		typ = "application/octet-stream"
	}
	rw.Header().Set("Content-Type", typ)
	rw.Header().Set("Cache-Control", "no-store")
	http.ServeContent(rw, r, "", fi.ModTime(), io.NewSectionReader(klar, 0, groesse))
}

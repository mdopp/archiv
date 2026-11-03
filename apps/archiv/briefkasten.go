package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
)

// The Briefkasten (owner, 2026-11-01): what arrives while the vault is
// CLOSED is made unreadable on the box without being able to read the
// archive. The Mailholer writes new mail as packets, age-encrypted to the
// person's public key, into /data/briefkasten/; WhatsApp's newest database
// is kept in /eingang/letzte/ and its plaintext media are encrypted there.
// When the vault opens, the Postbote brings the packets into msgvault.
// Signal backups are no longer packed here (2026-11-02): msgvault's
// import-signal reads the decrypted backup as a folder, and that plaintext
// may exist only inside the vault, so the Postbote reads them while open.

// What the Briefkasten says, one German sentence each.
const (
	saysLetzteFehler = "Die neueste Sicherung ließ sich nicht für ein neues Handy zurücklegen. Sag dem Admin Bescheid."
)

// Gesehen is the set of messages already packed, per source: HMAC-SHA256
// of each message's identity under a key that lives beside it, so the
// set says nothing about the messages (the key is not secret from root,
// but without it a digest cannot be tested against a guessed message
// from outside the box, e.g. from a restic snapshot of /data).
type Gesehen struct {
	Dir string

	mu  sync.Mutex // the Briefkasten and the Mailholer share it: one key, made once
	key []byte
}

func (g *Gesehen) schluessel() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.key != nil {
		return g.key, nil
	}
	p := filepath.Join(g.Dir, "schluessel")
	k, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		k = make([]byte, 32)
		rand.Read(k)
		if err := os.MkdirAll(g.Dir, 0o700); err != nil {
			return nil, err
		}
		err = schreibenAtomar(p, k, 0o600)
	}
	if err != nil {
		return nil, err
	}
	g.key = k
	return k, nil
}

func (g *Gesehen) Mac(identitaet string) ([32]byte, error) {
	k, err := g.schluessel()
	if err != nil {
		return [32]byte{}, err
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte(identitaet))
	var out [32]byte
	copy(out[:], m.Sum(nil))
	return out, nil
}

// Laden reads the set of a source.
func (g *Gesehen) Laden(quelle string) (map[[32]byte]bool, error) {
	set := map[[32]byte]bool{}
	b, err := os.ReadFile(filepath.Join(g.Dir, quelle))
	if errors.Is(err, os.ErrNotExist) {
		return set, nil
	}
	if err != nil {
		return nil, err
	}
	for i := 0; i+32 <= len(b); i += 32 {
		var d [32]byte
		copy(d[:], b[i:i+32])
		set[d] = true
	}
	return set, nil
}

// Anhaengen adds digests to the set of a source, durably.
func (g *Gesehen) Anhaengen(quelle string, neu [][32]byte) error {
	if len(neu) == 0 {
		return nil
	}
	if err := os.MkdirAll(g.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(g.Dir, quelle), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, d := range neu {
		w.Write(d[:])
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Briefkasten keeps the inbox safe while the vault is closed or open.
type Briefkasten struct {
	Eingang    *Eingang
	Letzte     string // /eingang/letzte
	Post       string // /data/briefkasten: the packets
	Empfaenger string // /data/briefkasten.pub: the person's public key
	Gesehen    *Gesehen
	Protokoll  *Protokoll
	Takt       time.Duration
	Geaendert  func()
	Now        func() time.Time
}

func (b *Briefkasten) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Briefkasten) geaendert() {
	if b.Geaendert != nil {
		b.Geaendert()
	}
}

// Lauf packs until ctx ends: at once, then every Takt.
func (b *Briefkasten) Lauf(ctx context.Context) {
	takt := b.Takt
	if takt <= 0 {
		takt = 20 * time.Second
	}
	for {
		b.Runde(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(takt):
		}
	}
}

// Runde handles what is stable in the inbox once.
func (b *Briefkasten) Runde(ctx context.Context) {
	empf, err := empfaengerLesen(b.Empfaenger)
	if err != nil {
		return // no vault made yet: nobody to write to
	}
	b.aushaengen(empf)
	b.whatsapp(ctx, empf)
}

// aushaengen puts the Briefkasten's public key into the inbox as
// .briefkasten.pub (a dot-file: never a backup), where Homes hands it to
// the person's phone: the Homes app encrypts SMS and calls to it before
// they leave the phone (ADR 0029 §9).
func (b *Briefkasten) aushaengen(empf *age.X25519Recipient) {
	ziel := filepath.Join(b.Eingang.Dir, ".briefkasten.pub")
	inhalt := []byte(empf.String() + "\n")
	if alt, err := os.ReadFile(ziel); err == nil && bytes.Equal(alt, inhalt) {
		return
	}
	tmp := ziel + ".neu-" + zufall()
	err := os.WriteFile(tmp, inhalt, 0o644)
	if err == nil {
		err = os.Rename(tmp, ziel)
	}
	if err != nil {
		os.Remove(tmp)
		log.Printf("archiv: Briefkasten-Schlüssel in den Eingang legen: %v", err)
	}
}

func zufall() string {
	r := make([]byte, 8)
	rand.Read(r)
	return hex.EncodeToString(r)
}

// PackeXML writes the messages of an SMS Backup & Restore XML that are
// not in gesehen as one packet to out. It returns the digests to add to
// the set once the packet is in place.
func PackeXML(in io.Reader, out io.Writer, empf age.Recipient, m Manifest, telefon string, g *Gesehen, gesehen map[[32]byte]bool, jetzt time.Time) (neu [][32]byte, n, an int, err error) {
	p, err := NeuesPaket(out, empf, m, jetzt)
	if err != nil {
		return nil, 0, 0, err
	}
	diesmal := map[[32]byte]bool{}
	err = XMLLesen(in, telefon, func(x xmlNachricht) error {
		ident := x.N.identitaet(summen(x.Daten))
		mac, err := g.Mac(ident)
		if err != nil {
			return err
		}
		if gesehen[mac] || diesmal[mac] {
			return nil
		}
		diesmal[mac] = true
		h := sha256.Sum256([]byte(ident))
		x.N.ID = m.Quelle + ":" + hex.EncodeToString(h[:16])
		for i := range x.N.Anhaenge {
			a, err := p.Anhang(x.N.Anhaenge[i].Typ, x.N.Anhaenge[i].Name, x.Daten[i])
			if err != nil {
				return err
			}
			x.N.Anhaenge[i] = a
			an++
		}
		neu = append(neu, mac)
		return p.Nachricht(x.N)
	})
	if err != nil {
		return nil, 0, 0, err
	}
	if err := p.Schliessen(); err != nil {
		return nil, 0, 0, err
	}
	return neu, p.Anzahl(), an, nil
}

// letzteBehalten moves an imported backup to letzte/<quelle>/ when it is
// the newest there, and removes the older one; an older backup is deleted.
func letzteBehalten(letzte, quelle string, d Datei, name string) error {
	dir := filepath.Join(letzte, quelle)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	alte, _ := os.ReadDir(dir)
	for _, e := range alte {
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() && fi.ModTime().After(d.Mtime) {
			return os.Remove(d.Pfad) // a newer one is kept already
		}
	}
	if err := os.Rename(d.Pfad, filepath.Join(dir, name)); err != nil {
		return err
	}
	for _, e := range alte {
		if e.Name() != name && e.Type().IsRegular() {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// whatsapp keeps the newest database in letzte/ (a link: the inbox copy
// waits for the WhatsApp import, which is not there yet) and moves every
// stable media file there age-encrypted: WhatsApp's media are plaintext.
func (b *Briefkasten) whatsapp(ctx context.Context, empf age.Recipient) {
	db, err := b.Eingang.Stabil("whatsapp")
	if err != nil {
		log.Printf("archiv: Eingang whatsapp: %v", err)
	}
	if len(db) > 0 {
		neueste := db[len(db)-1] // oldest first
		ziel := filepath.Join(b.Letzte, "whatsapp", "Databases", "msgstore.db.crypt15")
		if fi, err := os.Stat(ziel); err != nil || neueste.Mtime.After(fi.ModTime()) {
			if err := verlinken(neueste.Pfad, ziel); err != nil {
				log.Printf("archiv: letzte whatsapp: %v", err)
				b.Protokoll.Fehlschlag(Fehler{Am: b.now(), Quelle: "whatsapp", Datei: neueste.Rel, Satz: saysLetzteFehler})
			}
		}
	}
	medien, err := b.Eingang.StabilMedien()
	if err != nil {
		log.Printf("archiv: Eingang whatsapp/Media: %v", err)
	}
	for _, m := range medien {
		if ctx.Err() != nil {
			return
		}
		ziel := filepath.Join(b.Letzte, "whatsapp", "Media", filepath.FromSlash(m.Rel)+".age")
		if err := verschluesseln(m.Pfad, ziel, empf); err != nil {
			log.Printf("archiv: Medium %s: %v", m.Rel, err)
			continue
		}
		os.Remove(m.Pfad) // only the encrypted copy stays
	}
}

// verlinken puts a hard link (or, across file systems, a copy) of src at
// ziel, replacing what is there, through a temporary name.
func verlinken(src, ziel string) error {
	if err := os.MkdirAll(filepath.Dir(ziel), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(ziel), "."+filepath.Base(ziel)+"."+zufall())
	if err := os.Link(src, tmp); err != nil {
		if err := kopieren(src, tmp); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, ziel); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func kopieren(src, ziel string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, _ := in.Stat()
	out, err := os.OpenFile(ziel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(ziel, fi.ModTime(), fi.ModTime())
}

// verschluesseln writes src age-encrypted to ziel (through a temporary name).
func verschluesseln(src, ziel string, empf age.Recipient) error {
	if err := os.MkdirAll(filepath.Dir(ziel), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := filepath.Join(filepath.Dir(ziel), "."+filepath.Base(ziel)+"."+zufall())
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	w, err := age.Encrypt(out, empf)
	if err == nil {
		_, err = io.Copy(w, in)
	}
	if err == nil {
		err = w.Close()
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if fi, err := in.Stat(); err == nil {
		os.Chtimes(tmp, fi.ModTime(), fi.ModTime())
	}
	return os.Rename(tmp, ziel)
}

// einwerfen puts a finished packet into the Briefkasten under
// <UTC time>-<quelle>.age, never over another one: a name that is taken
// moves on by a second, so the names stay in the order they were made.
func einwerfen(tmp, post, quelle string, jetzt time.Time) (string, error) {
	t := jetzt.UTC().Truncate(time.Second)
	for i := 0; i < 3600; i++ {
		name := t.Format("20060102T150405Z") + "-" + quelle + ".age"
		err := os.Link(tmp, filepath.Join(post, name))
		if err == nil {
			os.Remove(tmp)
			if dir, err := os.Open(post); err == nil {
				dir.Sync()
				dir.Close()
			}
			return name, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		t = t.Add(time.Second)
	}
	return "", errors.New("kein freier Name im Briefkasten")
}

// HalbeWeg removes the unfinished packets a stopped container left (their
// writer's cleanup does not run when the process ends mid-packet: the
// box, 2026-11-02, kept 193 MB of a first mail fetch). Called at start,
// before anything writes; a restart fetches or reads those messages again.
func HalbeWeg(post string) {
	eintraege, _ := os.ReadDir(post)
	for _, e := range eintraege {
		if strings.HasPrefix(e.Name(), ".neu-") && e.Type().IsRegular() {
			if err := os.Remove(filepath.Join(post, e.Name())); err == nil {
				log.Printf("archiv: halbes Paket %s vom letzten Lauf entfernt", e.Name())
			}
		}
	}
}

// Pakete lists the packets waiting in the Briefkasten, oldest first
// (their names begin with the UTC time).
func Pakete(post string) []string {
	var out []string
	eintraege, _ := os.ReadDir(post)
	for _, e := range eintraege {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".age") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// paketZeit reads the time from a packet's name.
func paketZeit(name string) (time.Time, bool) {
	i := strings.IndexByte(name, '-')
	if i < 0 {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102T150405Z", name[:i])
	return t, err == nil
}

// falschePassphrase reads signalbackup-tools' output: a bad MAC on the
// first frame is, in its own words, likely an incorrect passphrase.
func falschePassphrase(text string) bool {
	t := strings.ToUpper(text)
	return strings.Contains(t, "INCORRECT PASSPHRASE") || strings.Contains(t, "FAILED TO GET BACKUPKEY FROM PASSPHRASE")
}

// fehlerzeilen keeps the lines of a program's output that report an
// error, at most ten: the log gets the reason, not the export's chatter
// (which names contacts).
func fehlerzeilen(text string) string {
	var out []string
	for _, z := range strings.Split(text, "\n") {
		l := strings.ToLower(z)
		if strings.Contains(l, "error") || strings.Contains(l, "failed") || strings.Contains(l, "warning") {
			out = append(out, strings.TrimSpace(z))
		}
	}
	if len(out) > 10 {
		out = out[len(out)-10:]
	}
	if len(out) == 0 {
		return "(keine Fehlerzeile)"
	}
	return strings.Join(out, " | ")
}

package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"filippo.io/age"
)

// The Briefkasten (owner, 2026-11-01): the inbox is read while the vault
// is CLOSED, without being able to read the archive. Each new stable
// Signal backup, oldest first, is decrypted by signalbackup-tools into a
// named pipe (the plaintext is never a file), the messages not seen
// before are written as ONE packet, age-encrypted to the person's public
// key, into /data/briefkasten/, and the backup moves to /eingang/letzte/
// (the newest one stays there for restoring a new phone; the older one
// goes). When the vault opens, the Postbote brings the packets into
// msgvault. The price, said on the page: the Signal passphrase lies on
// the box, outside the vault, so root can decrypt FUTURE backups as they
// arrive; the archive's history stays readable only with the password.

// What the Briefkasten says, one German sentence each.
const (
	saysSignalFalsch   = "Die Signal-Passphrase passt nicht zu dieser Sicherung. Gib sie neu ein."
	saysSignalUnlesbar = "Die Signal-Sicherung %s ließ sich nicht lesen. Sie bleibt im Eingang; sag dem Admin Bescheid."
	saysPaketFehler    = "Aus der Signal-Sicherung %s ließ sich kein Paket schreiben. Sie bleibt im Eingang; sag dem Admin Bescheid."
	saysLetzteFehler   = "Die neueste Sicherung ließ sich nicht für ein neues Handy zurücklegen. Sag dem Admin Bescheid."
)

var ErrSignalPassphrase = errors.New(saysSignalFalsch)

// Gesehen is the set of messages already packed, per source: HMAC-SHA256
// of each message's identity under a key that lives beside it, so the
// set says nothing about the messages (the key is not secret from root,
// but without it a digest cannot be tested against a guessed message
// from outside the box, e.g. from a restic snapshot of /data).
type Gesehen struct {
	Dir string

	key []byte
}

func (g *Gesehen) schluessel() ([]byte, error) {
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

// Briefkasten turns inbox backups into packets, vault closed or open.
type Briefkasten struct {
	Eingang    *Eingang
	Letzte     string // /eingang/letzte
	Post       string // /data/briefkasten: the packets
	Empfaenger string // /data/briefkasten.pub: the person's public key
	Gesehen    *Gesehen
	Schluessel Schluessel // /data/schluessel: the Signal passphrase and the number, outside the vault
	Klartext   string     // a tmpfs folder for the named pipe (/dev/shm/archiv)
	Run        Ausfuehrer
	SignalBin  string
	Protokoll  *Protokoll
	Takt       time.Duration
	Geaendert  func()
	Now        func() time.Time

	mu         sync.Mutex
	laeuft     string
	zurueck    map[string]bool
	falsch     bool
	keinSignal bool
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

// Laeuft is the backup being packed now, or "".
func (b *Briefkasten) Laeuft() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.laeuft
}

// FehlenSchluessel says whether Signal backups wait for the passphrase or the number.
func (b *Briefkasten) FehlenSchluessel() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.keinSignal
}

// NeuerSchluessel lets the backups that failed try again.
func (b *Briefkasten) NeuerSchluessel() {
	b.mu.Lock()
	b.zurueck, b.falsch = nil, false
	b.mu.Unlock()
	b.Protokoll.FehlerWeg("signal")
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
	b.signal(ctx, empf)
	if ctx.Err() == nil {
		b.whatsapp(ctx, empf)
	}
}

func (b *Briefkasten) signal(ctx context.Context, empf age.Recipient) {
	stabil, err := b.Eingang.Stabil("signal")
	if err != nil {
		log.Printf("archiv: Eingang lesen: %v", err)
	}
	pass, errP := b.Schluessel.Lesen(SchluesselSignal)
	tel, errT := b.Schluessel.Lesen(SchluesselTelefon)
	defer nullen(pass)
	b.mu.Lock()
	b.keinSignal = len(stabil) > 0 && (errP != nil || errT != nil)
	falsch := b.falsch
	b.mu.Unlock()
	if errP != nil || errT != nil || falsch {
		return
	}
	for _, d := range stabil {
		if ctx.Err() != nil {
			return
		}
		b.mu.Lock()
		weg := b.zurueck[d.schluessel()]
		b.mu.Unlock()
		if weg {
			continue
		}
		err := b.Packen(ctx, d, empf, pass, string(tel))
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			b.mu.Lock()
			if b.zurueck == nil {
				b.zurueck = map[string]bool{}
			}
			b.zurueck[d.schluessel()] = true
			if errors.Is(err, ErrSignalPassphrase) {
				b.falsch = true // the same key fails on the others too
			}
			b.mu.Unlock()
			if errors.Is(err, ErrSignalPassphrase) {
				return
			}
		}
	}
}

func zufall() string {
	r := make([]byte, 8)
	rand.Read(r)
	return hex.EncodeToString(r)
}

// Packen makes the packet of one Signal backup. Nothing of it touches the
// vault; the plaintext flows through a named pipe and is never a file.
func (b *Briefkasten) Packen(ctx context.Context, d Datei, empf age.Recipient, pass []byte, telefon string) error {
	b.mu.Lock()
	b.laeuft = d.Rel
	b.mu.Unlock()
	b.geaendert()
	defer func() {
		b.mu.Lock()
		b.laeuft = ""
		b.mu.Unlock()
		b.geaendert()
	}()
	fehler := func(satz string, grund error) error {
		log.Printf("archiv: signal %s: %v", d.Rel, grund)
		if ctx.Err() == nil {
			b.Protokoll.Fehlschlag(Fehler{Am: b.now(), Quelle: "signal", Datei: d.Rel, Satz: satz})
		}
		return grund
	}

	if err := os.MkdirAll(b.Klartext, 0o700); err != nil {
		return fehler(fmt.Sprintf(saysPaketFehler, d.Rel), err)
	}
	rohr := filepath.Join(b.Klartext, "signal-"+zufall()+".xml")
	if err := syscall.Mkfifo(rohr, 0o600); err != nil {
		return fehler(fmt.Sprintf(saysPaketFehler, d.Rel), fmt.Errorf("mkfifo: %w", err))
	}
	defer os.Remove(rohr)

	if err := os.MkdirAll(b.Post, 0o700); err != nil {
		return fehler(fmt.Sprintf(saysPaketFehler, d.Rel), err)
	}
	jetzt := b.now().UTC()
	tmp := filepath.Join(b.Post, ".neu-signal-"+zufall()+".age")
	defer os.Remove(tmp) // gone unless renamed into place

	gesehen, err := b.Gesehen.Laden("signal")
	if err != nil {
		return fehler(fmt.Sprintf(saysPaketFehler, d.Rel), err)
	}

	type ergebnis struct {
		neu   [][32]byte
		n, an int
		err   error
	}
	leser := make(chan ergebnis, 1)
	go func() {
		var e ergebnis
		e.neu, e.n, e.an, e.err = b.lesen(rohr, tmp, empf, d, telefon, gesehen, jetzt)
		leser <- e
	}()

	a, err := b.Run.Run(ctx, signalExport(b.SignalBin, d.Pfad, rohr, telefon, pass))
	// a reader still waiting for the pipe to open: let it see the end
	var e ergebnis
	for warten := true; warten; {
		select {
		case e = <-leser:
			warten = false
		case <-time.After(50 * time.Millisecond):
			if w, err := os.OpenFile(rohr, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				w.Close()
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && a.Code != 0 {
		err = fmt.Errorf("signalbackup-tools Code %d", a.Code)
	}
	if err != nil {
		if falschePassphrase(a.Text) {
			return fehler(saysSignalFalsch, ErrSignalPassphrase)
		}
		log.Printf("archiv: signalbackup-tools: %s", fehlerzeilen(schwaerzen(a.Text, pass)))
		return fehler(fmt.Sprintf(saysSignalUnlesbar, d.Rel), err)
	}
	if e.err != nil {
		return fehler(fmt.Sprintf(saysPaketFehler, d.Rel), e.err)
	}
	paket := ""
	if e.n > 0 {
		name, err := einwerfen(tmp, b.Post, "signal", jetzt)
		if err != nil {
			return fehler(fmt.Sprintf(saysPaketFehler, d.Rel), err)
		}
		paket = name
	}
	if err := b.Gesehen.Anhaengen("signal", e.neu); err != nil {
		log.Printf("archiv: gesehen: %v (the next packet repeats these messages; msgvault keeps one of each)", err)
	}
	log.Printf("archiv: signal %s → %d neue Nachrichten, %d Anhänge %s", d.Rel, e.n, e.an, paket)
	b.Protokoll.Gepackt("signal", Gepackt{Am: b.now(), Sicherung: d.Rel, Paket: paket, Nachrichten: e.n, Anhaenge: e.an})
	if err := b.zuruecklegen("signal", d, filepath.Base(d.Rel)); err != nil {
		return fehler(saysLetzteFehler, err)
	}
	return nil
}

// lesen reads the XML from the pipe and writes the packet to tmp.
func (b *Briefkasten) lesen(rohr, tmp string, empf age.Recipient, d Datei, telefon string, gesehen map[[32]byte]bool, jetzt time.Time) (neu [][32]byte, n, an int, err error) {
	in, err := os.Open(rohr) // blocks until signalbackup-tools opens it, or Packen does
	if err != nil {
		return nil, 0, 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		io.Copy(io.Discard, in)
		return nil, 0, 0, err
	}
	defer out.Close()
	neu, n, an, err = PackeXML(in, out, empf, Manifest{Quelle: "signal", Herkunft: "box", Sicherung: filepath.Base(d.Rel)}, telefon, b.Gesehen, gesehen, jetzt)
	if err != nil {
		io.Copy(io.Discard, in) // let the writer finish or fail on its own
		return nil, 0, 0, err
	}
	return neu, n, an, out.Sync()
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

// zuruecklegen moves a packed backup to letzte/<quelle>/ when it is the
// newest there, and removes the older one; an older backup is deleted.
func (b *Briefkasten) zuruecklegen(quelle string, d Datei, name string) error {
	dir := filepath.Join(b.Letzte, quelle)
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

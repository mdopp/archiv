package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
)

// The Postbote brings the Briefkasten's packets into msgvault while the
// vault is open: oldest first, each decrypted with the private key from
// the vault, its attachments unpacked into the vault's work folder,
// written as an SMS Backup & Restore XML there, imported, and deleted.
// A mail packet is a Maildir: unpacked there and handed to
// import-maildir. Additive only: msgvault upserts every message by a
// stable id (mail: by a hash of the raw message), and neither import has
// an option that deletes; nothing here runs anything else. The work
// folder is emptied after every packet.

const (
	saysPaketKaputt   = "Das Paket %s ließ sich nicht öffnen. Es bleibt im Briefkasten; sag dem Admin Bescheid."
	saysGrosserAnhang = "[Anhang %s, %d MB: zu groß für die Suche im Archiv. Er liegt im Tresor unter grosse-anhaenge/%s.]"
	saysUebernahme    = "Das Archiv konnte die Nachrichten aus %s nicht übernehmen. Das Paket bleibt im Briefkasten; beim nächsten Öffnen versucht es das Archiv noch einmal."
	saysKeineNummer   = "Pakete warten, aber deine Handynummer fehlt. Gib sie unter „Schlüssel“ ein."
	saysAnderesPaket  = "Das Paket %s hat ein Format, das dieses Archiv noch nicht kennt. Es bleibt im Briefkasten."
)

var (
	importiert      = regexp.MustCompile(`Imported (\d+) SMS, (\d+) MMS, (\d+) calls, (\d+) attachments`)
	mailsImportiert = regexp.MustCompile(`Messages: (\d+) added, (\d+) updated, (\d+) skipped`)
	errOhneNummer   = errors.New("ohne Nummer")
)

type Postbote struct {
	Post       string     // /data/briefkasten
	Schluessel Schluessel // outside: the number
	Protokoll  *Protokoll
	Run        Ausfuehrer
	Msgvault   string
	Takt       time.Duration
	Arbeit     func() func() // Tresor.Arbeit: no idle close while one is delivered
	Geaendert  func()
	Now        func() time.Time

	mu      sync.Mutex
	laeuft  string
	gang    Gang
	zurueck map[string]bool
}

// Gang is one piece of work running now, as the page shows it. Gesamt 0:
// the size is not known (yet); Fertig 0 with Gesamt > 0: no progress is
// reported (msgvault's import says nothing until it ends).
type Gang struct {
	Was    string    // the packet or backup
	Quelle string    // signal, mail
	Seit   time.Time // started
	Fertig int
	Gesamt int
	Bis    time.Time // the newest message so far (mail)
}

// Gang says what is delivered now; Was is empty when nothing is.
func (p *Postbote) Gang() Gang {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gang
}

func (p *Postbote) gangSetzen(f func(*Gang)) {
	p.mu.Lock()
	f(&p.gang)
	p.mu.Unlock()
	p.geaendert()
}

func (p *Postbote) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Postbote) Laeuft() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.laeuft
}

func (p *Postbote) geaendert() {
	if p.Geaendert != nil {
		p.Geaendert()
	}
}

// Lauf delivers until ctx ends (the vault closes): at once, then every Takt.
func (p *Postbote) Lauf(ctx context.Context, innen Innen) {
	p.mu.Lock()
	p.zurueck = nil
	p.mu.Unlock()
	takt := p.Takt
	if takt <= 0 {
		takt = 20 * time.Second
	}
	for {
		p.Runde(ctx, innen)
		select {
		case <-ctx.Done():
			return
		case <-time.After(takt):
		}
	}
}

func (p *Postbote) Runde(ctx context.Context, innen Innen) {
	pakete := Pakete(p.Post)
	if len(pakete) == 0 {
		return
	}
	tel, _ := p.Schluessel.Lesen(SchluesselTelefon) // a mail packet needs none; a Signal packet waits for it
	id, err := identitaetLesen(innen.Identitaet())
	if err != nil {
		log.Printf("archiv: der private Schlüssel fehlt im Tresor: %v", err)
		return
	}
	for _, name := range pakete {
		if ctx.Err() != nil {
			return
		}
		p.mu.Lock()
		weg := p.zurueck[name]
		p.mu.Unlock()
		if weg {
			continue
		}
		if err := p.Zustellen(ctx, innen, id, name, string(tel)); err != nil && ctx.Err() == nil && !errors.Is(err, errOhneNummer) {
			p.mu.Lock()
			if p.zurueck == nil {
				p.zurueck = map[string]bool{}
			}
			p.zurueck[name] = true
			p.mu.Unlock()
		}
	}
}

// Zustellen imports one packet and deletes it. Its plaintext exists only
// inside the vault's work folder, and only for this call.
func (p *Postbote) Zustellen(ctx context.Context, innen Innen, id age.Identity, name, telefon string) (err error) {
	if p.Arbeit != nil {
		defer p.Arbeit()()
	}
	p.mu.Lock()
	p.laeuft = name
	p.gang = Gang{Was: name, Seit: p.now()}
	p.mu.Unlock()
	p.geaendert()
	defer func() {
		p.mu.Lock()
		p.laeuft = ""
		p.gang = Gang{}
		p.mu.Unlock()
		p.geaendert()
	}()
	quelle := "signal"
	fehler := func(satz string, grund error) error {
		log.Printf("archiv: Paket %s: %v", name, grund)
		if ctx.Err() == nil {
			p.Protokoll.Fehlschlag(Fehler{Am: p.now(), Quelle: quelle, Datei: name, Satz: satz})
		}
		return grund
	}

	arbeit := filepath.Join(innen.Arbeit(), "paket-"+zufall())
	defer os.RemoveAll(arbeit)
	xml := arbeit + ".xml"
	defer os.Remove(xml)
	if err := os.MkdirAll(filepath.Join(arbeit, "anhaenge"), 0o700); err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}

	f, err := os.Open(filepath.Join(p.Post, name))
	if err != nil {
		return fehler(fmt.Sprintf(saysPaketKaputt, name), err)
	}
	var nachrichten []Nachricht
	m, err := PaketLesen(f, id,
		func(datei string, r io.Reader) error {
			ziel := filepath.Join(arbeit, "anhaenge", datei)
			if strings.HasPrefix(datei, "maildir/") { // checked by PaketLesen: clean, no dot-level
				ziel = filepath.Join(arbeit, filepath.FromSlash(datei))
				if err := maildirAnlegen(filepath.Dir(filepath.Dir(ziel))); err != nil {
					return err
				}
			}
			out, err := os.OpenFile(ziel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, r)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			return err
		},
		func(n Nachricht) error { nachrichten = append(nachrichten, n); return nil })
	f.Close()
	if m.Quelle != "" {
		quelle = m.Quelle
	}
	menge := len(nachrichten)
	if quelle == "mail" {
		menge = zaehleMails(filepath.Join(arbeit, "maildir"))
	}
	p.gangSetzen(func(g *Gang) { g.Quelle, g.Gesamt = quelle, menge })
	if errors.Is(err, ErrPaketFormat) {
		return fehler(fmt.Sprintf(saysAnderesPaket, name), err)
	}
	if err != nil {
		return fehler(fmt.Sprintf(saysPaketKaputt, name), err)
	}

	if quelle == "mail" {
		return p.mailZustellen(ctx, innen, name, m, filepath.Join(arbeit, "maildir"), fehler)
	}
	if telefon == "" {
		return errOhneNummer // the page says it (saysKeineNummer); the packet waits
	}

	out, err := os.OpenFile(xml, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}
	x, err := NeuesXML(out, telefon)
	for i := 0; err == nil && i < len(nachrichten); i++ {
		err = x.Nachricht(nachrichten[i], func(a Anhang) (io.ReadCloser, string, error) {
			pfad := filepath.Join(arbeit, "anhaenge", filepath.Base(a.Datei))
			return anhangOderHinweis(pfad, a, innen.GrosseAnhaenge(), strings.TrimSuffix(name, ".age"))
		})
	}
	if err == nil {
		err = x.Schliessen()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}

	a, err := p.Run.Run(ctx, msgvaultImportSMS(p.Msgvault, innen.Home(), xml, telefon))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && a.Code != 0 {
		err = fmt.Errorf("msgvault import-synctech-sms Code %d: %s", a.Code, fehlerzeilen(a.Text))
	}
	if err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}
	var z Zahlen
	if m := importiert.FindStringSubmatch(a.Text); m != nil {
		z.SMS, _ = strconv.Atoi(m[1])
		z.MMS, _ = strconv.Atoi(m[2])
		z.Anrufe, _ = strconv.Atoi(m[3])
		z.Anhaenge, _ = strconv.Atoi(m[4])
	}
	if err := os.Remove(filepath.Join(p.Post, name)); err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}
	log.Printf("archiv: Paket %s eingelesen: %d SMS, %d MMS, %d Anhänge", name, z.SMS, z.MMS, z.Anhaenge)
	p.Protokoll.Eingelesen(quelle, name, z, p.now())
	return nil
}

// zaehleMails counts the messages of an unpacked Maildir, for the page.
func zaehleMails(dir string) int {
	n := 0
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// anhangOderHinweis opens an attachment for the XML, or, when msgvault
// would refuse it (MaxAnhang), moves it into the vault beside the archive
// and returns the note the message carries instead. The file is named by
// packet and position, so a retried packet lands on the same name.
func anhangOderHinweis(pfad string, a Anhang, ablage, paket string) (io.ReadCloser, string, error) {
	fi, err := os.Stat(pfad)
	if err != nil {
		return nil, "", err
	}
	if fi.Size() <= MaxAnhang {
		f, err := os.Open(pfad)
		return f, "", err
	}
	ziel := paket + "-" + filepath.Base(a.Datei)
	if n := filepath.Base(a.Name); a.Name != "" && n != "." && n != ".." && n != "/" {
		ziel += "-" + n
	}
	if err := os.MkdirAll(ablage, 0o700); err != nil {
		return nil, "", err
	}
	if err := os.Rename(pfad, filepath.Join(ablage, ziel)); err != nil {
		return nil, "", err
	}
	was := a.Name
	if was == "" {
		was = a.Typ
	}
	return nil, fmt.Sprintf(saysGrosserAnhang, was, (fi.Size()+(1<<20)-1)>>20, ziel), nil
}

// maildirAnlegen makes a Maildir folder whole (cur, new, tmp): msgvault
// takes only a folder with all three as a mailbox; a packet carries cur.
func maildirAnlegen(dir string) error {
	for _, d := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			return err
		}
	}
	return nil
}

// mailZustellen hands a mail packet's Maildir to msgvault, under the
// account the packet names, and deletes the packet.
func (p *Postbote) mailZustellen(ctx context.Context, innen Innen, name string, m Manifest, maildir string, fehler func(string, error) error) error {
	if m.Konto == "" {
		return fehler(fmt.Sprintf(saysAnderesPaket, name), errors.New("ein Mail-Paket ohne Konto"))
	}
	if _, err := os.Stat(maildir); err != nil {
		return fehler(fmt.Sprintf(saysPaketKaputt, name), errors.New("ein Mail-Paket ohne Mails"))
	}
	a, err := p.Run.Run(ctx, msgvaultImportMaildir(p.Msgvault, innen.Home(), maildir, m.Konto))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && a.Code != 0 {
		err = fmt.Errorf("msgvault import-maildir Code %d: %s", a.Code, fehlerzeilen(a.Text))
	}
	if err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}
	var z Zahlen
	if x := mailsImportiert.FindStringSubmatch(a.Text); x != nil {
		z.Mails, _ = strconv.Atoi(x[1]) // added; "updated" are messages it had, now with one more folder
	}
	if err := os.Remove(filepath.Join(p.Post, name)); err != nil {
		return fehler(fmt.Sprintf(saysUebernahme, name), err)
	}
	log.Printf("archiv: Paket %s eingelesen: %d Mails", name, z.Mails)
	p.Protokoll.Eingelesen("mail", name, z, p.now())
	return nil
}

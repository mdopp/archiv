package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The import (laib#985): while the vault is open, each stable Signal
// backup in the inbox, oldest first, is exported by signalbackup-tools to
// an SMS Backup & Restore XML inside the vault and read by msgvault's
// import-synctech-sms. The import is additive only: msgvault upserts each
// message by a stable id and has no option that deletes what a newer
// backup lacks; nothing here calls anything that deletes. Afterwards the
// backup leaves the inbox. The plaintext XML is deleted in every case; a
// failed backup stays in the inbox and the page and the status say why.

// What the import says, one German sentence each.
const (
	saysSignalFalsch     = "Die Signal-Passphrase passt nicht zu dieser Sicherung. Gib sie neu ein."
	saysSignalUnlesbar   = "Die Signal-Sicherung %s ließ sich nicht lesen. Sie bleibt im Eingang; sag dem Admin Bescheid."
	saysSignalUebernahme = "Das Archiv konnte die Nachrichten aus %s nicht übernehmen. Die Sicherung bleibt im Eingang; beim nächsten Öffnen versucht es das Archiv noch einmal."
	saysSignalWeg        = "Die Sicherung %s ist eingelesen, ließ sich aber nicht aus dem Eingang löschen. Sag dem Admin Bescheid."
	saysArbeitPlatz      = "Im Tresor ist kein Platz für das Entpacken von %s. Sag dem Admin Bescheid."
)

// ErrSignalPassphrase is the wrong passphrase, said as the person reads it.
var ErrSignalPassphrase = errors.New(saysSignalFalsch)

// Zahlen are what one import brought.
type Zahlen struct {
	SMS      int `json:"sms"`
	MMS      int `json:"mms"`
	Anrufe   int `json:"anrufe"`
	Anhaenge int `json:"anhaenge"`
}

func (z Zahlen) Nachrichten() int { return z.SMS + z.MMS }

// Eingelesen is the last import of a source.
type Eingelesen struct {
	Am     time.Time `json:"am"`
	Datei  string    `json:"datei"`
	Zahlen Zahlen    `json:"zahlen"`
	Anzahl int       `json:"anzahl"` // backups imported since the vault was made
}

// Fehler is the last thing that went wrong, as the person reads it.
type Fehler struct {
	Am     time.Time `json:"am"`
	Quelle string    `json:"quelle"`
	Datei  string    `json:"datei,omitempty"`
	Satz   string    `json:"satz"`
}

// Protokoll is what the import keeps outside the vault (no secret, no
// message): when it read what, and the last error. It feeds the page
// while the vault is closed and the status file.
type Protokoll struct {
	Datei string

	mu            sync.Mutex
	LetzterImport map[string]*Eingelesen `json:"letzter_import"`
	LetzterFehler *Fehler                `json:"letzter_fehler,omitempty"`
	ZuSeit        *time.Time             `json:"zu_seit,omitempty"`
}

func LadeProtokoll(datei string) *Protokoll {
	p := &Protokoll{Datei: datei}
	if b, err := os.ReadFile(datei); err == nil {
		if err := json.Unmarshal(b, p); err != nil {
			log.Printf("archiv: %s: %v (beginne neu)", datei, err)
		}
	}
	if p.LetzterImport == nil {
		p.LetzterImport = map[string]*Eingelesen{}
	}
	return p
}

func (p *Protokoll) speichern() {
	b, _ := json.MarshalIndent(p, "", "  ")
	if err := schreibenAtomar(p.Datei, b, 0o644); err != nil {
		log.Printf("archiv: %s: %v", p.Datei, err)
	}
}

func (p *Protokoll) Eingelesen(quelle, datei string, z Zahlen, am time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	if alt := p.LetzterImport[quelle]; alt != nil {
		n = alt.Anzahl
	}
	p.LetzterImport[quelle] = &Eingelesen{Am: am, Datei: datei, Zahlen: z, Anzahl: n + 1}
	if p.LetzterFehler != nil && p.LetzterFehler.Quelle == quelle && p.LetzterFehler.Datei == datei {
		p.LetzterFehler = nil
	}
	p.speichern()
}

func (p *Protokoll) Fehlschlag(f Fehler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.LetzterFehler = &f
	p.speichern()
}

// FehlerWeg forgets the last error of a source (a new key was given).
func (p *Protokoll) FehlerWeg(quelle string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.LetzterFehler != nil && p.LetzterFehler.Quelle == quelle {
		p.LetzterFehler = nil
		p.speichern()
	}
}

func (p *Protokoll) Zu(am time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ZuSeit = &am
	p.speichern()
}

// Kopie is a snapshot for readers.
func (p *Protokoll) Kopie() (map[string]Eingelesen, *Fehler, *time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := map[string]Eingelesen{}
	for k, v := range p.LetzterImport {
		m[k] = *v
	}
	var f *Fehler
	if p.LetzterFehler != nil {
		x := *p.LetzterFehler
		f = &x
	}
	var zu *time.Time
	if p.ZuSeit != nil {
		x := *p.ZuSeit
		zu = &x
	}
	return m, f, zu
}

// Importeur reads the inbox into msgvault.
type Importeur struct {
	Eingang    *Eingang
	Protokoll  *Protokoll
	Run        Ausfuehrer
	SignalBin  string // signalbackup-tools
	Msgvault   string // msgvault
	Takt       time.Duration
	Arbeit     func() func() // marks an import running (Tresor.Arbeit)
	Geaendert  func()
	Now        func() time.Time
	mu         sync.Mutex
	laeuft     string          // the file being read now, for the page
	zurueck    map[string]bool // failed in this opening: not again until a new key or the next opening
	falsch     bool            // the stored passphrase did not fit: no Signal import until a new one
	keinSignal bool            // backups wait, the passphrase or the number is missing
}

func (im *Importeur) now() time.Time {
	if im.Now != nil {
		return im.Now()
	}
	return time.Now()
}

// Laeuft is the file being imported now, or "".
func (im *Importeur) Laeuft() string {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.laeuft
}

// FehlenSchluessel says whether Signal backups wait for a key.
func (im *Importeur) FehlenSchluessel() bool {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.keinSignal
}

// NeuerSchluessel lets the backups that failed with the old key try again.
func (im *Importeur) NeuerSchluessel() {
	im.mu.Lock()
	im.zurueck, im.falsch = nil, false
	im.mu.Unlock()
	im.Protokoll.FehlerWeg("signal")
}

// Lauf imports until ctx ends: at once, then every Takt.
func (im *Importeur) Lauf(ctx context.Context, innen Innen) {
	im.mu.Lock()
	im.zurueck, im.falsch = nil, false
	im.mu.Unlock()
	takt := im.Takt
	if takt <= 0 {
		takt = 20 * time.Second
	}
	for {
		im.Runde(ctx, innen)
		select {
		case <-ctx.Done():
			return
		case <-time.After(takt):
		}
	}
}

// Runde imports every stable Signal backup once, oldest first.
func (im *Importeur) Runde(ctx context.Context, innen Innen) {
	stabil, err := im.Eingang.Stabil("signal")
	if err != nil {
		log.Printf("archiv: Eingang lesen: %v", err)
	}
	sch := Schluessel{innen.Schluessel()}
	pass, errP := sch.Lesen(SchluesselSignal)
	tel, errT := sch.Lesen(SchluesselTelefon)
	defer nullen(pass)
	im.mu.Lock()
	im.keinSignal = len(stabil) > 0 && (errP != nil || errT != nil)
	falsch := im.falsch
	im.mu.Unlock()
	if errP != nil || errT != nil || falsch {
		return
	}
	for _, d := range stabil {
		if ctx.Err() != nil {
			return
		}
		im.mu.Lock()
		weg := im.zurueck[d.schluessel()]
		im.mu.Unlock()
		if weg {
			continue
		}
		err := im.Signal(ctx, innen, d, pass, string(tel))
		if ctx.Err() != nil {
			return // closing: the backup stays, nothing to report
		}
		if err != nil {
			im.mu.Lock()
			if im.zurueck == nil {
				im.zurueck = map[string]bool{}
			}
			im.zurueck[d.schluessel()] = true
			if errors.Is(err, ErrSignalPassphrase) {
				im.falsch = true // the same key fails on the others too
			}
			im.mu.Unlock()
			if errors.Is(err, ErrSignalPassphrase) {
				return
			}
		}
	}
}

var importiert = regexp.MustCompile(`Imported (\d+) SMS, (\d+) MMS, (\d+) calls, (\d+) attachments`)

// Signal imports one backup. The XML (plaintext) never outlives this call.
func (im *Importeur) Signal(ctx context.Context, innen Innen, d Datei, pass []byte, telefon string) (err error) {
	if im.Arbeit != nil {
		defer im.Arbeit()()
	}
	im.mu.Lock()
	im.laeuft = d.Rel
	im.mu.Unlock()
	if im.Geaendert != nil {
		im.Geaendert()
	}
	defer func() {
		im.mu.Lock()
		im.laeuft = ""
		im.mu.Unlock()
		if im.Geaendert != nil {
			im.Geaendert()
		}
	}()
	fehler := func(satz string, grund error) error {
		log.Printf("archiv: signal %s: %v", d.Rel, grund)
		if ctx.Err() == nil {
			im.Protokoll.Fehlschlag(Fehler{Am: im.now(), Quelle: "signal", Datei: d.Rel, Satz: satz})
		}
		return grund
	}

	r := make([]byte, 8)
	rand.Read(r)
	xml := filepath.Join(innen.Arbeit(), "signal-"+hex.EncodeToString(r)+".xml")
	defer func() {
		if e := os.Remove(xml); e != nil && !os.IsNotExist(e) {
			log.Printf("archiv: Klartext %s nicht gelöscht: %v", filepath.Base(xml), e)
		}
	}()

	a, err := im.Run.Run(ctx, signalExport(im.SignalBin, d.Pfad, xml, telefon, pass))
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
		if strings.Contains(a.Text, "No space left") {
			return fehler(fmt.Sprintf(saysArbeitPlatz, d.Rel), err)
		}
		return fehler(fmt.Sprintf(saysSignalUnlesbar, d.Rel), err)
	}
	if _, err := os.Stat(xml); err != nil {
		log.Printf("archiv: signalbackup-tools: %s", fehlerzeilen(schwaerzen(a.Text, pass)))
		return fehler(fmt.Sprintf(saysSignalUnlesbar, d.Rel), fmt.Errorf("kein XML: %w", err))
	}

	a, err = im.Run.Run(ctx, msgvaultImportSMS(im.Msgvault, innen.Home(), xml, telefon))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && a.Code != 0 {
		err = fmt.Errorf("msgvault import-synctech-sms Code %d: %s", a.Code, fehlerzeilen(a.Text))
	}
	if err != nil {
		return fehler(fmt.Sprintf(saysSignalUebernahme, d.Rel), err)
	}
	var z Zahlen
	if m := importiert.FindStringSubmatch(a.Text); m != nil {
		z.SMS, _ = strconv.Atoi(m[1])
		z.MMS, _ = strconv.Atoi(m[2])
		z.Anrufe, _ = strconv.Atoi(m[3])
		z.Anhaenge, _ = strconv.Atoi(m[4])
	}
	if err := os.Remove(d.Pfad); err != nil {
		im.Protokoll.Eingelesen("signal", d.Rel, z, im.now())
		return fehler(fmt.Sprintf(saysSignalWeg, d.Rel), err)
	}
	log.Printf("archiv: signal %s eingelesen: %d SMS, %d MMS, %d Anrufe, %d Anhänge", d.Rel, z.SMS, z.MMS, z.Anrufe, z.Anhaenge)
	im.Protokoll.Eingelesen("signal", d.Rel, z, im.now())
	return nil
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

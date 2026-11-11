package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// The Mailholer (owner, 2026-11-02, „A“): mail is fetched over IMAP while
// the vault is CLOSED, as the Signal backups are read. Every hour it logs
// in with the person's app password (in /data/schluessel, outside the
// vault), fetches what is new since the last round, read-only (EXAMINE,
// BODY.PEEK: nothing on the server changes, not even \Seen), and writes
// the new messages as ONE packet of a Maildir, age-encrypted to the
// person's public key, into the Briefkasten. When the vault opens, the
// Postbote hands the Maildir to `msgvault import-maildir`. The price, said
// on the page: whoever has root on the box can read the person's mail
// with the app password, which the provider can revoke at any time.
//
// Several mailboxes (owner, 2026-11-11): the person may give more than
// one; each is fetched the same way, one after the other, each with its
// own fetch state. And the household's document mailbox (the Paperless
// catch-all, its login from the box: Dokumentenpostfach): only the mails
// addressed to this person's <login>+doc…@<domain>, read-only as well.

const (
	saysMailAnmeldung = "Der Mail-Server hat die Anmeldung abgelehnt. Prüf Benutzername und App-Passwort und gib sie neu ein."
	saysMailServer    = "Der Mail-Server %s war nicht erreichbar. Das Archiv versucht es in einer Stunde wieder."
	saysMailPaket     = "Aus den neuen Mails ließ sich kein Paket schreiben. Das Archiv versucht es in einer Stunde wieder; hilft das nicht, sag dem Admin Bescheid."
	saysMailAbbruch   = "Das Abholen der Mails brach ab (%s). Was schon im Briefkasten liegt, bleibt; der Rest kommt in einer Stunde."
	saysDokuAnmeldung = "Das Dokumenten-Postfach des Haushalts hat die Anmeldung des Archivs abgelehnt. Sag dem Admin Bescheid: Das Passwort für dein Archiv ist veraltet."
)

var ErrMailAnmeldung = errors.New(saysMailAnmeldung)

// PostOrdner is one IMAP mailbox as LIST names it.
type PostOrdner struct {
	Name      string
	Trenner   string
	Attribute []string
}

// Mail is one fetched message, as the server holds it.
type Mail struct {
	UID   uint32
	Flags []string
	Datum time.Time
	Roh   []byte
}

// Postfach is what the Mailholer needs of an IMAP server; imapPostfach is
// the real one, the tests bring their own (and one test the real one
// against go-imap's in-memory server).
type Postfach interface {
	Ordner() ([]PostOrdner, error)
	Waehlen(name string) (uidValidity uint32, err error) // read-only
	NeueUIDs(nach uint32, an string) ([]uint32, error)   // UIDs greater than nach, ascending; an: only mail whose To holds it (IMAP's substring)
	Holen(uids []uint32, f func(Mail) error) error
	Abmelden() error
}

// imapAdresse makes host:port of what a person typed: 993 unless a port is given.
func imapAdresse(host string) (adresse, name string) {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "imaps://"), "imap://")
	host = strings.TrimSuffix(host, "/")
	if h, _, err := net.SplitHostPort(host); err == nil {
		return host, h
	}
	return net.JoinHostPort(host, "993"), host
}

// imapWurzeln: nil is the system's CAs; a test trusts its own server.
var imapWurzeln *x509.CertPool

// ImapVerbinden logs in over TLS (port 993; on 143 with STARTTLS). The
// password never travels in the clear: a server without TLS is refused.
func ImapVerbinden(ctx context.Context, z imapZugang) (Postfach, error) {
	adresse, name := imapAdresse(z.Host)
	if z.Port > 0 {
		adresse = net.JoinHostPort(name, strconv.Itoa(z.Port))
	}
	d := &net.Dialer{Timeout: 30 * time.Second}
	cfg := &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12, RootCAs: imapWurzeln}
	var c *client.Client
	var err error
	if strings.HasSuffix(adresse, ":143") {
		c, err = client.DialWithDialer(d, adresse)
		if err == nil {
			if ok, _ := c.SupportStartTLS(); !ok {
				c.Logout()
				return nil, errors.New("der Server bietet kein STARTTLS: das Passwort ginge im Klartext")
			}
			err = c.StartTLS(cfg)
		}
	} else {
		c, err = client.DialWithDialerTLS(d, adresse, cfg)
	}
	if err != nil {
		if c != nil {
			c.Logout()
		}
		return nil, err
	}
	c.Timeout = 5 * time.Minute
	if err := c.Login(z.Benutzer, z.Passwort); err != nil {
		c.Logout()
		log.Printf("archiv: IMAP-Anmeldung: %v", err)
		return nil, ErrMailAnmeldung
	}
	return &imapPostfach{c: c}, nil
}

type imapPostfach struct{ c *client.Client }

func (p *imapPostfach) Ordner() ([]PostOrdner, error) {
	ch := make(chan *imap.MailboxInfo, 64)
	fertig := make(chan error, 1)
	go func() { fertig <- p.c.List("", "*", ch) }()
	var out []PostOrdner
	for m := range ch {
		out = append(out, PostOrdner{Name: m.Name, Trenner: m.Delimiter, Attribute: m.Attributes})
	}
	return out, <-fertig
}

func (p *imapPostfach) Waehlen(name string) (uint32, error) {
	st, err := p.c.Select(name, true)
	if err != nil {
		return 0, err
	}
	return st.UidValidity, nil
}

func (p *imapPostfach) NeueUIDs(nach uint32, an string) ([]uint32, error) {
	k := imap.NewSearchCriteria()
	s := new(imap.SeqSet)
	s.AddRange(nach+1, 0) // nach+1:* — which always names the last message, even an older one
	k.Uid = s
	if an != "" {
		k.Header.Add("To", an)
	}
	uids, err := p.c.UidSearch(k)
	if err != nil {
		return nil, err
	}
	var out []uint32
	for _, u := range uids {
		if u > nach {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (p *imapPostfach) Holen(uids []uint32, f func(Mail) error) error {
	if len(uids) == 0 {
		return nil
	}
	s := new(imap.SeqSet)
	s.AddNum(uids...)
	teil := &imap.BodySectionName{Peek: true} // BODY.PEEK[]: the whole message, \Seen untouched
	ch := make(chan *imap.Message, 16)
	fertig := make(chan error, 1)
	go func() {
		fertig <- p.c.UidFetch(s, []imap.FetchItem{imap.FetchUid, imap.FetchFlags, imap.FetchInternalDate, teil.FetchItem()}, ch)
	}()
	var ferr error
	for m := range ch {
		if ferr != nil {
			continue // drain
		}
		r := m.GetBody(teil)
		if r == nil {
			continue // gone between SEARCH and FETCH
		}
		roh, err := io.ReadAll(r)
		if err != nil {
			ferr = err
			continue
		}
		ferr = f(Mail{UID: m.Uid, Flags: m.Flags, Datum: m.InternalDate, Roh: roh})
	}
	if err := <-fertig; err != nil {
		return err
	}
	return ferr
}

func (p *imapPostfach) Abmelden() error { return p.c.Logout() }

// ordnerWahl: a server with an „All Mail“ folder (\All, Gmail) is read
// there only — every message once, Spam and Trash left out; elsewhere
// every selectable folder but Junk and Trash.
func ordnerWahl(alle []PostOrdner) []PostOrdner {
	hat := func(o PostOrdner, attr string) bool {
		for _, a := range o.Attribute {
			if strings.EqualFold(a, attr) {
				return true
			}
		}
		return false
	}
	for _, o := range alle {
		if hat(o, imap.AllAttr) {
			return []PostOrdner{o}
		}
	}
	var out []PostOrdner
	for _, o := range alle {
		if hat(o, imap.NoSelectAttr) || hat(o, `\NonExistent`) || hat(o, imap.JunkAttr) || hat(o, imap.TrashAttr) {
			continue
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// maildirPfad is the folder's place in the packet's Maildir: one
// directory per level, each name made safe (no dot at the start, which
// Maildir++ reads as a separator; no slash; never empty, "." or "..").
// msgvault turns the levels into a label "Arbeit/Projekte".
func maildirPfad(o PostOrdner) string {
	teile := []string{o.Name}
	if o.Trenner != "" {
		teile = strings.Split(o.Name, o.Trenner)
	}
	var out []string
	for _, t := range teile {
		t = strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r < 0x20 {
				return '_'
			}
			return r
		}, strings.TrimSpace(t))
		if strings.HasPrefix(t, ".") {
			t = "_" + t[1:]
		}
		if t == "" {
			t = "_"
		}
		out = append(out, t)
	}
	return strings.Join(out, "/")
}

// maildirName is the message's unique file name with its flags
// (Maildir's info: S seen, R answered, F flagged, D draft, T deleted).
func maildirName(validity uint32, m Mail, summe string) string {
	var f []byte
	for _, x := range []struct {
		flag string
		b    byte
	}{{imap.DraftFlag, 'D'}, {imap.FlaggedFlag, 'F'}, {imap.AnsweredFlag, 'R'}, {imap.SeenFlag, 'S'}, {imap.DeletedFlag, 'T'}} {
		for _, g := range m.Flags {
			if strings.EqualFold(g, x.flag) {
				f = append(f, x.b)
				break
			}
		}
	}
	return fmt.Sprintf("%d.%d.%s:2,%s", validity, m.UID, summe[:16], f)
}

// mailStand is how far each folder was fetched. Keyed by an HMAC of the
// folder's name, so the file says nothing about the folders.
type mailStand map[string]ordnerStand

type ordnerStand struct {
	Validity uint32 `json:"validity"`
	UID      uint32 `json:"uid"`
}

// standDatei is a mailbox's fetch state: the first mailbox keeps the file
// it had before there were several.
func (h *Mailholer) standDatei(id string) string {
	if id == kontoErstes || id == "" {
		return filepath.Join(h.Gesehen.Dir, "mail-stand.json")
	}
	return filepath.Join(h.Gesehen.Dir, "mail-stand-"+id+".json")
}

func (h *Mailholer) standLaden(id string) mailStand {
	s := mailStand{}
	if b, err := os.ReadFile(h.standDatei(id)); err == nil {
		if err := json.Unmarshal(b, &s); err != nil {
			log.Printf("archiv: mail-stand: %v (beginne neu; doppelte Mails lässt das Archiv weg)", err)
			s = mailStand{}
		}
	}
	return s
}

func (h *Mailholer) standSpeichern(id string, s mailStand) error {
	b, _ := json.Marshal(s)
	if err := os.MkdirAll(h.Gesehen.Dir, 0o700); err != nil {
		return err
	}
	return schreibenAtomar(h.standDatei(id), b, 0o600)
}

// Mailholer fetches new mail into the Briefkasten, vault closed or open.
type Mailholer struct {
	Post       string // /data/briefkasten
	Empfaenger string // /data/briefkasten.pub
	Gesehen    *Gesehen
	Schluessel Schluessel
	Dokumente  *Dokumentenpostfach // the household's document mailbox; nil: none on this box
	Verbinden  func(context.Context, imapZugang) (Postfach, error)
	Protokoll  *Protokoll
	Takt       time.Duration // between rounds; one hour
	Buendel    int           // messages per FETCH; 100
	PaketBytes int64         // a packet is closed once it holds this much; 256 MiB
	Geaendert  func()
	Now        func() time.Time

	mu      sync.Mutex
	laeuft  bool
	gang    Gang
	falsch  map[string]bool // a mailbox whose login was refused: wait for a new password
	wechsel map[string]bool // a mailbox now names another one: its fetch state starts over
	anstoss chan struct{}
}

func (h *Mailholer) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Mailholer) geaendert() {
	if h.Geaendert != nil {
		h.Geaendert()
	}
}

func (h *Mailholer) kanal() chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.anstoss == nil {
		h.anstoss = make(chan struct{}, 1)
	}
	return h.anstoss
}

// Laeuft says whether mail is being fetched now.
func (h *Mailholer) Laeuft() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.laeuft
}

// Gang says how far the running round is; Was is empty when none runs.
func (h *Mailholer) Gang() Gang {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gang
}

func (h *Mailholer) gangSetzen(f func(*Gang)) {
	h.mu.Lock()
	f(&h.gang)
	h.mu.Unlock()
	h.geaendert()
}

// KontoGewechselt: the next round starts this mailbox's fetch state over
// (folder numbers of another mailbox say nothing; the HMAC set still keeps
// out what is already packed).
func (h *Mailholer) KontoGewechselt(id string) {
	h.mu.Lock()
	if h.wechsel == nil {
		h.wechsel = map[string]bool{}
	}
	h.wechsel[id] = true
	h.mu.Unlock()
}

// KontoWeg forgets a removed mailbox's fetch state. What it brought stays in the archive.
func (h *Mailholer) KontoWeg(id string) {
	if err := os.Remove(h.standDatei(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("archiv: Mail-Stand: %v", err)
	}
}

// NeuerSchluessel: new credentials; try at once, also after a refused login.
func (h *Mailholer) NeuerSchluessel() {
	h.mu.Lock()
	h.falsch = nil
	h.mu.Unlock()
	h.Protokoll.FehlerWeg("mail")
	select {
	case h.kanal() <- struct{}{}:
	default:
	}
}

// Lauf fetches until ctx ends: at once, then every Takt or when nudged.
func (h *Mailholer) Lauf(ctx context.Context) {
	takt := h.Takt
	if takt <= 0 {
		takt = time.Hour
	}
	an := h.kanal()
	for {
		h.Runde(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(takt):
		case <-an:
		}
	}
}

// kontoLauf is one mailbox in a round.
type kontoLauf struct {
	ID     string // its fetch state
	Konto  string // the packet's account: msgvault's identifier, and how the page names it
	Zugang imapZugang
	Doku   *Dokumentenpostfach // the document mailbox: one folder, only the person's mail
}

// konten are the mailboxes of a round: the person's, then the document mailbox.
func (h *Mailholer) konten() []kontoLauf {
	var out []kontoLauf
	k, err := imapKonten(h.Schluessel)
	if err != nil {
		log.Printf("archiv: IMAP-Zugang unlesbar: %v", err)
	}
	for _, z := range k {
		out = append(out, kontoLauf{ID: z.ID, Konto: z.Benutzer, Zugang: z})
	}
	if d := h.Dokumente; d != nil {
		if z, ok := d.zugang(); ok {
			out = append(out, kontoLauf{ID: kontoDokumente, Konto: d.Adresse(), Zugang: z, Doku: d})
		}
	}
	return out
}

// Runde fetches what is new in every folder of every mailbox once.
func (h *Mailholer) Runde(ctx context.Context) {
	empf, err := empfaengerLesen(h.Empfaenger)
	if err != nil {
		return // no vault made yet
	}
	konten := h.konten()
	if len(konten) == 0 {
		return // no mail account given
	}
	h.mu.Lock()
	if h.laeuft {
		h.mu.Unlock()
		return
	}
	h.laeuft = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.laeuft = false
		h.gang = Gang{}
		h.mu.Unlock()
		h.geaendert()
	}()
	for _, k := range konten {
		if ctx.Err() != nil {
			return
		}
		h.kontoRunde(ctx, empf, k)
	}
}

func (h *Mailholer) kontoRunde(ctx context.Context, empf age.Recipient, k kontoLauf) {
	h.mu.Lock()
	if h.falsch[k.ID] {
		h.mu.Unlock()
		return
	}
	h.gang = Gang{Was: k.Konto, Quelle: "mail", Seit: h.now()}
	wechsel := h.wechsel[k.ID]
	delete(h.wechsel, k.ID)
	h.mu.Unlock()
	if wechsel {
		h.KontoWeg(k.ID)
	}
	h.geaendert()

	verbinden := h.Verbinden
	if verbinden == nil {
		verbinden = ImapVerbinden
	}
	z := k.Zugang
	pf, err := verbinden(ctx, z)
	z.Passwort = ""
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrMailAnmeldung) {
			h.mu.Lock()
			if h.falsch == nil {
				h.falsch = map[string]bool{}
			}
			h.falsch[k.ID] = true // the same password fails again; wait for a new one
			h.mu.Unlock()
			satz := saysMailAnmeldung
			if k.Doku != nil {
				satz = saysDokuAnmeldung
			}
			h.Protokoll.Fehlschlag(Fehler{Am: h.now(), Quelle: "mail", Datei: k.Konto, Satz: satz})
			return
		}
		log.Printf("archiv: IMAP %s: %v", z.Host, err)
		h.Protokoll.Fehlschlag(Fehler{Am: h.now(), Quelle: "mail", Datei: k.Konto, Satz: fmt.Sprintf(saysMailServer, z.Host)})
		return
	}
	defer pf.Abmelden()
	g, err := h.holen(ctx, pf, empf, k)
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		h.Protokoll.Gepackt("mail", g)
		h.Protokoll.FehlerWegVon("mail", k.Konto)
		return
	}
	if g.Nachrichten > 0 {
		h.Protokoll.PaketMerken("mail", g) // without clearing this mailbox's error: a break in a row counts on
	}
	log.Printf("archiv: Mail holen: %v", err)
	satz := fmt.Sprintf(saysMailAbbruch, kurz(err))
	if errors.Is(err, errMailPaket) {
		satz = saysMailPaket
	}
	h.Protokoll.Fehlschlag(Fehler{Am: h.now(), Quelle: "mail", Datei: k.Konto, Satz: satz})
}

var errMailPaket = errors.New("paket")

func kurz(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// mailPaket is the packet being filled.
type mailPaket struct {
	tmp   string
	datei *os.File
	p     *PaketSchreiber
	bytes int64
	n     int
	macs  [][32]byte
}

// holen walks the folders and packs what is new. The fetch state of a
// folder is saved only once the messages up to it are in a packet in
// place: a crash fetches them again (and the HMAC set leaves them out).
func (h *Mailholer) holen(ctx context.Context, pf Postfach, empf age.Recipient, k kontoLauf) (Gepackt, error) {
	konto := k.Konto
	g := Gepackt{Am: h.now(), Sicherung: konto}
	buendel := h.Buendel
	if buendel <= 0 {
		buendel = 100
	}
	grenze := h.PaketBytes
	if grenze <= 0 {
		grenze = 256 << 20
	}
	var wahl []PostOrdner
	an := ""
	if k.Doku != nil {
		wahl, an = []PostOrdner{{Name: k.Doku.ordner(), Trenner: "/"}}, k.Doku.lokal() // the one folder Paperless reads, only the person's mail
	} else {
		alle, err := pf.Ordner()
		if err != nil {
			return g, fmt.Errorf("Ordner: %w", err)
		}
		wahl = ordnerWahl(alle)
	}
	gesehen, err := h.Gesehen.Laden("mail")
	if err != nil {
		return g, fmt.Errorf("%w: %v", errMailPaket, err)
	}
	stand := h.standLaden(k.ID)
	neu := mailStand{}
	for k, v := range stand {
		neu[k] = v
	}
	var pk *mailPaket
	defer func() {
		if pk != nil {
			pk.datei.Close()
			os.Remove(pk.tmp)
		}
	}()
	einwerfenJetzt := func() error {
		if pk == nil {
			return h.standSpeichern(k.ID, neu) // nothing new was fetched: the state may move on
		}
		err := pk.p.Schliessen()
		if err == nil {
			err = pk.datei.Sync()
		}
		if cerr := pk.datei.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(pk.tmp)
			pk = nil
			return fmt.Errorf("%w: %v", errMailPaket, err)
		}
		name, err := einwerfen(pk.tmp, h.Post, "mail", h.now())
		if err != nil {
			os.Remove(pk.tmp)
			pk = nil
			return fmt.Errorf("%w: %v", errMailPaket, err)
		}
		if err := h.Gesehen.Anhaengen("mail", pk.macs); err != nil {
			log.Printf("archiv: gesehen mail: %v (msgvault keeps one of each)", err)
		}
		for _, m := range pk.macs {
			gesehen[m] = true
		}
		g.Paket, g.Nachrichten = name, g.Nachrichten+pk.n
		log.Printf("archiv: mail → %d neue Mails in %s", pk.n, name)
		pk = nil
		return h.standSpeichern(k.ID, neu)
	}
	neuesPaket := func() error {
		if err := os.MkdirAll(h.Post, 0o700); err != nil {
			return err
		}
		tmp := filepath.Join(h.Post, ".neu-mail-"+zufall()+".age")
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		p, err := NeuesPaket(f, empf, Manifest{Quelle: "mail", Herkunft: "box", Konto: konto}, h.now().UTC())
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		pk = &mailPaket{tmp: tmp, datei: f, p: p}
		return nil
	}

	for _, o := range wahl {
		if ctx.Err() != nil {
			return g, ctx.Err()
		}
		mac, err := h.Gesehen.Mac("ordner\x00" + o.Name)
		if err != nil {
			return g, fmt.Errorf("%w: %v", errMailPaket, err)
		}
		schl := hex.EncodeToString(mac[:16])
		validity, err := pf.Waehlen(o.Name)
		if err != nil {
			log.Printf("archiv: IMAP-Ordner nicht lesbar (%v); übersprungen", err)
			continue
		}
		st := neu[schl]
		if st.Validity != validity {
			st = ordnerStand{Validity: validity} // the server renumbered: from the start (the HMAC set keeps out what is packed)
		}
		uids, err := pf.NeueUIDs(st.UID, an)
		if err != nil {
			return g, fmt.Errorf("SEARCH: %w", err)
		}
		h.gangSetzen(func(gg *Gang) { gg.Gesamt += len(uids) })
		pfad := maildirPfad(o)
		if k.Doku != nil {
			pfad = "Dokumente" // msgvault's label for these mails, whatever the household's folder is called
		}
		bis := h.Gang().Bis
		for i := 0; i < len(uids); i += buendel {
			if ctx.Err() != nil {
				return g, ctx.Err()
			}
			teil := uids[i:min(i+buendel, len(uids))]
			err := pf.Holen(teil, func(m Mail) error {
				if k.Doku != nil {
					if !k.Doku.Passt(m.Roh) {
						return nil // IMAP's TO is a substring: hanna+doc holds anna+doc; never another person's mail
					}
					m.Flags = nil // Paperless' marks, not the person's
				}
				summe := sha256.Sum256(m.Roh)
				hs := hex.EncodeToString(summe[:])
				d, err := h.Gesehen.Mac("v1\x00" + hs)
				if err != nil {
					return fmt.Errorf("%w: %v", errMailPaket, err)
				}
				if gesehen[d] {
					return nil
				}
				if pk == nil {
					if err := neuesPaket(); err != nil {
						return fmt.Errorf("%w: %v", errMailPaket, err)
					}
				}
				gesehen[d] = true // also within this round: the same message in two folders goes in once
				name := "maildir/" + pfad + "/cur/" + maildirName(validity, m, hs)
				if err := pk.p.Datei(name, m.Roh); err != nil {
					return fmt.Errorf("%w: %v", errMailPaket, err)
				}
				pk.bytes += int64(len(m.Roh))
				pk.n++
				if m.Datum.After(bis) {
					bis = m.Datum
				}
				pk.macs = append(pk.macs, d)
				return nil
			})
			if err != nil {
				if errors.Is(err, errMailPaket) {
					return g, err
				}
				return g, fmt.Errorf("FETCH: %w", err)
			}
			st.UID = teil[len(teil)-1]
			neu[schl] = st
			h.gangSetzen(func(gg *Gang) { gg.Fertig += len(teil); gg.Bis = bis }) // once per bundle: each change rewrites the status file
			if pk == nil {
				if err := h.standSpeichern(k.ID, neu); err != nil {
					return g, fmt.Errorf("%w: %v", errMailPaket, err)
				}
			} else if pk.bytes >= grenze {
				if err := einwerfenJetzt(); err != nil {
					return g, err
				}
			}
		}
		neu[schl] = st
	}
	return g, einwerfenJetzt()
}

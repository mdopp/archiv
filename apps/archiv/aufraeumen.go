package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/commands"
)

// Deleting old mail at the provider (owner, 2026-11-11): per mailbox, off
// by default. The person says „älter als“ (default 24 months); the archive
// first counts (a dry run, nothing changes) and shows „1.234 Mails älter
// als 2 Jahre, alle im Archiv – jetzt beim Anbieter löschen?“; only the
// person's tap starts the first deletion, then it runs monthly while the
// vault is open. A mail is deleted only when all of this holds:
//
//   - older than the person's limit (the server's INTERNALDATE),
//   - not flagged (a star), not a draft, not already marked deleted, not
//     in the Drafts, Junk or Trash folder,
//   - proven in the archive: msgvault holds a message with the same
//     Message-ID whose content id ends in the SHA-256 of the very bytes
//     the server sends now (import-maildir's "maildir-<sha256>"). A mail
//     without a Message-ID, one fetched but not imported yet, one msgvault
//     never saw: none of these is proven, so none is deleted.
//
// The proof needs msgvault, which answers only while the vault is open:
// with the vault closed, or msgvault not answering, nothing is deleted.
// The settings and the counts live in the vault (Innen.MailAufraeumen).
// The fetch stays read-only (EXAMINE); only this uses SELECT. Gmail (a
// server with an „All Mail“ folder) expects a delete as a MOVE into its
// Trash, which Gmail empties after 30 days; elsewhere the message gets
// \Deleted and a UID EXPUNGE of exactly these messages. The document
// mailbox is never touched.

const (
	aufraeumenMonate = 24                  // the default „älter als“
	aufraeumenTakt   = 30 * 24 * time.Hour // after the first run: monthly
	probeAlter       = 24 * time.Hour      // a dry run older than this is counted again

	saysAufrZu        = "Das Archiv ist abgeschlossen. Ohne den Abgleich mit dem Archiv löscht es nichts beim Anbieter."
	saysAufrBeleg     = "Das Archiv konnte nicht prüfen, welche Mails es schon hat. Gelöscht wurde nichts; es versucht es in einer Stunde wieder."
	saysAufrServer    = "Der Mail-Server %s war nicht erreichbar. Gelöscht wurde nichts; das Archiv versucht es in einer Stunde wieder."
	saysAufrAnmeldung = "Der Mail-Server hat die Anmeldung abgelehnt. Gelöscht wurde nichts. Gib bei diesem Postfach ein neues App-Passwort ein."
	saysAufrKorb      = "Bei diesem Anbieter fand das Archiv keinen Papierkorb, in den es Mails verschieben darf. Gelöscht wurde nichts."
	saysAufrFremd     = "In einem Ordner liegen Mails, die schon zum Löschen markiert sind, und der Server kann nicht einzeln löschen. Damit davon nichts mitgeht, hat das Archiv dort nichts gelöscht."
	saysAufrAbbruch   = "Das Löschen brach ab (%s). Gelöscht ist nur, was sicher im Archiv ist; der Rest folgt beim nächsten Mal."
	saysAufrKann      = "Dieses Postfach kann das Archiv nicht aufräumen."
)

var (
	errBeleg        = errors.New("beleg")
	errKeinKorb     = errors.New("kein Papierkorb")
	errFremdWeg     = errors.New("fremde gelöschte Mails im Ordner")
	errNichtFrei    = errors.New("nicht freigebbar")
	errMonateFalsch = errors.New("monate")
)

// Durchlauf is one count or deletion.
type Durchlauf struct {
	Am        time.Time `json:"am"`
	Monate    int       `json:"monate"`
	Alt       int       `json:"alt"`       // older than Monate, not flagged, not a draft
	ImArchiv  int       `json:"im_archiv"` // of those, proven in the archive
	Geloescht int       `json:"geloescht"` // deleted at the provider (Gmail: moved to its Trash)
	Fehler    string    `json:"fehler,omitempty"`
}

// Aufraeumen are one mailbox's settings and what came of them.
type Aufraeumen struct {
	An       bool       `json:"an"`
	Monate   int        `json:"monate"`
	Freigabe *time.Time `json:"freigabe,omitempty"` // the person's tap on „jetzt löschen“, for this Monate
	Probe    *Durchlauf `json:"probe,omitempty"`    // the dry run the page asks with
	Letzter  *Durchlauf `json:"letzter,omitempty"`  // the last deletion
}

func (i Innen) MailAufraeumen() string { return filepath.Join(i.Wurzel, "mail-aufraeumen.json") }

// ArchivBeleg says what msgvault holds: for each Message-ID (without its
// angle brackets), the content ids of its messages. An error means:
// nothing is proven.
type ArchivBeleg interface {
	MailsImArchiv(ctx context.Context, ids []string) (map[string][]string, error)
}

// Kopf is what a dry run reads of a message before its body.
type Kopf struct {
	UID       uint32
	Flags     []string
	Datum     time.Time // INTERNALDATE
	MessageID string    // without angle brackets; "" when it has none
}

// LoeschPostfach is what deleting needs of a server, beyond the fetch.
type LoeschPostfach interface {
	Postfach
	Schreibend(name string) (uidValidity uint32, err error) // SELECT
	Alte(vor time.Time) ([]uint32, error)                   // before vor, not flagged, not a draft, not marked deleted
	Koepfe(uids []uint32, f func(Kopf) error) error
	Loeschen(uids []uint32, papierkorb string) error // papierkorb "": \Deleted + UID EXPUNGE; else MOVE there
}

type Aufraeumer struct {
	Schluessel Schluessel
	Beleg      ArchivBeleg
	Verbinden  func(context.Context, imapZugang) (Postfach, error)
	Takt       time.Duration // how often it looks whether a run is due; an hour
	Buendel    int           // messages per step; 200
	Geaendert  func()
	Now        func() time.Time

	datei   sync.Mutex // the settings file
	mu      sync.Mutex
	laeuft  string // the mailbox being counted or cleaned
	anstoss chan struct{}
}

func (a *Aufraeumer) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Aufraeumer) geaendert() {
	if a.Geaendert != nil {
		a.Geaendert()
	}
}

func (a *Aufraeumer) kanal() chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.anstoss == nil {
		a.anstoss = make(chan struct{}, 1)
	}
	return a.anstoss
}

func (a *Aufraeumer) anstossen() {
	select {
	case a.kanal() <- struct{}{}:
	default:
	}
}

// Laeuft names the mailbox being worked on, "" when none.
func (a *Aufraeumer) Laeuft() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.laeuft
}

func aufrLaden(innen Innen) (map[string]*Aufraeumen, error) {
	out := map[string]*Aufraeumen{}
	b, err := os.ReadFile(innen.MailAufraeumen())
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func aufrSpeichern(innen Innen, s map[string]*Aufraeumen) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return schreibenAtomar(innen.MailAufraeumen(), b, 0o600)
}

// Einstellungen are every mailbox's settings (open vault only).
func (a *Aufraeumer) Einstellungen(innen Innen) map[string]Aufraeumen {
	a.datei.Lock()
	defer a.datei.Unlock()
	s, err := aufrLaden(innen)
	if err != nil {
		log.Printf("archiv: Aufräumen-Einstellungen: %v", err)
	}
	out := map[string]Aufraeumen{}
	for k, v := range s {
		out[k] = *v
	}
	return out
}

// Einstellen switches one mailbox's cleanup on or off, with its limit.
// Another limit, or switching on, asks again: a new dry run, a new tap.
func (a *Aufraeumer) Einstellen(innen Innen, id string, an bool, monate int) error {
	if monate < 1 || monate > 600 {
		return errMonateFalsch
	}
	a.datei.Lock()
	s, err := aufrLaden(innen)
	if err != nil {
		a.datei.Unlock()
		return err
	}
	e := s[id]
	if e == nil {
		e = &Aufraeumen{}
		s[id] = e
	}
	if !an || !e.An || e.Monate != monate {
		e.Freigabe, e.Probe = nil, nil
	}
	e.An, e.Monate = an, monate
	err = aufrSpeichern(innen, s)
	a.datei.Unlock()
	if err == nil && an {
		a.anstossen()
	}
	return err
}

// Freigeben is the person's tap: the dry run they saw, for the limit they
// set, without an error. From now on it deletes, the first time at once.
func (a *Aufraeumer) Freigeben(innen Innen, id string) error {
	a.datei.Lock()
	s, err := aufrLaden(innen)
	if err != nil {
		a.datei.Unlock()
		return err
	}
	e := s[id]
	if e == nil || !e.An || e.Probe == nil || e.Probe.Fehler != "" || e.Probe.Monate != e.Monate || e.Probe.ImArchiv == 0 {
		a.datei.Unlock()
		return errNichtFrei
	}
	jetzt := a.now()
	e.Freigabe = &jetzt
	err = aufrSpeichern(innen, s)
	a.datei.Unlock()
	if err == nil {
		a.anstossen()
	}
	return err
}

// Vergessen drops a removed mailbox's settings.
func (a *Aufraeumer) Vergessen(innen Innen, id string) {
	a.datei.Lock()
	defer a.datei.Unlock()
	s, err := aufrLaden(innen)
	if err != nil || s[id] == nil {
		return
	}
	delete(s, id)
	if err := aufrSpeichern(innen, s); err != nil {
		log.Printf("archiv: Aufräumen-Einstellungen: %v", err)
	}
}

// faellig: what is due for a mailbox now — a dry run (echt false) while
// the person has not tapped, the deletion once they have and monthly after.
func faellig(e *Aufraeumen, jetzt time.Time) (echt, ok bool) {
	switch {
	case e == nil || !e.An:
		return false, false
	case e.Freigabe == nil:
		p := e.Probe
		return false, p == nil || p.Monate != e.Monate || p.Fehler != "" || jetzt.Sub(p.Am) >= probeAlter
	default:
		l := e.Letzter
		return true, l == nil || l.Am.Before(*e.Freigabe) || l.Fehler != "" || jetzt.Sub(l.Am) >= aufraeumenTakt
	}
}

// Lauf runs while the vault is open: at once, then every Takt or when nudged.
func (a *Aufraeumer) Lauf(ctx context.Context, innen Innen) {
	takt := a.Takt
	if takt <= 0 {
		takt = time.Hour
	}
	an := a.kanal()
	for {
		a.Runde(ctx, innen)
		select {
		case <-ctx.Done():
			return
		case <-time.After(takt):
		case <-an:
		}
	}
}

// Runde does what is due for each of the person's mailboxes.
func (a *Aufraeumer) Runde(ctx context.Context, innen Innen) {
	konten, err := imapKonten(a.Schluessel)
	if err != nil || len(konten) == 0 {
		return
	}
	for _, z := range konten {
		if ctx.Err() != nil {
			return
		}
		a.datei.Lock()
		s, err := aufrLaden(innen)
		a.datei.Unlock()
		if err != nil {
			log.Printf("archiv: Aufräumen-Einstellungen: %v", err)
			return
		}
		e := s[z.ID]
		echt, ok := faellig(e, a.now())
		if !ok {
			continue
		}
		monate := e.Monate
		a.mu.Lock()
		a.laeuft = z.Anzeige()
		a.mu.Unlock()
		a.geaendert()
		d := a.durchlauf(ctx, z, monate, echt)
		a.mu.Lock()
		a.laeuft = ""
		a.mu.Unlock()
		if ctx.Err() != nil && d.Geloescht == 0 {
			a.geaendert()
			return // the vault closed meanwhile: nothing to record
		}
		a.datei.Lock()
		if s, err := aufrLaden(innen); err == nil {
			if e := s[z.ID]; e != nil && e.An && e.Monate == monate && (e.Freigabe != nil) == echt {
				if echt {
					e.Letzter = &d
				} else {
					e.Probe = &d
				}
				if err := aufrSpeichern(innen, s); err != nil {
					log.Printf("archiv: Aufräumen-Einstellungen: %v", err)
				}
			}
		}
		a.datei.Unlock()
		if echt {
			log.Printf("archiv: alte Mails beim Anbieter: %d gelöscht, %d älter, %d im Archiv (%s)", d.Geloescht, d.Alt, d.ImArchiv, d.Fehler)
		}
		a.geaendert()
	}
}

func (a *Aufraeumer) durchlauf(ctx context.Context, z imapZugang, monate int, echt bool) Durchlauf {
	d := Durchlauf{Am: a.now(), Monate: monate}
	verbinden := a.Verbinden
	if verbinden == nil {
		verbinden = ImapVerbinden
	}
	pf, err := verbinden(ctx, z)
	if err != nil {
		if errors.Is(err, ErrMailAnmeldung) {
			d.Fehler = saysAufrAnmeldung
		} else {
			d.Fehler = fmt.Sprintf(saysAufrServer, z.Host)
		}
		return d
	}
	defer pf.Abmelden()
	lp, ok := pf.(LoeschPostfach)
	if !ok {
		d.Fehler = saysAufrKann
		return d
	}
	if err := a.pruefen(ctx, lp, monate, echt, &d); err != nil {
		switch {
		case errors.Is(err, errNichtBereit):
			d.Fehler = saysAufrZu
		case errors.Is(err, errBeleg):
			d.Fehler = saysAufrBeleg
		case errors.Is(err, errKeinKorb):
			d.Fehler = saysAufrKorb
		case errors.Is(err, errFremdWeg):
			d.Fehler = saysAufrFremd
		default:
			d.Fehler = fmt.Sprintf(saysAufrAbbruch, kurz(err))
		}
		log.Printf("archiv: alte Mails beim Anbieter: %v", err)
	}
	return d
}

func hatAttribut(o PostOrdner, attr string) bool {
	for _, a := range o.Attribute {
		if strings.EqualFold(a, attr) {
			return true
		}
	}
	return false
}

// loeschbar: flags that keep a message — a star, a draft, a delete mark.
func loeschbar(flags []string) bool {
	for _, f := range flags {
		if strings.EqualFold(f, imap.FlaggedFlag) || strings.EqualFold(f, imap.DraftFlag) || strings.EqualFold(f, imap.DeletedFlag) {
			return false
		}
	}
	return true
}

// messageID is a Message-ID as msgvault keeps it: without its angle
// brackets, "" when there is none or it is malformed.
func messageID(wert string) string {
	id := strings.TrimSpace(wert)
	if strings.ContainsAny(id, "<>") {
		i := strings.IndexByte(id, '<')
		j := strings.IndexByte(id[i+1:], '>')
		if i < 0 || j < 0 {
			return ""
		}
		id = id[i+1 : i+1+j]
	}
	if id == "" || strings.TrimSpace(id) != id {
		return ""
	}
	for _, r := range id {
		if r <= ' ' || r > '~' || r == '\'' || r == '\\' || r == '"' { // also what the SQL literal could not carry
			return ""
		}
	}
	return id
}

// kopfID reads the Message-ID of a raw message.
func kopfID(roh []byte) string {
	m, err := mail.ReadMessage(bytes.NewReader(roh))
	if err != nil {
		return ""
	}
	return messageID(m.Header.Get("Message-Id"))
}

// pruefen walks the folders: counts, proves and (echt) deletes.
func (a *Aufraeumer) pruefen(ctx context.Context, pf LoeschPostfach, monate int, echt bool, d *Durchlauf) error {
	alle, err := pf.Ordner()
	if err != nil {
		return fmt.Errorf("Ordner: %w", err)
	}
	papierkorb := ""
	for _, o := range alle {
		if hatAttribut(o, imap.AllAttr) { // Gmail: a delete is a move to its Trash
			for _, t := range alle {
				if hatAttribut(t, imap.TrashAttr) {
					papierkorb = t.Name
				}
			}
			if papierkorb == "" {
				return errKeinKorb
			}
		}
	}
	buendel := a.Buendel
	if buendel <= 0 {
		buendel = 200
	}
	vor := a.now().AddDate(0, -monate, 0)
	for _, o := range ordnerWahl(alle) {
		if hatAttribut(o, imap.DraftsAttr) {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if echt {
			_, err = pf.Schreibend(o.Name)
		} else {
			_, err = pf.Waehlen(o.Name) // counting changes nothing: EXAMINE
		}
		if err != nil {
			log.Printf("archiv: IMAP-Ordner nicht lesbar (%v); übersprungen", err)
			continue
		}
		uids, err := pf.Alte(vor)
		if err != nil {
			return fmt.Errorf("SEARCH: %w", err)
		}
		for i := 0; i < len(uids); i += buendel {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			teil := uids[i:min(i+buendel, len(uids))]
			ids := map[uint32]string{}
			err := pf.Koepfe(teil, func(k Kopf) error {
				if !loeschbar(k.Flags) || k.Datum.IsZero() || !k.Datum.Before(vor) {
					return nil
				}
				d.Alt++
				if k.MessageID != "" {
					ids[k.UID] = k.MessageID
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("FETCH: %w", err)
			}
			if len(ids) == 0 {
				continue
			}
			var liste []string
			gesehen := map[string]bool{}
			for _, id := range ids {
				if !gesehen[id] {
					gesehen[id], liste = true, append(liste, id)
				}
			}
			beleg, err := a.Beleg.MailsImArchiv(ctx, liste)
			if errors.Is(err, errNichtBereit) {
				return err
			}
			if err != nil {
				return fmt.Errorf("%w: %v", errBeleg, err)
			}
			var pruefen []uint32
			for _, u := range teil {
				if id, da := ids[u]; da && len(beleg[id]) > 0 {
					pruefen = append(pruefen, u)
				}
			}
			var bewiesen []uint32
			err = pf.Holen(pruefen, func(m Mail) error {
				id := ids[m.UID]
				if !loeschbar(m.Flags) || kopfID(m.Roh) != id {
					return nil // changed since the header was read: left alone
				}
				summe := sha256.Sum256(m.Roh)
				hs := "-" + hex.EncodeToString(summe[:])
				for _, sid := range beleg[id] {
					if strings.HasSuffix(sid, hs) {
						bewiesen = append(bewiesen, m.UID)
						break
					}
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("FETCH: %w", err)
			}
			d.ImArchiv += len(bewiesen)
			if echt && len(bewiesen) > 0 {
				if err := pf.Loeschen(bewiesen, papierkorb); err != nil {
					return err
				}
				d.Geloescht += len(bewiesen)
			}
		}
	}
	return nil
}

// --- the real server

func (p *imapPostfach) Schreibend(name string) (uint32, error) {
	st, err := p.c.Select(name, false)
	if err != nil {
		return 0, err
	}
	return st.UidValidity, nil
}

func (p *imapPostfach) Alte(vor time.Time) ([]uint32, error) {
	k := imap.NewSearchCriteria()
	k.Before = vor
	k.WithoutFlags = []string{imap.FlaggedFlag, imap.DraftFlag, imap.DeletedFlag}
	return p.c.UidSearch(k)
}

func (p *imapPostfach) Koepfe(uids []uint32, f func(Kopf) error) error {
	if len(uids) == 0 {
		return nil
	}
	s := new(imap.SeqSet)
	s.AddNum(uids...)
	teil := &imap.BodySectionName{Peek: true, BodyPartName: imap.BodyPartName{Specifier: imap.HeaderSpecifier, Fields: []string{"Message-Id"}}}
	ch := make(chan *imap.Message, 64)
	fertig := make(chan error, 1)
	go func() {
		fertig <- p.c.UidFetch(s, []imap.FetchItem{imap.FetchUid, imap.FetchFlags, imap.FetchInternalDate, teil.FetchItem()}, ch)
	}()
	var ferr error
	for m := range ch {
		if ferr != nil {
			continue // drain
		}
		k := Kopf{UID: m.Uid, Flags: m.Flags, Datum: m.InternalDate}
		if r := m.GetBody(teil); r != nil {
			b, _ := io.ReadAll(r)
			k.MessageID = kopfID(append(b, '\r', '\n'))
		}
		ferr = f(k)
	}
	if err := <-fertig; err != nil {
		return err
	}
	return ferr
}

// uidExpunge is UID EXPUNGE (UIDPLUS, RFC 4315): only these messages.
type uidExpunge struct{ s *imap.SeqSet }

func (u uidExpunge) Command() *imap.Command {
	return &imap.Command{Name: "EXPUNGE", Arguments: []interface{}{u.s}}
}

func (p *imapPostfach) Loeschen(uids []uint32, papierkorb string) error {
	if len(uids) == 0 {
		return nil
	}
	s := new(imap.SeqSet)
	s.AddNum(uids...)
	if papierkorb != "" {
		if ok, _ := p.c.Support("MOVE"); !ok {
			return errKeinKorb // go-imap would fall back to a plain EXPUNGE, which takes every marked message
		}
		return p.c.UidMove(s, papierkorb)
	}
	if err := p.c.UidStore(s, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil); err != nil {
		return err
	}
	if ok, _ := p.c.Support("UIDPLUS"); ok {
		st, err := p.c.Execute(&commands.Uid{Cmd: uidExpunge{s}}, nil)
		if err != nil {
			return err
		}
		return st.Err()
	}
	// Without UIDPLUS, EXPUNGE takes every message marked deleted in the
	// folder: only when the marked ones are exactly ours.
	k := imap.NewSearchCriteria()
	k.WithFlags = []string{imap.DeletedFlag}
	markiert, err := p.c.UidSearch(k)
	unsere := map[uint32]bool{}
	for _, u := range uids {
		unsere[u] = true
	}
	fremd := err != nil
	for _, u := range markiert {
		fremd = fremd || !unsere[u]
	}
	if fremd {
		if err := p.c.UidStore(s, imap.FormatFlagsOp(imap.RemoveFlags, true), []interface{}{imap.DeletedFlag}, nil); err != nil {
			log.Printf("archiv: Löschmarke zurücknehmen: %v", err)
		}
		return errFremdWeg
	}
	return p.c.Expunge(nil)
}

// --- the proof, from msgvault

// MailsImArchiv asks msgvault (its SQL over the analytics cache, read
// only) which messages it holds with these Message-IDs. A message the
// cache does not have yet is not there: it is proven next time.
func (m *MsgvaultAPI) MailsImArchiv(ctx context.Context, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	for i := 0; i < len(ids); i += 200 {
		teil := ids[i:min(i+200, len(ids))]
		var lit []string
		for _, id := range teil {
			if messageID(id) != id {
				return nil, fmt.Errorf("Message-ID %q", id)
			}
			lit = append(lit, "'"+id+"'")
		}
		sql := "SELECT CAST(rfc822_message_id AS VARCHAR) AS mid, CAST(source_message_id AS VARCHAR) AS sid FROM messages " +
			"WHERE deleted_at IS NULL AND rfc822_message_id IN (" + strings.Join(lit, ", ") + ")"
		zeilen, err := m.abfragen(ctx, sql, "mid", "sid")
		if err != nil {
			return nil, err
		}
		for _, z := range zeilen {
			if z[0] != "" && z[1] != "" {
				out[z[0]] = append(out[z[0]], z[1])
			}
		}
	}
	return out, nil
}

// MailNummer is msgvault's id of the message with this Message-ID, for
// its page /messages/<id>; 0 when the archive does not hold it.
func (m *MsgvaultAPI) MailNummer(ctx context.Context, id string) (int64, error) {
	if messageID(id) != id {
		return 0, nil
	}
	zeilen, err := m.abfragen(ctx, "SELECT CAST(id AS VARCHAR) AS id FROM messages WHERE deleted_at IS NULL AND rfc822_message_id = '"+id+"' ORDER BY id LIMIT 1", "id")
	if err != nil || len(zeilen) == 0 {
		return 0, err
	}
	var n int64
	_, err = fmt.Sscan(zeilen[0][0], &n)
	return n, err
}

// abfragen runs a read-only query and returns the named columns as text.
// A cache still being built (202, 503) is an error: nothing is proven.
func (m *MsgvaultAPI) abfragen(ctx context.Context, sql string, spalten ...string) ([][]string, error) {
	var r struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}
	if err := m.rufe(ctx, http.MethodPost, "/api/v1/query", nil, map[string]string{"sql": sql}, &r); err != nil {
		return nil, err
	}
	if r.Columns == nil {
		return nil, errors.New("msgvault baut seinen Index noch")
	}
	idx := make([]int, len(spalten))
	for i, s := range spalten {
		idx[i] = -1
		for j, c := range r.Columns {
			if c == s {
				idx[i] = j
			}
		}
		if idx[i] < 0 {
			return nil, fmt.Errorf("msgvault-Abfrage ohne Spalte %q", s)
		}
	}
	var out [][]string
	for _, z := range r.Rows {
		zeile := make([]string, len(spalten))
		for i, j := range idx {
			if j < len(z) && z[j] != nil {
				zeile[i] = fmt.Sprint(z[j])
			}
		}
		out = append(out, zeile)
	}
	return out, nil
}

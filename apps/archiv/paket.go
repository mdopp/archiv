package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
)

// A Paket (packet) carries new messages of one source from the inbox to
// the archive while the vault is closed: age-encrypted to the person's
// public key, so whoever writes it (the box tonight, the phone later)
// cannot read it back, and only the open vault (the private key) can.
// The format is source-neutral and versioned; README.md "Paket format"
// is its specification, and this file is its reference.

const (
	paketFormat  = "archiv-paket"
	paketVersion = 1
)

// Manifest is the packet's first entry, manifest.json.
type Manifest struct {
	Format    string `json:"format"`              // "archiv-paket"
	Version   int    `json:"version"`             // 1
	Quelle    string `json:"quelle"`              // "signal", later "whatsapp", …
	Erstellt  string `json:"erstellt"`            // RFC 3339, UTC
	Herkunft  string `json:"herkunft"`            // "box" or "telefon"
	Sicherung string `json:"sicherung,omitempty"` // the backup it was made from, if any
	Konto     string `json:"konto,omitempty"`     // mail: the account (the IMAP user) the messages belong to
}

// Anhang is one attachment of a message; Datei names its tar entry.
type Anhang struct {
	Datei string `json:"datei"`          // "anhaenge/<name>"
	Typ   string `json:"typ"`            // MIME type
	Name  string `json:"name,omitempty"` // the original file name, if known
}

// Nachricht is one message, one line of nachrichten.jsonl.
type Nachricht struct {
	ID         string   `json:"id"`                 // stable for this message within its source, across packets
	Zeit       int64    `json:"zeit"`               // ms since 1970, UTC
	Richtung   string   `json:"richtung"`           // "ein" (received) or "aus" (sent by the person)
	Absender   string   `json:"absender,omitempty"` // E.164 of the sender of a received message
	Teilnehmer []string `json:"teilnehmer"`         // E.164 of everyone in the conversation but the person, sorted
	Name       string   `json:"name,omitempty"`     // the contact's or the group's name
	Text       string   `json:"text,omitempty"`
	Gelesen    bool     `json:"gelesen,omitempty"`
	QuellID    string   `json:"quell_id,omitempty"` // the source's own message id, if it has one
	Anhaenge   []Anhang `json:"anhaenge,omitempty"`
}

// identitaet is what makes a message the same message in the next
// backup: never the packet's choice of file names.
func (n Nachricht) identitaet(anhangSummen []string) string {
	t := append([]string(nil), n.Teilnehmer...)
	sort.Strings(t)
	return strings.Join([]string{"v1", strconv.FormatInt(n.Zeit, 10), n.Richtung, n.Absender, strings.Join(t, ","), n.Text, strings.Join(anhangSummen, ",")}, "\x00")
}

// --- writing

// PaketSchreiber writes one packet as a stream: manifest first, then
// attachments as they come, the messages last (held in memory: text only).
type PaketSchreiber struct {
	ziel     io.WriteCloser // age
	tar      *tar.Writer
	jsonl    bytes.Buffer
	n        int
	anhaenge int
	jetzt    time.Time
}

func NeuesPaket(w io.Writer, empfaenger age.Recipient, m Manifest, jetzt time.Time) (*PaketSchreiber, error) {
	enc, err := age.Encrypt(w, empfaenger)
	if err != nil {
		return nil, err
	}
	p := &PaketSchreiber{ziel: enc, tar: tar.NewWriter(enc), jetzt: jetzt}
	m.Format, m.Version = paketFormat, paketVersion
	if m.Erstellt == "" {
		m.Erstellt = jetzt.UTC().Format(time.RFC3339)
	}
	b, _ := json.Marshal(m)
	if err := p.eintrag("manifest.json", b); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *PaketSchreiber) eintrag(name string, b []byte) error {
	if err := p.tar.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: p.jetzt, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return err
	}
	_, err := p.tar.Write(b)
	return err
}

// Anhang stores one attachment and returns its entry for the message.
func (p *PaketSchreiber) Anhang(typ, name string, daten []byte) (Anhang, error) {
	p.anhaenge++
	a := Anhang{Datei: fmt.Sprintf("anhaenge/%06d", p.anhaenge), Typ: typ, Name: name}
	return a, p.eintrag(a.Datei, daten)
}

// Datei stores a raw file under its entry name: a mail packet's
// maildir/<folder>/cur/<name>, one message each (README "Paket format").
func (p *PaketSchreiber) Datei(name string, daten []byte) error {
	if !maildirEintrag(name) {
		return fmt.Errorf("Eintrag %q", name)
	}
	p.n++
	return p.eintrag(name, daten)
}

// maildirEintrag: maildir/…/cur/<name>, clean, no level empty or
// beginning with a dot (so never "..", and never a Maildir++ separator).
func maildirEintrag(name string) bool {
	if !strings.HasPrefix(name, "maildir/") || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") {
		return false
	}
	teile := strings.Split(name, "/")
	if len(teile) < 4 || teile[len(teile)-2] != "cur" {
		return false
	}
	for _, t := range teile {
		if t == "" || strings.HasPrefix(t, ".") {
			return false
		}
	}
	return true
}

func (p *PaketSchreiber) Nachricht(n Nachricht) error {
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	p.jsonl.Write(b)
	p.jsonl.WriteByte('\n')
	p.n++
	return nil
}

func (p *PaketSchreiber) Anzahl() int { return p.n }

// Schliessen writes the messages and ends the tar and the age stream.
func (p *PaketSchreiber) Schliessen() error {
	if err := p.eintrag("nachrichten.jsonl", p.jsonl.Bytes()); err != nil {
		return err
	}
	if err := p.tar.Close(); err != nil {
		return err
	}
	return p.ziel.Close()
}

// --- reading

var ErrPaketFormat = errors.New("kein Paket in einem bekannten Format")

// PaketLesen decrypts a packet and hands each entry to the callbacks:
// the manifest first (checked), then attachments to anhang (by their
// base name) and a mail packet's Maildir files (by their whole entry
// name, maildir/…, checked), then every
// message to nachricht. Attachments may come before or after the
// messages, so a reader that needs both keeps the attachments
// (Postbote: in the open vault).
func PaketLesen(r io.Reader, id age.Identity, anhang func(name string, r io.Reader) error, nachricht func(Nachricht) error) (Manifest, error) {
	var m Manifest
	dec, err := age.Decrypt(r, id)
	if err != nil {
		return m, fmt.Errorf("entschlüsseln: %w", err)
	}
	tr := tar.NewReader(dec)
	erst := true
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, err
		}
		if erst {
			if h.Name != "manifest.json" {
				return m, ErrPaketFormat
			}
			if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil {
				return m, ErrPaketFormat
			}
			if m.Format != paketFormat || m.Version != paketVersion {
				return m, fmt.Errorf("%w: %s %d", ErrPaketFormat, m.Format, m.Version)
			}
			erst = false
			continue
		}
		switch {
		case h.Name == "nachrichten.jsonl":
			s := bufio.NewScanner(tr)
			s.Buffer(make([]byte, 1<<20), 64<<20)
			for s.Scan() {
				if len(bytes.TrimSpace(s.Bytes())) == 0 {
					continue
				}
				var n Nachricht
				if err := json.Unmarshal(s.Bytes(), &n); err != nil {
					return m, fmt.Errorf("nachrichten.jsonl: %w", err)
				}
				if err := nachricht(n); err != nil {
					return m, err
				}
			}
			if err := s.Err(); err != nil {
				return m, err
			}
		case strings.HasPrefix(h.Name, "maildir/") && h.Typeflag == tar.TypeReg:
			if !maildirEintrag(h.Name) {
				return m, fmt.Errorf("Mail %q", h.Name)
			}
			if err := anhang(h.Name, tr); err != nil { // the whole entry name: it has a slash, an attachment never does
				return m, err
			}
		case strings.HasPrefix(h.Name, "anhaenge/") && h.Typeflag == tar.TypeReg:
			name := path.Base(h.Name)
			if name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
				return m, fmt.Errorf("Anhang %q", h.Name)
			}
			if err := anhang(name, tr); err != nil {
				return m, err
			}
		}
	}
	if erst {
		return m, ErrPaketFormat
	}
	return m, nil
}

// --- from SMS Backup & Restore XML (signalbackup-tools --exportxml)

// xmlNachricht is one message of the XML, with its attachments' data.
type xmlNachricht struct {
	N     Nachricht
	Daten [][]byte // per attachment, same order as N.Anhaenge (whose Datei is still empty)
}

func ziffern(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func gleicheNummer(a, b string) bool { x, y := ziffern(a), ziffern(b); return x != "" && x == y }

func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			if a.Value == "null" {
				return ""
			}
			return a.Value
		}
	}
	return ""
}

func ohne(liste []string, telefon string) []string {
	var out []string
	gesehen := map[string]bool{}
	for _, a := range liste {
		a = strings.TrimSpace(a)
		if a == "" || gleicheNummer(a, telefon) || gesehen[a] {
			continue
		}
		gesehen[a] = true
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// XMLLesen streams the XML and hands every message to f. It fails when
// the XML ends before </smses>: a cut export is never a packet.
func XMLLesen(r io.Reader, telefon string, f func(xmlNachricht) error) error {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	ende := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		t, ok := tok.(xml.StartElement)
		if !ok {
			if e, ok := tok.(xml.EndElement); ok && e.Name.Local == "smses" {
				ende = true
			}
			continue
		}
		switch t.Name.Local {
		case "sms":
			typ := attr(t, "type")
			n := Nachricht{Richtung: "aus", Zeit: zahl(attr(t, "date")), Name: attr(t, "contact_name"), Text: attr(t, "body"), Gelesen: attr(t, "read") == "1"}
			if typ == "1" {
				n.Richtung, n.Absender = "ein", attr(t, "address")
			}
			n.Teilnehmer = ohne([]string{attr(t, "address")}, telefon)
			if n.Teilnehmer == nil {
				n.Teilnehmer = []string{}
			}
			if err := f(xmlNachricht{N: n}); err != nil {
				return err
			}
		case "mms":
			x, err := mmsLesen(dec, t, telefon)
			if err != nil {
				return err
			}
			if err := f(x); err != nil {
				return err
			}
		}
	}
	if !ende {
		return errors.New("das XML endet vor </smses>")
	}
	return nil
}

func zahl(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

func mmsLesen(dec *xml.Decoder, start xml.StartElement, telefon string) (xmlNachricht, error) {
	n := Nachricht{Richtung: "aus", Zeit: zahl(attr(start, "date")), Name: attr(start, "contact_name"), Gelesen: attr(start, "read") == "1", QuellID: attr(start, "m_id")}
	if attr(start, "msg_box") == "1" {
		n.Richtung = "ein"
	}
	var x xmlNachricht
	var texte, alle []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return x, fmt.Errorf("MMS: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "part":
				ct := attr(t, "ct")
				switch {
				case ct == "application/smil":
				case ct == "text/plain" && attr(t, "data") == "":
					if s := attr(t, "text"); s != "" {
						texte = append(texte, s)
					}
				default:
					d := attr(t, "data")
					if d == "" {
						continue
					}
					b, err := base64.StdEncoding.DecodeString(d)
					if err != nil {
						return x, fmt.Errorf("Anhang: %w", err)
					}
					name := attr(t, "cl")
					if name == "" {
						name = attr(t, "name")
					}
					n.Anhaenge = append(n.Anhaenge, Anhang{Typ: ct, Name: name})
					x.Daten = append(x.Daten, b)
				}
			case "addr":
				a := attr(t, "address")
				alle = append(alle, a)
				if attr(t, "type") == "137" && n.Richtung == "ein" {
					n.Absender = a
				}
			}
		case xml.EndElement:
			if t.Name.Local == "mms" {
				if len(alle) == 0 {
					alle = strings.Split(attr(start, "address"), "~")
				}
				n.Teilnehmer = ohne(alle, telefon)
				if n.Teilnehmer == nil {
					n.Teilnehmer = []string{}
				}
				n.Text = strings.Join(texte, "\n")
				x.N = n
				return x, nil
			}
		}
	}
}

func summen(daten [][]byte) []string {
	var s []string
	for _, d := range daten {
		h := sha256.Sum256(d)
		s = append(s, hex.EncodeToString(h[:]))
	}
	return s
}

// --- back to SMS Backup & Restore XML (what msgvault imports)

func xa(name, wert string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(wert)) // escapes quotes, tabs and newlines too, as an attribute needs
	return " " + name + `="` + b.String() + `"`
}

// XMLSchreiber writes messages as an SMS Backup & Restore XML. A message
// without attachments with one other person is an <sms>, everything else
// an <mms>; msgvault keys both by the same set of participants.
type XMLSchreiber struct {
	w       *bufio.Writer
	telefon string
}

func NeuesXML(w io.Writer, telefon string) (*XMLSchreiber, error) {
	x := &XMLSchreiber{w: bufio.NewWriterSize(w, 1<<20), telefon: telefon}
	_, err := x.w.WriteString("<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>\n<smses>\n")
	return x, err
}

// Nachricht writes one message; anhang opens the data of an attachment.
func (x *XMLSchreiber) Nachricht(n Nachricht, anhang func(Anhang) (io.ReadCloser, error)) error {
	w := x.w
	gelesen := "0"
	if n.Gelesen {
		gelesen = "1"
	}
	if len(n.Anhaenge) == 0 && len(n.Teilnehmer) == 1 {
		typ := "2"
		if n.Richtung == "ein" {
			typ = "1"
		}
		fmt.Fprintf(w, "  <sms protocol=\"0\"%s%s%s subject=\"null\"%s toa=\"null\" sc_toa=\"null\" service_center=\"null\"%s status=\"-1\" locked=\"0\" date_sent=\"0\"%s />\n",
			xa("address", n.Teilnehmer[0]), xa("date", strconv.FormatInt(n.Zeit, 10)), xa("type", typ), xa("body", n.Text), xa("read", gelesen), xa("contact_name", n.Name))
		return nil
	}
	box := "2"
	if n.Richtung == "ein" {
		box = "1"
	}
	fmt.Fprintf(w, "  <mms%s%s%s%s%s m_type=\"%s\" ct_t=\"application/vnd.wap.multipart.related\"%s>\n    <parts>\n",
		xa("date", strconv.FormatInt(n.Zeit, 10)), xa("msg_box", box), xa("read", gelesen), xa("address", strings.Join(n.Teilnehmer, "~")),
		xa("contact_name", n.Name), map[string]string{"1": "132", "2": "128"}[box], xa("m_id", n.QuellID))
	seq := 0
	if n.Text != "" {
		fmt.Fprintf(w, "      <part seq=\"%d\" ct=\"text/plain\" chset=\"106\"%s />\n", seq, xa("text", n.Text))
		seq++
	}
	for _, a := range n.Anhaenge {
		rc, err := anhang(a)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "      <part seq=\"%d\"%s%s%s data=\"", seq, xa("ct", a.Typ), xa("name", a.Name), xa("cl", a.Name))
		enc := base64.NewEncoder(base64.StdEncoding, w)
		_, err = io.Copy(enc, rc)
		rc.Close()
		if err != nil {
			return err
		}
		enc.Close()
		w.WriteString("\" />\n")
		seq++
	}
	w.WriteString("    </parts>\n    <addrs>\n")
	if n.Richtung == "ein" {
		fmt.Fprintf(w, "      <addr%s type=\"137\" charset=\"106\" />\n", xa("address", n.Absender))
		fmt.Fprintf(w, "      <addr%s type=\"151\" charset=\"106\" />\n", xa("address", x.telefon))
		for _, t := range n.Teilnehmer {
			if t != n.Absender {
				fmt.Fprintf(w, "      <addr%s type=\"151\" charset=\"106\" />\n", xa("address", t))
			}
		}
	} else {
		fmt.Fprintf(w, "      <addr%s type=\"137\" charset=\"106\" />\n", xa("address", x.telefon))
		for _, t := range n.Teilnehmer {
			fmt.Fprintf(w, "      <addr%s type=\"151\" charset=\"106\" />\n", xa("address", t))
		}
	}
	_, err := w.WriteString("    </addrs>\n  </mms>\n")
	return err
}

func (x *XMLSchreiber) Schliessen() error {
	if _, err := x.w.WriteString("</smses>\n"); err != nil {
		return err
	}
	return x.w.Flush()
}

// --- keys

// empfaengerLesen reads the public key (outside the vault).
func empfaengerLesen(datei string) (*age.X25519Recipient, error) {
	b, err := os.ReadFile(datei)
	if err != nil {
		return nil, err
	}
	return age.ParseX25519Recipient(strings.TrimSpace(string(b)))
}

// identitaetLesen reads the private key (inside the open vault).
func identitaetLesen(datei string) (*age.X25519Identity, error) {
	b, err := os.ReadFile(datei)
	if err != nil {
		return nil, err
	}
	defer nullen(b)
	return age.ParseX25519Identity(strings.TrimSpace(string(b)))
}

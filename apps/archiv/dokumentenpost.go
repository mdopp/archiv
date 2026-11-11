package main

import (
	"bytes"
	"log"
	"net/mail"
	"os"
	"strconv"
	"strings"
)

// The household's document mailbox (owner, 2026-11-11): Paperless reads
// one IMAP mailbox, a catch-all for the domain, and files what comes to
// <login>+doc@<domain> as that person's (laib templates/paperless,
// laib#1160). The archive reads the same mailbox for its person, so the
// mail to a document is in their archive like any other mail: only the
// mails addressed to <login>+doc@<domain> or <login>+doc+<anything>@<domain>,
// in the one folder Paperless reads, read-only (EXAMINE, BODY.PEEK:
// Paperless' flags stay as they are). The login comes from the box
// (DOKUMENTE_MAIL_*, the password as a file, never the environment); the
// person enters nothing. Nothing is ever deleted there (aufraeumen.go
// works on the person's own mailboxes only).

// kontoDokumente is the document mailbox's ID: its fetch state, its errors.
const kontoDokumente = "dokumente"

type Dokumentenpostfach struct {
	Host          string
	Port          int
	Benutzer      string
	Ordner        string // the folder Paperless reads; INBOX
	Domain        string // of <login>+doc@<domain>
	Login         string // the person (ARCHIV_PERSON)
	PasswortDatei string
}

// DokumentenpostfachAus reads DOKUMENTE_MAIL_HOST, _PORT, _USER, _ORDNER,
// _DOMAIN and the password file (DOKUMENTE_MAIL_PASSWORD_FILE, default
// /run/secrets/dokumente-mail-password). nil when the box gives no host,
// user or domain: then there is no document mailbox to read.
func DokumentenpostfachAus(person string, getenv func(string) string) *Dokumentenpostfach {
	d := &Dokumentenpostfach{
		Host:          strings.TrimSpace(getenv("DOKUMENTE_MAIL_HOST")),
		Benutzer:      strings.TrimSpace(getenv("DOKUMENTE_MAIL_USER")),
		Ordner:        strings.TrimSpace(getenv("DOKUMENTE_MAIL_ORDNER")),
		Domain:        strings.ToLower(strings.TrimPrefix(strings.TrimSpace(getenv("DOKUMENTE_MAIL_DOMAIN")), "@")),
		Login:         strings.ToLower(strings.TrimSpace(person)),
		PasswortDatei: strings.TrimSpace(getenv("DOKUMENTE_MAIL_PASSWORD_FILE")),
	}
	if d.PasswortDatei == "" {
		d.PasswortDatei = "/run/secrets/dokumente-mail-password"
	}
	if p := strings.TrimSpace(getenv("DOKUMENTE_MAIL_PORT")); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			log.Printf("archiv: DOKUMENTE_MAIL_PORT %q ist keine Portnummer: Dokumenten-Postfach aus", p)
			return nil
		}
		d.Port = n
	}
	if d.Host == "" || d.Benutzer == "" || d.Domain == "" || d.Login == "" || strings.ContainsAny(d.Login, "@+ ") {
		return nil
	}
	return d
}

func (d *Dokumentenpostfach) ordner() string {
	if d.Ordner == "" {
		return "INBOX"
	}
	return d.Ordner
}

// lokal is what the server is asked for: IMAP's TO search is a substring.
func (d *Dokumentenpostfach) lokal() string { return d.Login + "+doc" }

// Adresse is the person's document address, as Homes shows it.
func (d *Dokumentenpostfach) Adresse() string { return d.lokal() + "@" + d.Domain }

// zugang is the login, its password read from the box's file; false
// while the box gave none (the owner has not set it for this archive).
func (d *Dokumentenpostfach) zugang() (imapZugang, bool) {
	b, err := os.ReadFile(d.PasswortDatei)
	if err != nil {
		return imapZugang{}, false
	}
	defer nullen(b)
	pw := string(bytes.TrimRight(b, "\r\n"))
	if pw == "" {
		return imapZugang{}, false
	}
	return imapZugang{ID: kontoDokumente, Host: d.Host, Port: d.Port, Benutzer: d.Benutzer, Passwort: pw}, true
}

// Passt: the mail is addressed to this person's document address — a
// recipient whose local part is exactly <login>+doc or begins with
// <login>+doc+, at the domain. The server's TO search only narrows: it
// matches hanna+doc for anna+doc, and a mail only naming the address in
// its subject. To, Cc and the delivery headers a catch-all adds count.
func (d *Dokumentenpostfach) Passt(roh []byte) bool {
	m, err := mail.ReadMessage(bytes.NewReader(roh))
	if err != nil {
		return false
	}
	lokal := d.lokal()
	for _, kopf := range []string{"To", "Cc", "Delivered-To", "X-Original-To", "Envelope-To"} {
		for _, wert := range m.Header[kopf] {
			for _, a := range adressen(wert) {
				at := strings.LastIndexByte(a, '@')
				if at <= 0 || a[at+1:] != d.Domain {
					continue
				}
				if l := a[:at]; l == lokal || strings.HasPrefix(l, lokal+"+") {
					return true
				}
			}
		}
	}
	return false
}

// adressen are the addresses in a header, lower case: parsed as RFC 5322
// says, else (a header a mail program wrote loosely) every word with an @.
func adressen(wert string) []string {
	var out []string
	if l, err := (&mail.AddressParser{}).ParseList(wert); err == nil {
		for _, a := range l {
			out = append(out, strings.ToLower(a.Address))
		}
		return out
	}
	for _, w := range strings.FieldsFunc(wert, func(r rune) bool { return strings.ContainsRune(" ,;<>\"'\t\r\n()", r) }) {
		if strings.Contains(w, "@") {
			out = append(out, strings.ToLower(w))
		}
	}
	return out
}

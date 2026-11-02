package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
)

// werkzeug: `archiv paket-aus-xml` turns an SMS Backup & Restore XML
// (from signalbackup-tools, or an Android SMS backup) into a packet in
// the Briefkasten, as the nightly run did with a Signal backup before
// import-signal: only the messages not packed before, encrypted to the
// person's public key.
// The CI integration test uses it to send a packet through the real
// msgvault; an admin can use it to bring an old SMS backup in.
//
//	archiv paket-aus-xml -telefon +49151… [-quelle sms] < backup.xml
func werkzeug(args []string) int {
	fs := flag.NewFlagSet("paket-aus-xml", flag.ContinueOnError)
	telefon := fs.String("telefon", "", "the person's own number, E.164")
	quelle := fs.String("quelle", "signal", "the source named in the manifest")
	data := fs.String("data", env("DATA", "/data"), "the instance's data folder")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	tel, err := telefonnummer(*telefon)
	if err != nil {
		fmt.Fprintln(os.Stderr, saysTelefonFormat)
		return 2
	}
	empf, err := empfaengerLesen(filepath.Join(*data, "briefkasten.pub"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Das Archiv ist noch nicht eingerichtet (kein öffentlicher Schlüssel):", err)
		return 1
	}
	g := &Gesehen{Dir: filepath.Join(*data, "gesehen")}
	gesehen, err := g.Laden(*quelle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	post := filepath.Join(*data, "briefkasten")
	os.MkdirAll(post, 0o700)
	jetzt := time.Now().UTC()
	tmp := filepath.Join(post, ".neu-"+*quelle+"-"+zufall()+".age")
	defer os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	neu, n, an, err := PackeXML(os.Stdin, out, empf, Manifest{Quelle: *quelle, Herkunft: "box"}, tel, g, gesehen, jetzt)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if n == 0 {
		fmt.Println("nichts Neues, kein Paket")
		return 0
	}
	name, err := einwerfen(tmp, post, *quelle, jetzt)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := g.Anhaengen(*quelle, neu); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Printf("%s: %d Nachrichten, %d Anhänge\n", name, n, an)
	return 0
}

// mailWerkzeug: `archiv paket-aus-mails -konto <adresse> <ordner>` packs
// the .eml files of a folder (an old export, or the CI test's mails) as
// one mail packet, as the hourly IMAP round does: only the messages not
// packed before, into INBOX, as read.
func mailWerkzeug(args []string) int {
	fs := flag.NewFlagSet("paket-aus-mails", flag.ContinueOnError)
	konto := fs.String("konto", "", "the mail account the messages belong to")
	data := fs.String("data", env("DATA", "/data"), "the instance's data folder")
	if err := fs.Parse(args); err != nil || *konto == "" || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "archiv paket-aus-mails -konto <adresse> <ordner mit .eml>")
		return 2
	}
	empf, err := empfaengerLesen(filepath.Join(*data, "briefkasten.pub"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Das Archiv ist noch nicht eingerichtet (kein öffentlicher Schlüssel):", err)
		return 1
	}
	pf, err := ordnerPostfach(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	h := &Mailholer{Post: filepath.Join(*data, "briefkasten"), Gesehen: &Gesehen{Dir: filepath.Join(*data, "gesehen")}}
	g, err := h.holen(context.Background(), pf, empf, *konto)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if g.Paket == "" {
		fmt.Println("nichts Neues, kein Paket")
		return 0
	}
	fmt.Printf("%s: %d Mails\n", g.Paket, g.Nachrichten)
	return 0
}

// lokalesPostfach is a folder of .eml files as one IMAP folder, INBOX.
type lokalesPostfach struct{ dateien []string }

func ordnerPostfach(dir string) (*lokalesPostfach, error) {
	e, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	p := &lokalesPostfach{}
	for _, x := range e {
		if x.Type().IsRegular() && strings.HasSuffix(x.Name(), ".eml") {
			p.dateien = append(p.dateien, filepath.Join(dir, x.Name()))
		}
	}
	sort.Strings(p.dateien)
	return p, nil
}

func (p *lokalesPostfach) Ordner() ([]PostOrdner, error) {
	return []PostOrdner{{Name: "INBOX", Trenner: "/"}}, nil
}
func (p *lokalesPostfach) Waehlen(string) (uint32, error) { return 1, nil }
func (p *lokalesPostfach) NeueUIDs(nach uint32) ([]uint32, error) {
	var out []uint32
	for i := range p.dateien {
		if u := uint32(i + 1); u > nach {
			out = append(out, u)
		}
	}
	return out, nil
}
func (p *lokalesPostfach) Holen(uids []uint32, f func(Mail) error) error {
	for _, u := range uids {
		b, err := os.ReadFile(p.dateien[u-1])
		if err != nil {
			return err
		}
		if err := f(Mail{UID: u, Flags: []string{imap.SeenFlag}, Roh: b}); err != nil {
			return err
		}
	}
	return nil
}
func (p *lokalesPostfach) Abmelden() error { return nil }

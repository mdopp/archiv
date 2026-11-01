package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// werkzeug: `archiv paket-aus-xml` turns an SMS Backup & Restore XML
// (from signalbackup-tools, or an Android SMS backup) into a packet in
// the Briefkasten, as the nightly run does with a Signal backup: only
// the messages not packed before, encrypted to the person's public key.
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

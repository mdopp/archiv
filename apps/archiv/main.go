// archiv: one person's message archive (laib ADR 0029). A small
// supervisor around msgvault: it keeps the archive in a gocryptfs vault
// that only the person's password opens, starts msgvault while it is
// open, imports the phone backups Homes leaves in the inbox and the mail
// it fetches over IMAP, closes the
// vault again after two idle hours, and writes laib's status file.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/mdopp/archiv/apps/archiv/tresor"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// sekunden reads a duration in seconds from the environment.
func sekunden(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return def
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "paket-aus-xml" {
		os.Exit(werkzeug(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "paket-aus-mails" {
		os.Exit(mailWerkzeug(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "umfeld-gespraeche" {
		os.Exit(gespraecheWerkzeug(os.Args[2:], os.Stdin, os.Stdout))
	}
	if len(os.Args) > 1 && os.Args[1] == "umfeld-kante" {
		os.Exit(kanteWerkzeug(os.Args[2:], os.Stdin, os.Stdout))
	}
	if len(os.Args) > 1 && os.Args[1] == "umfeld-gruppen" {
		os.Exit(gruppenWerkzeug(os.Args[2:], os.Stdin, os.Stdout))
	}
	if len(os.Args) > 1 && os.Args[1] == "umfeld-hinweise" {
		os.Exit(hinweiseWerkzeug(os.Args[2:], os.Stdin, os.Stdout))
	}
	person := os.Getenv("ARCHIV_PERSON")
	if person == "" {
		log.Fatal("archiv: ARCHIV_PERSON fehlt: die Instanz gehört genau einer Person")
	}
	data := env("DATA", "/data")
	port, _ := strconv.Atoi(env("MSGVAULT_PORT", "8090"))
	statusDatei := env("STATUS_FILE", filepath.Join(data, "laib-status.json"))
	run := execAusfuehrer{}

	prot := LadeProtokoll(filepath.Join(data, "archiv-stand.json"))
	eingangDir := env("EINGANG", "/eingang")
	eingang := &Eingang{Dir: eingangDir, Ruhe: sekunden("EINGANG_RUHE", time.Minute)}
	empfaenger := filepath.Join(data, "briefkasten.pub")
	schluessel := Schluessel{Dir: filepath.Join(data, "schluessel")}
	cipher := filepath.Join(data, "tresor")
	vault := tresor.Neu(tresor.Konfig{
		Cipher:         cipher,
		Mnt:            env("TRESOR_MOUNT", "/run/tresor"),
		Port:           port,
		Empfaenger:     empfaenger,
		Mounter:        &Gocryptfs{Bin: env("GOCRYPTFS", "gocryptfs"), Fusermount: env("FUSERMOUNT", "fusermount3"), Run: run},
		Dienst:         &Msgvault{Bin: env("MSGVAULT", "msgvault"), Port: port},
		Leerlauf:       sekunden("LEERLAUF", 2*time.Hour),
		Sitzung:        sekunden("SITZUNG", tresor.SitzungStandard),
		Schluessel:     sekunden("SCHLUESSEL", tresor.SchluesselStandard),
		MsgvaultConfig: msgvaultConfig,
	})
	prog := neueProgramme(vault)
	vault.Einziehen = func() error { return schluessel.SignalEinziehen(prog.Importe) }
	takt := sekunden("IMPORT_TAKT", 20*time.Second)
	bk := &Briefkasten{Eingang: eingang, Letzte: filepath.Join(eingangDir, "letzte"), Post: filepath.Join(data, "briefkasten"),
		Empfaenger: empfaenger, Gesehen: &Gesehen{Dir: filepath.Join(data, "gesehen")}, Protokoll: prot, Takt: takt}
	mh := &Mailholer{Post: bk.Post, Empfaenger: empfaenger, Gesehen: bk.Gesehen, Schluessel: schluessel, Protokoll: prot,
		Takt: sekunden("MAIL_TAKT", time.Hour), Dokumente: DokumentenpostfachAus(person, os.Getenv)}
	pb := &Postbote{Post: bk.Post, Eingang: eingang, Letzte: bk.Letzte, Schluessel: schluessel, Protokoll: prot, Run: run, Msgvault: env("MSGVAULT", "msgvault"),
		SignalBin: env("SIGNALBACKUP_TOOLS", "signalbackup-tools"), Takt: takt, Arbeit: vault.Arbeit}
	rad, radErr := RadicaleAusUmgebung(person)
	if radErr != nil {
		log.Printf("archiv: Kontakte und Kalender aus: %v", radErr)
	}
	// msgvault for each program, with its own key
	apiImporte := &MsgvaultAPI{Port: port, Key: prog.Importe.msgvaultSchluessel}
	apiNachrichten := &MsgvaultAPI{Port: port, Key: prog.Nachrichten.msgvaultSchluessel}
	apiStammbaum := &MsgvaultAPI{Port: port, Key: prog.Stammbaum.msgvaultSchluessel}
	aufr := &Aufraeumer{Schluessel: schluessel, Beleg: apiImporte, Takt: sekunden("AUFRAEUMEN_TAKT", time.Hour)}
	ab := &Abgleich{Radicale: rad, Konfig: radErr, API: apiImporte, Run: run, Msgvault: env("MSGVAULT", "msgvault"),
		Takt: sekunden("KONTAKTE_TAKT", 3*time.Hour), Arbeit: vault.Arbeit}
	if rad != nil {
		relaisPort, _ := strconv.Atoi(env("KONTAKTE_RELAIS_PORT", "18443"))
		ab.Relais = &Relais{Person: person, Port: relaisPort}
	}
	vault.Vorbereiten = func() error { return ab.Vorbereiten(prog.Importe) }
	um := &Umfeld{URL: immichURL(), Oeffentlich: immichOeffentlich(os.Getenv), Radicale: rad, Msgvault: apiStammbaum, Modell: modellAus(os.Getenv), Schluessel: schluessel, Takt: sekunden("UMFELD_TAKT", time.Hour)}
	pb.SozialNeu = func(Innen) { um.SozialNeu(prog.Stammbaum) } // the net reads it with its own key
	if os.Getenv("IMMICH_URL") != "" && um.URL == "" {
		log.Printf("archiv: IMMICH_URL %q ist keine http(s)-Adresse: Umfeld aus", os.Getenv("IMMICH_URL"))
	}
	web := &Web{Puls: &Puls{}, Passkeys: &Passkeys{Datei: filepath.Join(schluessel.Dir, "passkeys.json")}, Person: person, Secret: os.Getenv("PROXY_SECRET"), PublicURL: os.Getenv("PUBLIC_URL"), HomesSecret: homesSecret(os.Getenv), HomesIntern: homesIntern(os.Getenv),
		Tresor: vault, P: prog, Freigaben: &Freigabenwahl{Datei: filepath.Join(data, "freigaben.json")}, Briefkasten: bk, Mailholer: mh, Postbote: pb, Eingang: eingang, Protokoll: prot, Schluessel: schluessel,
		Letzte: bk.Letzte, Archiv: archivProxy(port), Abgleich: ab, Personen: &Personenkarte{API: apiNachrichten}, Umfeld: um, Aufraeumer: aufr}
	os.Remove(filepath.Join(data, "aufgaben.json")) // the counts kept outside the vault before homes#375: Homes keeps the tasks now

	status := func() {
		st := vault.Stand()
		if st.Zustand == Verschlossen && !st.ZuSeit.IsZero() {
			if _, _, zu := prot.Kopie(); zu == nil || !zu.Equal(st.ZuSeit) {
				prot.Zu(st.ZuSeit)
			}
		}
		statusSchreiben(statusDatei, statusBauen(web.lage(st)))
		web.Puls.Senden()
	}
	vault.Geaendert, bk.Geaendert, mh.Geaendert, pb.Geaendert, web.Geaendert, ab.Geaendert, um.Geaendert = status, status, status, status, status, status, status
	aufr.Geaendert = status
	// the import, the Abgleich, the Umfeld and old mail at the provider (it needs msgvault's proof)
	vault.Lauf = laufen(lauf{prog.Importe, pb.Lauf}, lauf{prog.Importe, ab.Lauf}, lauf{prog.Stammbaum, um.Lauf}, lauf{prog.Importe, aufr.Lauf})
	vault.Erkennen()
	if _, _, zu := prot.Kopie(); zu == nil && vault.Stand().Zustand == Verschlossen {
		prot.Zu(time.Now()) // closed since at least now: a restart closes it
	}
	if os.Getenv("PROXY_SECRET") == "" {
		log.Printf("archiv: PROXY_SECRET fehlt: niemand wird als angemeldet erkannt, nur Agenten mit Schlüssel kommen durch")
	}
	status()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	HalbeWeg(bk.Post)
	go bk.Lauf(ctx) // the Briefkasten: open or closed
	go mh.Lauf(ctx) // mail, hourly: open or closed
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				vault.Pruefen(ctx)
				status()
			}
		}
	}()

	srv := &http.Server{Addr: env("LISTEN", ":8080"), Handler: web, ReadHeaderTimeout: 20 * time.Second}
	srv.RegisterOnShutdown(web.Puls.Schluss) // live pages end their stream at once
	go func() {
		log.Printf("archiv für %s hört auf %s (Tresor %s, Zustand %s)", person, srv.Addr, cipher, vault.Stand().Zustand)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Printf("archiv: halte an, schließe den Tresor")
	ende, fertig := context.WithTimeout(context.Background(), 60*time.Second)
	defer fertig()
	srv.Shutdown(ende)
	if err := vault.Abschliessen(ende, "Container hält an"); err != nil {
		log.Printf("archiv: abschließen beim Anhalten: %v", err)
	}
	status()
}

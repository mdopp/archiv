// archiv: one person's message archive (laib ADR 0029). A small
// supervisor around msgvault: it keeps the archive in a gocryptfs vault
// that only the person's password opens, starts msgvault while it is
// open, imports the phone backups Haus leaves in the inbox and the mail
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
	tresor := &Tresor{
		Cipher:     filepath.Join(data, "tresor"),
		Mnt:        env("TRESOR_MOUNT", "/run/tresor"),
		Port:       port,
		Empfaenger: empfaenger,
		Mounter:    &Gocryptfs{Bin: env("GOCRYPTFS", "gocryptfs"), Fusermount: env("FUSERMOUNT", "fusermount3"), Run: run},
		Dienst:     &Msgvault{Bin: env("MSGVAULT", "msgvault"), Port: port},
		Leerlauf:   sekunden("LEERLAUF", 2*time.Hour),
	}
	takt := sekunden("IMPORT_TAKT", 20*time.Second)
	bk := &Briefkasten{Eingang: eingang, Letzte: filepath.Join(eingangDir, "letzte"), Post: filepath.Join(data, "briefkasten"),
		Empfaenger: empfaenger, Gesehen: &Gesehen{Dir: filepath.Join(data, "gesehen")}, Schluessel: schluessel,
		Klartext: env("KLARTEXT", "/dev/shm/archiv"), Run: run, SignalBin: env("SIGNALBACKUP_TOOLS", "signalbackup-tools"),
		Protokoll: prot, Takt: takt}
	mh := &Mailholer{Post: bk.Post, Empfaenger: empfaenger, Gesehen: bk.Gesehen, Schluessel: schluessel, Protokoll: prot,
		Takt: sekunden("MAIL_TAKT", time.Hour)}
	pb := &Postbote{Post: bk.Post, Eingang: eingang, Schluessel: schluessel, Protokoll: prot, Run: run, Msgvault: env("MSGVAULT", "msgvault"),
		Takt: takt, Arbeit: tresor.Arbeit}
	web := &Web{Puls: &Puls{}, Person: person, Secret: os.Getenv("PROXY_SECRET"), PublicURL: os.Getenv("PUBLIC_URL"), HausSecret: os.Getenv("ARCHIV_HAUS_SECRET"),
		Tresor: tresor, Briefkasten: bk, Mailholer: mh, Postbote: pb, Eingang: eingang, Protokoll: prot, Schluessel: schluessel,
		Letzte: bk.Letzte, Archiv: archivProxy(port)}

	status := func() {
		st := tresor.Stand()
		if st.Zustand == Verschlossen && !st.ZuSeit.IsZero() {
			if _, _, zu := prot.Kopie(); zu == nil || !zu.Equal(st.ZuSeit) {
				prot.Zu(st.ZuSeit)
			}
		}
		statusSchreiben(statusDatei, statusBauen(web.lage(st)))
		web.Puls.Senden()
	}
	tresor.Geaendert, bk.Geaendert, mh.Geaendert, pb.Geaendert, web.Geaendert = status, status, status, status, status
	tresor.Lauf = pb.Lauf
	tresor.Erkennen()
	if _, _, zu := prot.Kopie(); zu == nil && tresor.Stand().Zustand == Verschlossen {
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
				tresor.Pruefen(ctx)
				status()
			}
		}
	}()

	srv := &http.Server{Addr: env("LISTEN", ":8080"), Handler: web, ReadHeaderTimeout: 20 * time.Second}
	srv.RegisterOnShutdown(web.Puls.Schluss) // live pages end their stream at once
	go func() {
		log.Printf("archiv für %s hört auf %s (Tresor %s, Zustand %s)", person, srv.Addr, tresor.Cipher, tresor.Stand().Zustand)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Printf("archiv: halte an, schließe den Tresor")
	ende, fertig := context.WithTimeout(context.Background(), 60*time.Second)
	defer fertig()
	srv.Shutdown(ende)
	if err := tresor.Abschliessen(ende, "Container hält an"); err != nil {
		log.Printf("archiv: abschließen beim Anhalten: %v", err)
	}
	status()
}

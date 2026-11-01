// archiv: one person's message archive (laib ADR 0029). A small
// supervisor around msgvault: it keeps the archive in a gocryptfs vault
// that only the person's password opens, starts msgvault while it is
// open, imports the phone backups Haus leaves in the inbox, closes the
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
	person := os.Getenv("ARCHIV_PERSON")
	if person == "" {
		log.Fatal("archiv: ARCHIV_PERSON fehlt: die Instanz gehört genau einer Person")
	}
	data := env("DATA", "/data")
	port, _ := strconv.Atoi(env("MSGVAULT_PORT", "8090"))
	statusDatei := env("STATUS_FILE", filepath.Join(data, "laib-status.json"))
	run := execAusfuehrer{}

	prot := LadeProtokoll(filepath.Join(data, "archiv-stand.json"))
	eingang := &Eingang{Dir: env("EINGANG", "/eingang"), Ruhe: sekunden("EINGANG_RUHE", time.Minute)}
	tresor := &Tresor{
		Cipher:   filepath.Join(data, "tresor"),
		Mnt:      env("TRESOR_MOUNT", "/run/tresor"),
		Port:     port,
		Mounter:  &Gocryptfs{Bin: env("GOCRYPTFS", "gocryptfs"), Fusermount: env("FUSERMOUNT", "fusermount3"), Run: run},
		Dienst:   &Msgvault{Bin: env("MSGVAULT", "msgvault"), Port: port},
		Leerlauf: sekunden("LEERLAUF", 2*time.Hour),
	}
	im := &Importeur{Eingang: eingang, Protokoll: prot, Run: run, SignalBin: env("SIGNALBACKUP_TOOLS", "signalbackup-tools"),
		Msgvault: env("MSGVAULT", "msgvault"), Takt: sekunden("IMPORT_TAKT", 20*time.Second), Arbeit: tresor.Arbeit}
	web := &Web{Person: person, Secret: os.Getenv("PROXY_SECRET"), PublicURL: os.Getenv("PUBLIC_URL"),
		Tresor: tresor, Importeur: im, Eingang: eingang, Protokoll: prot, Archiv: archivProxy(port)}

	status := func() {
		st := tresor.Stand()
		if st.Zustand == Verschlossen && !st.ZuSeit.IsZero() {
			if _, _, zu := prot.Kopie(); zu == nil || !zu.Equal(st.ZuSeit) {
				prot.Zu(st.ZuSeit)
			}
		}
		statusSchreiben(statusDatei, statusBauen(person, st, wartende(eingang), prot, im.Laeuft(), im.FehlenSchluessel(), web.PublicURL, time.Now()))
	}
	tresor.Geaendert, im.Geaendert, web.Geaendert = status, status, status
	tresor.Lauf = im.Lauf
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

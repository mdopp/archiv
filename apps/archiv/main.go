// archiv: a small Laib app. One page, a health check, and the user
// Caddy forwarded: Remote-User counts only when the request carries the
// proxy secret, which Caddy adds after Authelia's check (ADR 0012 §3), so a
// process on the box cannot forge a login by setting the header itself.
package main

import (
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"os"
)

const proxyHeader = "X-Archiv-Proxy" // the header laib renders for the route's proxySecret

func handler(secret string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if user := forwardedUser(r, secret); user != "" {
			fmt.Fprintf(w, "Hallo, %s!\n", user)
			return
		}
		fmt.Fprintln(w, "Hallo!")
	})
	return mux
}

// forwardedUser is the logged-in user, or "" when the request did not come
// through Caddy with the secret.
func forwardedUser(r *http.Request, secret string) string {
	got := r.Header.Get(proxyHeader)
	if secret == "" || subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
		return ""
	}
	return r.Header.Get("Remote-User")
}

func main() {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("archiv listens on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler(os.Getenv("PROXY_SECRET"))))
}

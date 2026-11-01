package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// archivProxy forwards to msgvault on loopback. A request the page let
// through (the person's, through Caddy) carries the vault's key in its
// context: the proxy adds it as X-API-Key, so msgvault's web UI works
// without its login and the person never sees the key. A bearer request
// (an agent) has no key in its context and goes as it came. Caddy's own
// headers (the proxy secret, Remote-*) and Authelia's cookie never reach
// msgvault.
func archivProxy(port int) http.Handler {
	ziel := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(ziel)
			for _, h := range []string{"X-Forwarded-Proto", "X-Forwarded-Host"} {
				if v := pr.In.Header.Get(h); v != "" {
					pr.Out.Header.Set(h, v)
				}
			}
			pr.Out.Header.Del(proxyHeader)
			for k := range pr.Out.Header {
				if strings.HasPrefix(k, "Remote-") {
					pr.Out.Header.Del(k)
				}
			}
			pr.Out.Header.Del("X-Api-Key")
			if key, ok := pr.In.Context().Value(schluesselKontext{}).(string); ok && key != "" {
				pr.Out.Header.Del("Authorization")
				pr.Out.Header.Set("X-Api-Key", key)
			}
			ohneAutheliaKeks(pr.Out)
		},
		FlushInterval: 100 * time.Millisecond,
		ErrorHandler: func(rw http.ResponseWriter, r *http.Request, err error) {
			log.Printf("archiv: msgvault %s %s: %v", r.Method, r.URL.Path, err)
			if strings.HasPrefix(r.URL.Path, "/api/") || bearer(r) {
				jsonFehler(rw, http.StatusBadGateway, saysArchivWeg)
				return
			}
			seite(rw, http.StatusBadGateway, "Archiv antwortet nicht", "<p>"+saysArchivWeg+"</p>", "")
		},
	}
}

func ohneAutheliaKeks(r *http.Request) {
	alle := r.Cookies()
	if len(alle) == 0 {
		return
	}
	r.Header.Del("Cookie")
	for _, c := range alle {
		if !strings.HasPrefix(c.Name, "authelia") {
			r.AddCookie(c)
		}
	}
}

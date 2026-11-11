package main

import (
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestSchau serves kanteBox's open archive on ARCHIV_SCHAU (an address)
// for ARCHIV_SCHAU_DAUER, signed in as anna: for looking at the pages in a
// browser. Without the variable it does nothing.
func TestSchau(t *testing.T) {
	addr := os.Getenv("ARCHIV_SCHAU")
	if addr == "" {
		t.Skip("ARCHIV_SCHAU is not set")
	}
	wt, _, _, _, _ := kanteBox(t)
	dauer, _ := time.ParseDuration(os.Getenv("ARCHIV_SCHAU_DAUER"))
	if dauer == 0 {
		dauer = 10 * time.Minute
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		for k, v := range anna {
			r.Header.Set(k, v)
		}
		wt.w.ServeHTTP(rw, r)
	})}
	go srv.Serve(l)
	time.Sleep(dauer)
	srv.Close()
}

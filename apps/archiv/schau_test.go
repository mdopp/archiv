package main

import (
	"image"
	"image/png"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestSchau serves kanteBox's open archive on ARCHIV_SCHAU (an address)
// for ARCHIV_SCHAU_DAUER, signed in as anna: for looking at the pages in a
// browser; with ARCHIV_SCHAU_VIEL a pair with many chats and threads
// (kanteViel). Without the variable it does nothing.
func TestSchau(t *testing.T) {
	addr := os.Getenv("ARCHIV_SCHAU")
	if addr == "" {
		t.Skip("ARCHIV_SCHAU is not set")
	}
	wt, ut, _, innen, _ := kanteBox(t)
	if os.Getenv("ARCHIV_SCHAU_VIEL") != "" {
		kanteViel(t, ut, innen)
	}
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
		if r.URL.Path == kantePfad+"/bild" { // the fake Immich has no picture: a grey square stands for it
			rw.Header().Set("Content-Type", "image/png")
			m := image.NewGray(image.Rect(0, 0, 8, 8))
			for i := range m.Pix {
				m.Pix[i] = 0x60 + uint8(len(r.URL.RawQuery)%4)*0x18
			}
			png.Encode(rw, m)
			return
		}
		wt.w.ServeHTTP(rw, r)
	})}
	go srv.Serve(l)
	time.Sleep(dauer)
	srv.Close()
}

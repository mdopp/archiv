package main

import (
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestSchau serves kanteBox's open archive on ARCHIV_SCHAU (an address)
// for ARCHIV_SCHAU_DAUER, signed in as anna: for looking at the pages in a
// browser; with ARCHIV_SCHAU_VIEL a pair with many chats and threads
// (kanteViel), with ARCHIV_SCHAU_LEUTE that many more people on anna's
// photos (schauLeute). Without the variable it does nothing.
func TestSchau(t *testing.T) {
	addr := os.Getenv("ARCHIV_SCHAU")
	if addr == "" {
		t.Skip("ARCHIV_SCHAU is not set")
	}
	wt, ut, _, innen, _ := kanteBox(t)
	if os.Getenv("ARCHIV_SCHAU_VIEL") != "" {
		kanteViel(t, ut, innen)
	}
	if viele, _ := strconv.Atoi(os.Getenv("ARCHIV_SCHAU_LEUTE")); viele > 0 {
		schauLeute(t, ut, innen, viele)
	}
	if os.Getenv("ARCHIV_SCHAU_FAMILIE") != "" {
		schauFamilie(t, ut, innen)
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

// schauLeute puts viele more people on anna's photos, each on a few days
// of its own (the first ones on more), for the picture with many.
func schauLeute(t *testing.T, ut *umfeldTest, innen Innen, viele int) {
	tab, err := tabelleLaden(innen)
	if err != nil {
		t.Fatal(err)
	}
	vornamen := []string{"Lena", "Jonas", "Mia", "Paul", "Emma", "Felix", "Lina", "Max", "Clara", "Noah", "Ida", "Elias", "Greta", "Theo", "Marie"}
	nachnamen := []string{"Schmidt", "Weber", "Fischer", "Wagner", "Becker", "Hoffmann", "Schulz", "Koch"}
	for i := range viele {
		id := fmt.Sprintf("p-viel%d", i)
		tab.Namen[id] = vornamen[i%len(vornamen)] + " " + nachnamen[i/len(vornamen)%len(nachnamen)]
		for d := range 2 + (viele-i)/4 {
			tab.Fotos = append(tab.Fotos, netzFoto{ID: fmt.Sprintf("v%d-%d", i, d), T: time.Date(2020+d%6, time.Month(1+i%12), 1+d, 12, 0, 0, 0, time.UTC), Wer: []string{"p-anna", id}})
		}
	}
	if err := jsonImTresor(innen.umfeldTabelle(), tab); err != nil {
		t.Fatal(err)
	}
	ut.u.laden(innen)
}

// schauFamilie gives anna parents (Rosi and Karl), an aunt whose side is
// not known (Hedwig), a grandmother (Erna), a cousin (Lukas) and a
// relative (Otto), each answered and on one photo day with her: the tree
// with the relatives a step away.
func schauFamilie(t *testing.T, ut *umfeldTest, innen Innen) {
	tab, err := tabelleLaden(innen)
	if err != nil {
		t.Fatal(err)
	}
	am := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	as := antwortenLaden(innen)
	for i, p := range [][3]string{{"p-rosi", "Rosi Berg", "eltern"}, {"p-karl", "Karl Berg", "eltern"}, {"p-hedwig", "Hedwig Sommer", "onkel"},
		{"p-erna", "Erna Lang", "grosseltern"}, {"p-lukas", "Lukas Sommer", "cousin"}, {"p-otto", "Otto Lang", "verwandt"}} {
		tab.Namen[p[0]] = p[1]
		tab.Fotos = append(tab.Fotos, netzFoto{ID: fmt.Sprintf("f%d", i), T: time.Date(2021, 6, 1+i, 12, 0, 0, 0, time.UTC), Wer: []string{"p-anna", p[0]}})
		as[paarKey(p[0], "p-anna")] = antwort{X: p[0], Art: p[2], Am: am}
	}
	if err := jsonImTresor(innen.umfeldTabelle(), tab); err != nil {
		t.Fatal(err)
	}
	if err := jsonImTresor(innen.umfeldAntworten(), as); err != nil {
		t.Fatal(err)
	}
	ut.u.laden(innen)
}

package main

import (
	"encoding/json"
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
	if os.Getenv("ARCHIV_SCHAU_NETZ") != "" {
		schauNetz(t, ut, innen)
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

// schauNetz (ARCHIV_SCHAU_NETZ) puts into anna's net what made it say
// more than it knew (owner 2026-11-11): a wedding of twenty on two days,
// two class chats of thirty with eight parents, a plumber's two mails
// nobody answered, two mails with eight in copy; and what it did not
// read: Jana, whom anna writes daily, and Tobias, close for years and
// silent since 2022. The conversations are JSON, as the vault keeps them.
func schauNetz(t *testing.T, ut *umfeldTest, innen Innen) {
	tab, err := tabelleLaden(innen)
	if err != nil {
		t.Fatal(err)
	}
	karte := func(key, name string) string {
		tab.Kontakte = append(tab.Kontakte, netzKontakt{Key: "privat/" + key, Name: name, Person: map[string]string{"jana": "p-jana", "tobias": "p-tobias"}[key]})
		return `"privat/` + key + `"`
	}
	gaeste := []string{"p-anna", "p-ben"}
	for i, name := range []string{"Gerda Lenz", "Heinz Lenz", "Petra Kuhn", "Rolf Kuhn", "Silke Marx", "Dirk Marx", "Antje Vogt", "Bernd Vogt", "Elke Roth",
		"Jens Roth", "Karin Sauer", "Uwe Sauer", "Birgit Jung", "Olaf Jung", "Heike Franke", "Frank Franke", "Tanja Graf", "Sven Graf"} {
		id := fmt.Sprintf("p-gast%d", i)
		tab.Namen[id], gaeste = name, append(gaeste, id)
	}
	for d := range 2 {
		tab.Fotos = append(tab.Fotos, netzFoto{ID: fmt.Sprintf("hochzeit%d", d), T: time.Date(2024, 6, 14+d, 15, 0, 0, 0, time.UTC), Stadt: "Trier", Wer: gaeste})
	}
	tab.Namen["p-jana"], tab.Namen["p-tobias"] = "Jana Freund", "Tobias Alt"
	for d := range 14 {
		tab.Fotos = append(tab.Fotos, netzFoto{ID: fmt.Sprintf("jana%d", d), T: time.Date(2021+d/3, time.Month(1+d%12), 3+d, 12, 0, 0, 0, time.UTC), Stadt: "Jana", Wer: []string{"p-anna", "p-jana"}},
			netzFoto{ID: fmt.Sprintf("tobias%d", d), T: time.Date(2014+d/2, time.Month(1+d%12), 5+d, 12, 0, 0, 0, time.UTC), Stadt: "Tobias", Wer: []string{"p-anna", "p-tobias"}})
	}
	jana, tobias, maier := karte("jana", "Jana Freund"), karte("tobias", "Tobias Alt"), karte("maier", "Heizung Maier")
	var eltern, kollegen []string
	for i, name := range []string{"Ute Brandt", "Kai Brandt", "Nele Simon", "Ralf Simon", "Dana Ludwig", "Timo Ludwig", "Vera Haas", "Marc Haas"} {
		eltern = append(eltern, karte(fmt.Sprintf("e%d", i), name))
	}
	for i, name := range []string{"Sonja Peters", "Holger Arnold"} {
		kollegen = append(kollegen, karte(fmt.Sprintf("k%d", i), name))
	}
	ich := `"haushalt/a"`
	liste := func(ks []string) string {
		out := ich
		for _, k := range ks {
			out += "," + k
		}
		return "[" + out + "]"
	}
	roh := `[
{"i":"s1","a":"chat","t":"Klasse 3b Eltern","g":28,"n":900,"z":"2026-11-05T08:00:00Z","s":20,"w":` + liste(eltern) + `},
{"i":"s2","a":"chat","t":"Klasse 3b Sommerfest","g":25,"n":120,"z":"2026-07-01T08:00:00Z","s":15,"w":` + liste(eltern) + `},
{"i":"s3","a":"mail","g":2,"n":1,"z":"2026-03-01T08:00:00Z","s":1,"w":` + liste([]string{maier}) + `},
{"i":"s4","a":"mail","g":2,"n":2,"z":"2026-04-01T08:00:00Z","s":1,"w":` + liste([]string{maier}) + `},
{"i":"s5","a":"mail","g":8,"n":4,"z":"2026-05-01T08:00:00Z","s":3,"w":` + liste(kollegen) + `},
{"i":"s6","a":"mail","g":9,"n":6,"z":"2026-06-01T08:00:00Z","s":4,"w":` + liste(kollegen) + `},
{"i":"s7","a":"direkt","g":2,"n":840,"z":"2026-11-09T08:00:00Z","s":2,"w":` + liste([]string{jana}) + `},
{"i":"s8","a":"direkt","g":2,"n":420,"z":"2022-03-12T08:00:00Z","s":2,"w":` + liste([]string{tobias}) + `}]`
	var gs []netzGespraech
	if err := json.Unmarshal([]byte(roh), &gs); err != nil {
		t.Fatal(err)
	}
	tab.Gespraeche = append(tab.Gespraeche, gs...)
	if err := jsonImTresor(innen.umfeldTabelle(), tab); err != nil {
		t.Fatal(err)
	}
	ut.u.laden(innen)
}

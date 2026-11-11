package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeImmich answers as Immich 3.3.5 does the calls the archive makes:
// api-keys/me (the key's permissions), users/me (its e-mail, needs
// user.read), search/metadata (the photos with exif and people, paged by
// nextPage), a photo's thumbnail (asset.view). Anything else, or another key, is refused.
type fakeImmich struct {
	*httptest.Server
	mu       sync.Mutex
	key      string
	rechte   []string
	email    string // "" refuses users/me as Immich does without user.read
	personen map[string]fakePerson
	fotos    []fakeFoto
	seite    int // photos per page it serves at most
	anfragen []string
}

type fakePerson struct {
	name      string
	versteckt bool
}

type fakeFoto struct {
	ID, Wann string
	Lat, Lng float64
	Stadt    string
	Wer      []string
}

func neuesImmich(t *testing.T) *fakeImmich {
	f := &fakeImmich{key: "immich-key-0123456789abcdef", rechte: []string{"asset.read", "asset.view", "person.read"}, personen: map[string]fakePerson{}, seite: 4}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeImmich) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.anfragen = append(f.anfragen, r.Method+" "+r.URL.Path)
	if r.Header.Get("x-api-key") != f.key {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"message":"Invalid API key","statusCode":401}`)
		return
	}
	darf := func(recht string) bool {
		if slices.Contains(f.rechte, "all") || slices.Contains(f.rechte, recht) {
			return true
		}
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"Missing required permission: `+recht+`","statusCode":403}`)
		return false
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/api-keys/me":
		json.NewEncoder(w).Encode(map[string]any{"id": "k1", "name": "Archiv", "permissions": f.rechte})
	case r.Method == http.MethodGet && r.URL.Path == "/api/users/me":
		if f.email == "" || !darf("user.read") {
			if f.email == "" {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"message":"Missing required permission: user.read"}`)
			}
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "u1", "email": f.email})
	case r.Method == http.MethodPost && r.URL.Path == "/api/search/metadata":
		if !darf("asset.read") {
			return
		}
		var q struct {
			Size, Page int
			WithExif   bool `json:"withExif"`
			WithPeople bool `json:"withPeople"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		if !q.WithExif || !q.WithPeople || q.Page < 1 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		size := min(q.Size, f.seite)
		von := (q.Page - 1) * size
		var items []map[string]any
		for i := von; i < min(von+size, len(f.fotos)); i++ {
			items = append(items, f.item(f.fotos[i]))
		}
		var next any
		if von+size < len(f.fotos) {
			next = strconv.Itoa(q.Page + 1)
		}
		json.NewEncoder(w).Encode(map[string]any{"assets": map[string]any{"total": len(items), "count": len(items), "items": items, "nextPage": next}})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/assets/") && strings.HasSuffix(r.URL.Path, "/thumbnail"):
		if !darf("asset.view") {
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/assets/"), "/thumbnail")
		if r.URL.Query().Get("size") != "thumbnail" || !slices.ContainsFunc(f.fotos, func(p fakeFoto) bool { return p.ID == id }) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		io.WriteString(w, "JPEG "+id)
	default:
		http.Error(w, "not in this fake", http.StatusNotFound)
	}
}

func (f *fakeImmich) item(p fakeFoto) map[string]any {
	exif := map[string]any{"city": p.Stadt, "latitude": nil, "longitude": nil, "dateTimeOriginal": nil}
	if p.Wann != "" {
		exif["dateTimeOriginal"] = p.Wann
	}
	if p.Lat != 0 || p.Lng != 0 {
		exif["latitude"], exif["longitude"] = p.Lat, p.Lng
	}
	var leute []map[string]any
	for _, id := range p.Wer {
		pp := f.personen[id]
		leute = append(leute, map[string]any{"id": id, "name": pp.name, "isHidden": pp.versteckt, "birthDate": nil})
	}
	return map[string]any{"id": p.ID, "type": "IMAGE", "localDateTime": p.Wann, "exifInfo": exif, "people": leute}
}

func (f *fakeImmich) setzen(fotos []fakeFoto) {
	f.mu.Lock()
	f.fotos = fotos
	f.mu.Unlock()
}

// karteMit is a vCard 3.0 with uid, fn and further lines.
func karteMit(uid, fn, extra string) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + fn + "\r\n" + extra + "END:VCARD\r\n"
}

// bei is a photo on day d (2006-01-02) at noon UTC, in Saarbrücken's cell
// or at lat 0/lng 0 (none) with a city, of wer.
func bei(id, d string, lat, lng float64, stadt string, wer ...string) fakeFoto {
	return fakeFoto{ID: id, Wann: d + "T12:00:00.000Z", Lat: lat, Lng: lng, Stadt: stadt, Wer: wer}
}

type umfeldTest struct {
	u      *Umfeld
	immich *fakeImmich
	rad    *fakeRadicale
	innen  Innen
	daten  string // what stands for /data: nothing of the net may land there
	jetzt  *time.Time
}

// netzBox is Homes' fixture (internal/netz/netz_test.go), here: anna's
// key. Immich knows Anna, Ben, Kim (a child on most photos), Fritz,
// Gerd, a hidden Hanna and an unnamed face. Anna and Ben are together on
// three days, Kim with each, Fritz with Kim on two, Gerd with Anna on one
// day (two photos at one place); the household's book has Anna (the
// key's e-mail, iOS: Ben her spouse) and Ben at one address, Carla (no
// face) at Anna's firm; anna's phone (the archive's contact store) has
// Fritz, whom Android calls her friend and vCard 4 her brother.
func netzBox(t *testing.T) *umfeldTest {
	t.Helper()
	im := neuesImmich(t)
	for _, p := range [][2]string{{"p-anna", "Anna Berg"}, {"p-ben", "Ben Berg"}, {"p-kim", "Kim Berg"}, {"p-fritz", "Fritz Kurz"}, {"p-gerd", "Gerd Lang"}, {"u1", ""}} {
		im.personen[p[0]] = fakePerson{name: p[1]}
	}
	im.personen["p-hanna"] = fakePerson{name: "Hanna", versteckt: true}
	im.fotos = []fakeFoto{
		bei("a1", "2019-05-01", 49.23, 7.0, "Saarbrücken", "p-anna", "p-ben", "p-kim"),
		bei("a2", "2019-05-01", 49.231, 7.001, "Saarbrücken", "p-anna", "p-ben", "u1", "p-hanna"), // the same event: once
		bei("a3", "2022-08-10", 0, 0, "Rom", "p-anna", "p-ben"),
		bei("a4", "2025-12-24", 0, 0, "", "p-anna", "p-ben", "p-kim"),
		bei("a5", "2024-03-01", 52.5, 13.4, "Berlin", "p-fritz", "p-kim"),
		bei("a6", "2025-03-01", 52.5, 13.4, "Berlin", "p-fritz", "p-kim"),
		bei("a7", "2023-01-01", 49.23, 7.0, "Saarbrücken", "p-anna", "p-gerd"),
		bei("a8", "2023-01-01", 49.23, 7.0, "Saarbrücken", "p-anna", "p-gerd"),
		bei("a9", "2024-06-01", 49.23, 7.0, "Saarbrücken", "p-kim"),
		{ID: "a10", Stadt: "Rom", Wer: []string{"p-anna", "p-gerd"}}, // no dateTimeOriginal: skipped
	}
	im.email = "anna@box.test"
	im.rechte = append(im.rechte, "user.read")
	rad := neuesRadicale(t)
	rad.haushalt = map[string]string{
		"a.vcf": karteMit("a", "Anna Berg", "EMAIL:anna@box.test\r\nADR;TYPE=HOME:;;Gartenweg 2;Saarbrücken;;66111;Deutschland\r\nORG:Werk AG\r\n"+
			"item1.X-ABRELATEDNAMES:Ben Berg\r\nitem1.X-ABLabel:_$!<Spouse>!$_\r\n"),
		"b.vcf": karteMit("b", "Ben Berg", "ADR;TYPE=HOME:;;Gartenweg 2;Saarbrücken;;66111;Deutschland\r\n"),
		"c.vcf": karteMit("c", "Carla Wolf", "ORG:Werk AG;Einkauf\r\n"),
		"f.vcf": karteMit("f", "Werk AG", "ORG:Werk AG\r\nCATEGORIES:Firma\r\n"),
	}
	innen := Innen{t.TempDir()}
	store := neueKontaktablage()
	jetzt := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	store.Einmischen([]QuellKarte{
		{Href: "k1.vcf", Daten: karteMit("k1", "Anna Berg", "X-ANDROID-CUSTOM:vnd.android.cursor.item/relation;Fritz Kurz;6;;;;;;;;;;;;;\r\nRELATED;TYPE=sibling:urn:uuid:k2\r\n")},
		{Href: "k2.vcf", Daten: karteMit("urn:uuid:k2", "Fritz Kurz", "")},
	}, jetzt)
	if err := store.speichern(kontakteDatei(innen)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(innen.ImmichSchluessel(), []byte(im.key), 0o600); err != nil {
		t.Fatal(err)
	}
	ut := &umfeldTest{immich: im, rad: rad, innen: innen, daten: t.TempDir(), jetzt: &jetzt}
	ut.u = &Umfeld{URL: im.URL, Radicale: rad.radicale(t), Schluessel: Schluessel{Dir: filepath.Join(ut.daten, "schluessel")}, Loc: time.UTC, Now: func() time.Time { return *ut.jetzt }}
	ut.u.Runde(t.Context(), innen)
	if ut.u.Netz() == nil {
		t.Fatalf("no net: %+v", ut.u.Stand())
	}
	return ut
}

func namenVon(vs []Verbindung) (out []string) {
	for _, v := range vs {
		out = append(out, v.Mit.Name)
	}
	return out
}

// The ties of all people in the person's photos, not only hers: a day
// at one place counts once, a tie from photos needs two days (Gerd has
// one), the phone books add the address, the firm and the relations
// (iOS, Android, vCard 4) with the relation as seen from the centre.
// Homes' numbers, from the same fixture.
func TestNetzAusFotosUndTelefonbuch(t *testing.T) {
	ut := netzBox(t)
	n := ut.u.Netz()
	anna := n.Mitte()
	if anna != "p-anna" || n.Ich() != "p-anna" {
		t.Fatalf("the middle is the person's: %q", anna)
	}
	nahe := n.Nahe(anna, 10)
	if got := namenVon(nahe); !slices.Equal(got[:1], []string{"Ben Berg"}) || slices.Contains(got, "Gerd Lang") || slices.Contains(got, "Hanna") || !slices.Contains(got, "Carla Wolf") {
		t.Fatalf("anna's: %v", got)
	}
	if got := n.Gruende(anna, nahe[0].Kante); got != "3 Tage auf deinen Fotos seit 2019 · gleiche Adresse · Ehepartner/in (Telefonbuch)" {
		t.Fatalf("anna–ben: %q", got)
	}
	ben := n.Von("haushalt/b")
	if ben != "p-ben" || n.Gruende(ben, nahe[0].Kante) != "3 Tage auf deinen Fotos seit 2019 · gleiche Adresse · Ehepartner/in von Anna Berg (Telefonbuch)" {
		t.Fatalf("ben's view: %q %q", ben, n.Gruende(ben, nahe[0].Kante))
	}
	fritz := n.Nahe(n.Von("privat/urn:uuid:k2"), 5)
	if got := namenVon(fritz); !slices.Equal(got, []string{"Anna Berg", "Kim Berg"}) {
		t.Fatalf("fritz: %v", got)
	}
	if got := n.Gruende(fritz[0].Mit.ID, fritz[0].Kante); got != "Freund/in (Telefonbuch) · Geschwister (Telefonbuch)" {
		t.Fatalf("anna–fritz: %q", got)
	}
	if got := n.Gruende("p-kim", fritz[1].Kante); got != "2 Tage auf deinen Fotos seit 2024" {
		t.Fatalf("fritz–kim, not anna's: %q", got)
	}
	carla := slices.IndexFunc(nahe, func(v Verbindung) bool { return v.Mit.Name == "Carla Wolf" })
	if n.Gruende(anna, nahe[carla].Kante) != "gleiche Firma" || nahe[carla].Mit.Kontakt != "haushalt/c" {
		t.Fatalf("carla: %+v", nahe[carla])
	}
	if z := n.Zwischen([]string{"p-ben", "p-kim", "p-gerd"}); !slices.Equal(z, [][2]int{{0, 1}}) {
		t.Fatalf("among them: %v", z)
	}
	// paged: 10 photos at 4 a page; the one without a time is not in the table
	if st := ut.u.Stand(); st.Fotos != 9 || st.Gelesen != 10 || st.Laeuft || st.Fehler != "" || st.Hinweis != "" || !st.Am.Equal(*ut.jetzt) {
		t.Fatalf("stand: %+v", st)
	}
	// only inside the vault, owner-only; nothing in /data
	fi, err := os.Stat(ut.innen.umfeldTabelle())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the table: %v %v", fi, err)
	}
	filepath.WalkDir(ut.daten, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("something of the net outside the vault: %s", p)
		}
		return nil
	})
	// read only: Immich and Radicale got reads
	for _, a := range ut.immich.anfragen {
		if !strings.HasPrefix(a, "GET ") && a != "POST /api/search/metadata" {
			t.Errorf("Immich got %s", a)
		}
	}
	meth, _ := ut.rad.gesehen()
	for _, m := range meth {
		if !strings.HasPrefix(m, "REPORT ") && !strings.HasPrefix(m, "PROPFIND ") && !strings.HasPrefix(m, "GET ") {
			t.Errorf("Radicale got %s", m)
		}
	}
}

// Homes' numbers to the sixth place: every tie of the fixture with its
// strength, rank, days and first year, as Homes' internal/netz computed
// them on 2026-11-10 (71f9de0) from the same photos and cards.
func TestNetzZahlenWieHomes(t *testing.T) {
	n := netzBox(t).u.Netz()
	want := map[string][4]float64{
		"Anna Berg–Carla Wolf": {0.4, 0.4, 0, 0},
		"Anna Berg–Ben Berg":   {5.994137, 5.844005, 3, 2019},
		"Anna Berg–Fritz Kurz": {3, 3, 0, 0},
		"Anna Berg–Kim Berg":   {0.865264, 0.506535, 2, 2019},
		"Ben Berg–Kim Berg":    {0.865264, 0.551432, 2, 2019},
		"Fritz Kurz–Kim Berg":  {1.673149, 1.211932, 2, 2024},
	}
	got := map[string][4]float64{}
	for id := range n.kanten {
		for _, v := range n.Nahe(id, 20) {
			a, _ := n.Person(id)
			if a.Name < v.Mit.Name {
				got[a.Name+"–"+v.Mit.Name] = [4]float64{math.Round(v.Kante.Staerke*1e6) / 1e6, math.Round(v.Kante.Rang*1e6) / 1e6, float64(v.Kante.Tage), float64(v.Kante.Seit)}
			}
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

// Rang divides by how often both appear at all: Kim, on more events
// without Anna than with her, comes after Ben. A big group counts little
// per pair: two days of a ten-person photo are less than half of two
// days as a pair.
func TestNetzGruppeUndHub(t *testing.T) {
	n := netzBox(t).u.Netz()
	var ben, kim Kante
	for _, v := range n.Nahe("p-anna", 10) {
		switch v.Mit.ID {
		case "p-ben":
			ben = v.Kante
		case "p-kim":
			kim = v.Kante
		}
	}
	if kim.Tage != 2 || kim.Rang >= ben.Rang {
		t.Fatalf("kim %+v, ben %+v", kim, ben)
	}
	viele := []string{"p-anna", "p-ben", "p-kim", "p-fritz", "p-gerd", "x1", "x2", "x3", "x4", "x5"}
	n.st.Namen["x1"], n.st.Namen["x2"], n.st.Namen["x3"], n.st.Namen["x4"], n.st.Namen["x5"] = "X1", "X2", "X3", "X4", "X5"
	n.st.Fotos = []netzFoto{{T: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC), Wer: viele}, {T: time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC), Wer: viele},
		{T: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC), Stadt: "Rom", Wer: []string{"x1", "x2"}}, {T: time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC), Stadt: "Rom", Wer: []string{"x1", "x2"}}}
	n.bauen()
	x := n.Nahe("x1", 10)
	if x[0].Mit.ID != "x2" || x[1].Kante.Staerke >= x[0].Kante.Staerke/2 {
		t.Fatalf("x1: %v %+v", namenVon(x), x[:2])
	}
}

// Every round rebuilds from Immich as it is: a person gone is gone (no
// drift); a reopened vault starts from the table it kept.
func TestUmfeldJedeRundeNeu(t *testing.T) {
	ut := netzBox(t)
	ut.immich.setzen(ut.immich.fotos[3:])
	*ut.jetzt = ut.jetzt.Add(time.Hour)
	ut.u.Runde(t.Context(), ut.innen)
	n := ut.u.Netz()
	if got := n.Nahe("p-anna", 10)[0]; got.Mit.ID != "p-ben" || got.Kante.Tage != 0 || n.Gruende("p-anna", got.Kante) != "gleiche Adresse · Ehepartner/in (Telefonbuch)" { // one day left: the phone book's only
		t.Fatalf("after a round: %+v", got)
	}
	again := &Umfeld{URL: ut.u.URL, Loc: time.UTC, Now: ut.u.Now}
	again.laden(ut.innen)
	if got := namenVon(again.Netz().Nahe("p-anna", 10)); !slices.Equal(got, namenVon(n.Nahe("p-anna", 10))) {
		t.Fatalf("from the vault: %v", got)
	}
}

// Ausblenden hides a person where it would show, restorable; Nicht
// berechnen takes it out before any tie, the others' ties among
// themselves too; both are kept in the vault and come back.
func TestNetzVerbergen(t *testing.T) {
	ut := netzBox(t)
	u, n := ut.u, ut.u.Netz()
	if err := u.Verbergen(ut.innen, "p-ben", verbergenAus); err != nil {
		t.Fatal(err)
	}
	if got := namenVon(n.Nahe("p-anna", 10)); slices.Contains(got, "Ben Berg") || len(n.Nahe("p-ben", 10)) != 0 {
		t.Fatalf("ben shown: %v", got)
	}
	if err := u.Verbergen(ut.innen, "p-kim", verbergenNicht); err != nil {
		t.Fatal(err)
	}
	if got := namenVon(n.Nahe("p-fritz", 10)); slices.Contains(got, "Kim Berg") || len(n.kanten["p-kim"]) != 0 {
		t.Fatalf("kim computed: %v", got)
	}
	again := &Umfeld{URL: u.URL, Loc: time.UTC, Now: u.Now}
	again.laden(ut.innen)
	if v := again.Netz().Verborgene(); len(v) != 2 || v[0].Name != "Ben Berg" || v[0].Art != verbergenAus || v[1].Art != verbergenNicht {
		t.Fatalf("kept: %+v", v)
	}
	ut.u.Runde(t.Context(), ut.innen) // a new round keeps them hidden
	if got := namenVon(ut.u.Netz().Nahe("p-anna", 10)); slices.Contains(got, "Ben Berg") {
		t.Fatalf("a round brought ben back: %v", got)
	}
	again.Verbergen(ut.innen, "p-kim", "")
	again.Verbergen(ut.innen, "p-ben", "")
	if got := namenVon(again.Netz().Nahe("p-anna", 10)); got[0] != "Ben Berg" || !slices.Contains(got, "Kim Berg") || len(again.Netz().Verborgene()) != 0 {
		t.Fatalf("back: %v", got)
	}
	if fi, err := os.Stat(ut.innen.umfeldVerborgen()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("hidden ones not in the vault: %v %v", fi, err)
	}
}

// relationen reads what phones write: iOS's grouped label, Android's
// custom row with a type or its own label, vCard 4's RELATED.
func TestRelationen(t *testing.T) {
	text := karteMit("x", "X", "item2.X-ABRELATEDNAMES:Ute\r\nitem2.X-ABLABEL:Patentante\r\nitem3.X-ABRELATEDNAMES:Max\r\nitem3.X-ABLabel:_$!<Mother>!$_\r\n"+
		"X-ANDROID-CUSTOM:vnd.android.cursor.item/relation;Lea;0;Nichte;;;;;;;;;;;;\r\nRELATED;TYPE=child,contact;VALUE=text:Tom\r\n")
	ks := kartenLesen("privat/", text)
	if len(ks) != 1 {
		t.Fatalf("%d cards", len(ks))
	}
	var got []string
	for _, r := range ks[0].rel {
		got = append(got, r[0]+"="+beziehungsWort(r[1]))
	}
	if want := []string{"Ute=Patentante", "Max=Mutter", "Lea=Nichte", "Tom=Kind"}; !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

// The household book the archive may not read yet (the laib rights come
// separately): the net is made of the photos and the phone's book, and
// the page says plainly what is missing.
func TestUmfeldOhneHaushaltsbuch(t *testing.T) {
	ut := netzBox(t)
	ut.rad.mu.Lock()
	ut.rad.haushalt = nil
	ut.rad.mu.Unlock()
	ut.u.Runde(t.Context(), ut.innen)
	if st := ut.u.Stand(); st.Hinweis != saysHaushaltNichtLesen || st.Fehler != "" {
		t.Fatalf("%+v", st)
	}
	// the cards last read stay; a fresh vault has none of them
	if got := ut.u.Netz().Gruende("p-anna", ut.u.Netz().Nahe("p-anna", 1)[0].Kante); !strings.Contains(got, "gleiche Adresse") {
		t.Fatalf("the household's cards last read went: %q", got)
	}
	os.Remove(ut.innen.umfeldTabelle())
	ut.u.Runde(t.Context(), ut.innen)
	n := ut.u.Netz()
	if got := namenVon(n.Nahe("p-anna", 10)); !slices.Contains(got, "Fritz Kurz") || slices.Contains(got, "Carla Wolf") {
		t.Fatalf("photos and the phone's book: %v", got)
	}
}

// What a wrong or too narrow key, Immich out of reach and a key gone
// later say, in German; the key itself never reaches the log or a page.
func TestUmfeldSchluesselPruefen(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	im := neuesImmich(t)
	wt := neuesWeb(t)
	wt.w.Umfeld = &Umfeld{URL: im.URL}
	if rec := wt.rufe("POST", "/tresor/umfeld/schluessel", anna, url.Values{"schluessel": {im.key}}); rec.Code != http.StatusLocked {
		t.Fatalf("closed: %d", rec.Code)
	}
	wt.oeffnen(t)
	innen, _ := wt.tt.Innen()
	for _, fall := range []struct {
		key    string
		rechte []string
		code   int
		satz   string
	}{
		{"zu kurz", nil, 400, saysImmichSchluesselFormat},
		{"falscher-schluessel-0123456789", nil, 400, saysImmichSchluesselFalsch},
		{im.key, []string{"asset.read"}, 400, "„Fotos ansehen“ (asset.view), „Personen lesen“ (person.read)"},
		{im.key, []string{"all"}, 303, ""},
	} {
		im.mu.Lock()
		im.rechte = fall.rechte
		im.mu.Unlock()
		rec := wt.rufe("POST", "/tresor/umfeld/schluessel", anna, url.Values{"schluessel": {" " + fall.key + "\n"}})
		if rec.Code != fall.code || !strings.Contains(rec.Body.String(), esc(fall.satz)) {
			t.Fatalf("%q: %d %.400s", fall.key, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), im.key) {
			t.Fatal("the key on a page")
		}
	}
	b, err := os.ReadFile(innen.ImmichSchluessel())
	if err != nil || string(b) != im.key {
		t.Fatalf("not in the vault: %q %v", b, err)
	}
	if fi, _ := os.Stat(innen.ImmichSchluessel()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	page := wt.rufe("GET", "/tresor/quellen?m=umfeld", anna, nil).Body.String()
	if !strings.Contains(page, "Immich-Schlüssel hinterlegt am") || !strings.Contains(page, esc(saysUmfeldGespeichert)) || strings.Contains(page, im.key) {
		t.Fatalf("the vault page: %s", page)
	}
	im.Close() // out of reach
	if rec := wt.rufe("POST", "/tresor/umfeld/schluessel", anna, url.Values{"schluessel": {im.key}}); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), esc(saysImmichWeg)) {
		t.Fatalf("out of reach: %d %.300s", rec.Code, rec.Body)
	}
	if strings.Contains(buf.String(), im.key) {
		t.Fatalf("the key in the log:\n%s", buf.String())
	}
	if _, err := os.Stat(filepath.Join(wt.data, "immich-schluessel")); err == nil {
		t.Fatal("the key outside the vault")
	}
}

// A key that later loses a right, or is deleted in Immich, is said on
// the page; the last net stays.
func TestUmfeldSchluesselSpaeterUngueltig(t *testing.T) {
	ut := netzBox(t)
	ut.immich.mu.Lock()
	ut.immich.rechte = []string{"asset.view", "person.read"}
	ut.immich.mu.Unlock()
	ut.u.Runde(t.Context(), ut.innen)
	if st := ut.u.Stand(); !strings.Contains(st.Fehler, "(asset.read)") || st.Fotos != 9 || ut.u.Netz().Nahe("p-anna", 1)[0].Kante.Tage != 3 {
		t.Fatalf("%+v", st)
	}
	ut.immich.mu.Lock()
	ut.immich.key = "ein-anderer-schluessel-0123456789"
	ut.immich.mu.Unlock()
	ut.u.Runde(t.Context(), ut.innen)
	if st := ut.u.Stand(); st.Fehler != saysImmichNichtMehr {
		t.Fatalf("%+v", st)
	}
}

// The page: „Steht dir nahe“ with the picture and the ranked list and
// its reasons, another in the middle, hiding from it; closed, the locked
// page; the status carries only when.
func TestUmfeldSeite(t *testing.T) {
	ut := netzBox(t)
	wt := neuesWeb(t)
	wt.oeffnen(t)
	innen, _ := wt.tt.Innen()
	for _, d := range []string{"umfeld", "kontakte"} {
		os.MkdirAll(filepath.Join(innen.Wurzel, d), 0o700)
	}
	for _, p := range [][2]string{{ut.innen.umfeldTabelle(), innen.umfeldTabelle()}, {kontakteDatei(ut.innen), kontakteDatei(innen)}, {ut.innen.ImmichSchluessel(), innen.ImmichSchluessel()}} {
		b, _ := os.ReadFile(p[0])
		os.WriteFile(p[1], b, 0o600)
	}
	ut.u.Geaendert = nil
	wt.w.Umfeld = ut.u
	ut.u.laden(innen)

	page := wt.rufe("GET", "/tresor/umfeld", anna, nil).Body.String()
	for _, want := range []string{"<h3>Steht dir nahe</h3>", `<svg class=netzbild`, `<a class=zeile href="/tresor/umfeld?mitte=p-ben">Ben Berg <span class=k>3 Tage auf deinen Fotos seit 2019 · gleiche Adresse · Ehepartner/in (Telefonbuch)</span></a>`,
		"Zuletzt gerechnet um", `id=umfeld-netz data-live`} {
		if !strings.Contains(page, want) {
			t.Fatalf("%q missing:\n%s", want, page)
		}
	}
	if strings.Contains(page, `action="/tresor/umfeld/verbergen"`) || strings.Contains(page, `value="nicht"`) {
		t.Fatal("an action on a row of the list (owner 2026-11-11: „dicke Finger“)")
	}
	if i, j := strings.Index(page, ">Ben Berg <span"), strings.Index(page, ">Kim Berg <span"); i < 0 || j < i {
		t.Fatal("not ranked")
	}
	ben := wt.rufe("GET", "/tresor/umfeld?mitte=p-ben", anna, nil).Body.String()
	for _, want := range []string{`<div class=aktionen>`, `name=id value="p-ben"><button name=art value="aus"`, `<button class=leise name=art value="nicht"`, "Beides lässt sich zurückholen."} {
		if !strings.Contains(ben, want) {
			t.Fatalf("Ben's detail lacks %q:\n%s", want, ben)
		}
	}
	if !strings.Contains(css, ".aktionen{display:flex;flex-wrap:wrap;gap:24px") || !strings.Contains(css, ".nahe a.zeile{display:block;min-height:44px") {
		t.Fatal("the targets are not big and apart")
	}
	fritz := wt.rufe("GET", "/tresor/umfeld?mitte=p-fritz", anna, nil).Body.String()
	if !strings.Contains(fritz, "<h3>Steht Fritz Kurz nahe</h3>") || !strings.Contains(fritz, "Wieder dich in die Mitte") {
		t.Fatalf("fritz in the middle:\n%s", fritz)
	}
	rec := wt.rufe("POST", "/tresor/umfeld/verbergen", anna, url.Values{"id": {"p-ben"}, "art": {verbergenAus}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<div class=toast role=status><span>✓ Ben Berg ausgeblendet</span>") || !strings.Contains(rec.Body.String(), `<input type=hidden name=art value=""><button data-laeuft="Holt zurück …">Rückgängig</button>`) ||
		!strings.Contains(rec.Body.String(), "<h3>Steht dir nahe</h3>") || !strings.Contains(rec.Body.String(), "<h3>Ausgeblendet</h3>") || strings.Contains(rec.Body.String(), ">Ben Berg <span") {
		t.Fatalf("hide: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(innen.umfeldVerborgen()); !strings.Contains(string(b), "p-ben") {
		t.Fatalf("not kept in the vault: %s", b)
	}
	rec = wt.rufe("POST", "/tresor/umfeld/verbergen", anna, url.Values{"id": {"p-ben"}, "art": {""}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Ben Berg ist wieder da.") || !strings.Contains(rec.Body.String(), "<h3>Steht Ben Berg nahe</h3>") {
		t.Fatalf("Rückgängig: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(innen.umfeldVerborgen()); strings.Contains(string(b), "p-ben") {
		t.Fatalf("still hidden: %s", b)
	}
	if rec := wt.rufe("POST", "/tresor/umfeld/verbergen", anna, url.Values{"id": {"p-ben"}, "art": {"alles"}}); rec.Code != 400 {
		t.Fatalf("an unknown art: %d", rec.Code)
	}
	vault := wt.rufe("GET", "/tresor/quellen", anna, nil).Body.String()
	if !strings.Contains(vault, `href="/tresor/umfeld">Umfeld ansehen`) || !strings.Contains(vault, "Immich-Schlüssel hinterlegt am") {
		t.Fatalf("the vault page's card:\n%s", vault)
	}
	st := statusBauen(wt.w.lage(wt.tt.Stand()))
	b, _ := json.Marshal(st)
	if st.UmfeldAm == nil || strings.Contains(string(b), "Berg") {
		t.Fatalf("status: %s", b)
	}

	wt.tt.Abschliessen(context.Background(), "test")
	if rec := wt.rufe("GET", "/tresor/umfeld", anna, nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/tresor" {
		t.Fatalf("closed: %d %v", rec.Code, rec.Header())
	}
	if rec := wt.rufe("POST", "/tresor/umfeld/verbergen", anna, url.Values{"id": {"p-ben"}, "art": {""}}); rec.Code != http.StatusLocked {
		t.Fatalf("closed hide: %d", rec.Code)
	}
}

// Without IMMICH_URL Umfeld is not set up, and both pages say so.
func TestUmfeldNichtEingerichtet(t *testing.T) {
	t.Setenv("IMMICH_URL", "")
	if immichURL() != "" {
		t.Fatal("set up without a URL")
	}
	t.Setenv("IMMICH_URL", "ftp://immich")
	if immichURL() != "" {
		t.Fatal("not http")
	}
	t.Setenv("IMMICH_URL", "http://host.containers.internal:2283/")
	if immichURL() != "http://host.containers.internal:2283" {
		t.Fatal(immichURL())
	}
	wt := neuesWeb(t)
	wt.w.Umfeld = &Umfeld{}
	wt.oeffnen(t)
	for _, p := range []string{"/tresor", "/tresor/umfeld"} {
		if body := wt.rufe("GET", p, anna, nil).Body.String(); !strings.Contains(body, esc(saysImmichNichtEingerichtet)) {
			t.Fatalf("%s:\n%s", p, body)
		}
	}
	if rec := wt.rufe("POST", "/tresor/umfeld/schluessel", anna, url.Values{"schluessel": {"immich-key-0123456789abcdef"}}); rec.Code != http.StatusConflict {
		t.Fatalf("%d", rec.Code)
	}
}

// The background: a round at the opening (with the key), the net while
// open, gone from memory when the vault closes; a new key asks for a
// round at once.
func TestUmfeldLaufEndetMitDemTresor(t *testing.T) {
	ut := netzBox(t)
	u := &Umfeld{URL: ut.u.URL, Radicale: ut.u.Radicale, Loc: time.UTC, Takt: time.Hour, Now: ut.u.Now}
	runden, lief := 0, false
	var mu sync.Mutex
	u.Geaendert = func() { // counts the rounds begun
		l := u.Stand().Laeuft
		mu.Lock()
		if l && !lief {
			runden++
		}
		lief = l
		mu.Unlock()
	}
	os.Remove(ut.innen.umfeldTabelle())
	ctx, stop := context.WithCancel(context.Background())
	fertig := make(chan struct{})
	go func() { defer close(fertig); u.Lauf(ctx, ut.innen) }()
	warte := func(was string, ok func() bool) {
		t.Helper()
		bis := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(bis) {
				t.Fatalf("waited for %s", was)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	warte("the first round", func() bool { return u.Netz() != nil && !u.Stand().Laeuft })
	u.Jetzt()
	warte("a second round", func() bool { mu.Lock(); defer mu.Unlock(); return runden == 2 })
	stop()
	<-fertig
	if u.Netz() != nil || u.Stand().Am.IsZero() {
		t.Fatalf("after closing: %v %+v", u.Netz() != nil, u.Stand())
	}
}

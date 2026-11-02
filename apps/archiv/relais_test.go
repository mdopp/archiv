package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// msgvault rewrites its config.toml itself (BurntSushi's encoder: tables
// indented, arrays inline). The pin is set in [carddav], replacing an
// older one, and nothing else moves.
func TestTrustIsPinnedInMsgvaultsConfig(t *testing.T) {
	cfg := t.TempDir() + "/config.toml"
	os.WriteFile(cfg, []byte(`[server]
  bind_addr = "127.0.0.1"
  api_port = 8090

[carddav]
  provider = ""
  base_url = "https://kontakte.archiv.invalid:18443/"
  username = "msgvault"
  enabled = true
  trusted_origin = "https://alt.example:1"
  trusted_addresses = [
    "10.0.0.1",
  ]

[telemetry]
  enabled = false
`), 0o600)
	if err := vertrauenSetzen(cfg, "https://kontakte.archiv.invalid:18443", "192.168.178.20"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cfg)
	s := string(b)
	for _, want := range []string{`trusted_origin = "https://kontakte.archiv.invalid:18443"`, `trusted_addresses = ["192.168.178.20"]`, `base_url = "https://kontakte.archiv.invalid:18443/"`, "[telemetry]\n  enabled = false", `api_port = 8090`} {
		if !strings.Contains(s, want) {
			t.Fatalf("lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "10.0.0.1") || strings.Contains(s, "alt.example") || strings.Count(s, "trusted_origin") != 1 {
		t.Fatalf("the old pin stayed:\n%s", s)
	}
	// a config without [carddav] (the archive's own, before add-carddav) gets the table
	os.WriteFile(cfg, []byte(msgvaultConfig(8090)), 0o600)
	vertrauenSetzen(cfg, "https://kontakte.archiv.invalid:18443", "10.88.0.5")
	b, _ = os.ReadFile(cfg)
	if !strings.Contains(string(b), "[carddav]\ntrusted_origin = \"https://kontakte.archiv.invalid:18443\"\ntrusted_addresses = [\"10.88.0.5\"]") || !strings.Contains(string(b), "api_key_file") {
		t.Fatalf("%s", b)
	}
}

// The extra CA msgvault trusts can sign only the relay's name.
func TestTheRelaysCAIsConstrainedToItsName(t *testing.T) {
	cert, caPEM, err := relaisZertifikat(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	blatt, _ := x509.ParseCertificate(cert.Certificate[0])
	if _, err := blatt.Verify(x509.VerifyOptions{Roots: pool, DNSName: relaisHost}); err != nil {
		t.Fatalf("the relay's certificate: %v", err)
	}
	blk, _ := pem.Decode(caPEM)
	ca, _ := x509.ParseCertificate(blk.Bytes)
	if !ca.PermittedDNSDomainsCritical || len(ca.PermittedDNSDomains) != 1 || ca.PermittedDNSDomains[0] != relaisHost {
		t.Fatalf("name constraint: %v", ca.PermittedDNSDomains)
	}
}

func TestThePrivateAddressMsgvaultMayDial(t *testing.T) {
	r := &Relais{Adressen: func() ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("169.254.1.2"), netip.MustParseAddr("fd00::5"), netip.MustParseAddr("203.0.113.9"), netip.MustParseAddr("10.88.0.7")}, nil
	}}
	if a, err := r.privateAdresse(); err != nil || a.String() != "10.88.0.7" {
		t.Fatalf("%v %v", a, err)
	}
	r.Adressen = func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("169.254.1.2")}, nil }
	if _, err := r.privateAdresse(); err == nil {
		t.Fatal("link-local is no address msgvault dials")
	}
}

type relaisTest struct {
	rad   *fakeRadicale
	r     *Relais
	innen Innen
	pw    string
}

func neuesRelais(t *testing.T) *relaisTest {
	rt := &relaisTest{rad: neuesRadicale(t), innen: Innen{Wurzel: t.TempDir()}}
	os.MkdirAll(rt.innen.Home(), 0o700)
	os.WriteFile(rt.innen.Home()+"/config.toml", []byte(msgvaultConfig(8090)), 0o600)
	rt.r = &Relais{Person: "anna", Port: 18443, SystemCAs: "/nicht/da",
		Adressen: func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("192.168.1.9")}, nil }}
	if err := rt.r.Vorbereiten(rt.innen); err != nil {
		t.Fatal(err)
	}
	rt.pw = string(rt.r.Passwort())
	k := neueKontaktablage()
	k.Einmischen([]QuellKarte{{Href: "ben.vcf", Daten: vcard("u-ben", "Ben", "+4915199999999")}, {Href: "carla.vcf", Daten: vcard("u-carla", "Carla", "+4917011111111")}}, t0)
	k.Einmischen([]QuellKarte{{Href: "carla.vcf", Daten: vcard("u-carla", "Carla", "+4917011111111")}}, t0.Add(time.Hour)) // Ben deleted on the phone
	if err := k.speichern(kontakteDatei(rt.innen)); err != nil {
		t.Fatal(err)
	}
	return rt
}

func (rt *relaisTest) rufe(method, pfad, depth string, auth bool) *httptest.ResponseRecorder {
	q := httptest.NewRequest(method, pfad, strings.NewReader(`<propfind xmlns="DAV:"><prop><current-user-principal/></prop></propfind>`))
	if auth {
		q.SetBasicAuth(relaisBenutzer, rt.pw)
	}
	if depth != "" {
		q.Header.Set("Depth", depth)
	}
	rec := httptest.NewRecorder()
	rt.r.ServeHTTP(rec, q)
	return rec
}

// what msgvault's discovery reads from an answer (internal/carddav/xml.go)
type entdeckt struct {
	Responses []struct {
		Href  string `xml:"DAV: href"`
		Props []struct {
			Prop struct {
				Principal string `xml:"DAV: current-user-principal>href"`
				HomeSet   struct {
					Hrefs []string `xml:"DAV: href"`
				} `xml:"urn:ietf:params:xml:ns:carddav addressbook-home-set"`
				Typ struct {
					Collection  *struct{} `xml:"DAV: collection"`
					Addressbook *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook"`
				} `xml:"DAV: resourcetype"`
				Rechte []struct {
					Name struct {
						XMLName xml.Name
					} `xml:",any"`
				} `xml:"DAV: current-user-privilege-set>privilege"`
				Berichte []struct {
					Report struct {
						Name struct {
							XMLName xml.Name
						} `xml:",any"`
					} `xml:"DAV: report"`
				} `xml:"DAV: supported-report-set>supported-report"`
			} `xml:"DAV: prop"`
		} `xml:"DAV: propstat"`
	} `xml:"DAV: response"`
}

func lesen(t *testing.T, rec *httptest.ResponseRecorder) entdeckt {
	t.Helper()
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var e entdeckt
	if err := xml.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// RFC 6764 as msgvault follows it: the principal and the home are the
// person's /anna/, and the home holds one book, readable and nothing more.
func TestTheRelayAnswersMsgvaultsDiscovery(t *testing.T) {
	rt := neuesRelais(t)
	if rec := rt.rufe("PROPFIND", "/", "0", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without the relay's password: %d", rec.Code)
	}
	e := lesen(t, rt.rufe("PROPFIND", "/", "0", true))
	if len(e.Responses) != 1 || e.Responses[0].Href != "/" || e.Responses[0].Props[0].Prop.Principal != "/anna/" {
		t.Fatalf("principal: %+v", e)
	}
	e = lesen(t, rt.rufe("PROPFIND", "/anna/", "0", true))
	if len(e.Responses) != 1 || len(e.Responses[0].Props[0].Prop.HomeSet.Hrefs) != 1 || e.Responses[0].Props[0].Prop.HomeSet.Hrefs[0] != "/anna/" {
		t.Fatalf("home set: %+v", e)
	}
	e = lesen(t, rt.rufe("PROPFIND", "/anna/", "1", true))
	if len(e.Responses) != 2 || e.Responses[1].Href != "/anna/handy-kontakte/" {
		t.Fatalf("books: %+v", e)
	}
	buch := e.Responses[1].Props[0].Prop
	if buch.Typ.Addressbook == nil || buch.Typ.Collection == nil {
		t.Fatal("the book is no address book")
	}
	if len(buch.Rechte) != 1 || buch.Rechte[0].Name.XMLName.Local != "read" {
		t.Fatalf("privileges: %+v (msgvault picks a book with bind/write/unbind as its write target)", buch.Rechte)
	}
	if len(buch.Berichte) != 2 {
		t.Fatalf("reports: %+v", buch.Berichte)
	}
	// no card in the archive yet: no book offered
	os.Remove(kontakteDatei(rt.innen))
	if e := lesen(t, rt.rufe("PROPFIND", "/anna/", "1", true)); len(e.Responses) != 1 {
		t.Fatalf("a missing book was offered: %+v", e)
	}
}

// The book msgvault reads is the archive's store: every card, the one
// deleted on the phone marked; nothing is written, Radicale never asked.
func TestTheRelayServesTheStoreAndOnlyReads(t *testing.T) {
	rt := neuesRelais(t)
	q := func(method, pfad, body, depth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, pfad, strings.NewReader(body))
		r.SetBasicAuth(relaisBenutzer, rt.pw)
		if depth != "" {
			r.Header.Set("Depth", depth)
		}
		rec := httptest.NewRecorder()
		rt.r.ServeHTTP(rec, r)
		return rec
	}
	query := `<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/><C:address-data/></D:prop><C:filter/></C:addressbook-query>`
	rec := q("REPORT", "/anna/handy-kontakte/", query, "1")
	body := rec.Body.String()
	if rec.Code != http.StatusMultiStatus || !strings.Contains(body, "FN:Ben") || !strings.Contains(body, "FN:Carla") ||
		!strings.Contains(body, "CATEGORIES:Auf dem Handy gelöscht am ") || !strings.Contains(body, "&#13;") {
		t.Fatalf("query: %d %s", rec.Code, body)
	}
	if strings.Count(body, "gelöscht") != 1 {
		t.Fatal("only Ben is deleted")
	}
	multi := `<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/><C:address-data/></D:prop><D:href>/anna/handy-kontakte/carla.vcf</D:href><D:href>/anna/handy-kontakte/weg.vcf</D:href></C:addressbook-multiget>`
	body = q("REPORT", "/anna/handy-kontakte/", multi, "").Body.String()
	if !strings.Contains(body, "FN:Carla") || strings.Contains(body, "FN:Ben") || !strings.Contains(body, "404") {
		t.Fatalf("multiget: %s", body)
	}
	if rec := q("REPORT", "/anna/handy-kontakte/", `<D:sync-collection xmlns:D="DAV:"><D:sync-token/></D:sync-collection>`, ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("sync-collection: %d (msgvault falls back to a snapshot on 501)", rec.Code)
	}
	if body := q("PROPFIND", "/anna/handy-kontakte/", "", "1").Body.String(); strings.Count(body, "<D:getetag>") != 2 {
		t.Fatalf("listing: %s", body)
	}
	rec = q("GET", "/anna/handy-kontakte/ben.vcf", "", "")
	if rec.Code != 200 || rec.Header().Get("ETag") == "" || !strings.Contains(rec.Body.String(), "gelöscht") {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	for _, m := range []string{"PUT", "DELETE", "PROPPATCH", "MKCOL", "MOVE", "COPY", "POST", "LOCK"} {
		if rec := q(m, "/anna/handy-kontakte/neu.vcf", "x", ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
	for _, p := range []string{"/anna/privat-sport/", "/bob/handy-kontakte/", "/anna/handy-kontakte-alt/", "/anna/handy-kontakte/x/y.vcf"} {
		if rec := q("PROPFIND", p, "", "1"); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if m, _ := rt.rad.gesehen(); len(m) != 0 {
		t.Fatalf("the relay asked Radicale: %v", m)
	}
}

// The whole way msgvault takes: TLS to the name, the certificate checked
// against the bundle written for SSL_CERT_FILE, the relay's password.
func TestTheRelayOverTLSWithTheBundleMsgvaultGets(t *testing.T) {
	rt := neuesRelais(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	rt.r.Port = port
	rt.r.mu.Lock()
	rt.r.adresse = netip.MustParseAddr("127.0.0.1") // msgvault would dial the pinned private address
	rt.r.mu.Unlock()
	ctx, aus := context.WithCancel(context.Background())
	fertig := make(chan error, 1)
	go func() { fertig <- rt.r.Lauf(ctx) }()
	defer func() { aus(); <-fertig }()

	buendel, _ := os.ReadFile(rt.innen.KontakteCA())
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(buendel) {
		t.Fatal("no CA in the bundle")
	}
	c := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: relaisHost},
		DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, n, "127.0.0.1:"+strconv.Itoa(port))
		}}, Timeout: 5 * time.Second}
	var resp *http.Response
	for i := 0; i < 50; i++ {
		q, _ := http.NewRequest("PROPFIND", "https://"+relaisHost+":"+strconv.Itoa(port)+"/", nil)
		q.SetBasicAuth(relaisBenutzer, rt.pw)
		q.Header.Set("Depth", "0")
		if resp, err = c.Do(q); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus || !strings.Contains(string(b), "/anna/") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

// msgvault gets SSL_CERT_FILE only once the relay wrote its bundle.
func TestMsgvaultTrustsTheRelayOnlyWhenItIsThere(t *testing.T) {
	home := t.TempDir()
	if strings.Contains(strings.Join(msgvaultServe("msgvault", home).Env, " "), "SSL_CERT_FILE") {
		t.Fatal("SSL_CERT_FILE without a bundle")
	}
	os.WriteFile(home+"/"+kontakteCAName, []byte("x"), 0o644)
	if !strings.Contains(strings.Join(msgvaultServe("msgvault", home).Env, " "), "SSL_CERT_FILE="+home+"/"+kontakteCAName) {
		t.Fatal("no SSL_CERT_FILE with the bundle")
	}
}

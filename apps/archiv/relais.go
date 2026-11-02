package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The relay between msgvault and Radicale (relais = relay). msgvault's
// CardDAV client (0.22.0) reads an address book only
//   - over HTTPS (with a password it refuses plain HTTP),
//   - at a public address, or at a private one pinned in its config.toml
//     (trusted_origin + trusted_addresses; never loopback, never
//     link-local — host.containers.internal is link-local under podman),
//   - found by RFC 6764 discovery from the login's principal: Radicale
//     answers the archive's own account (archiv-<person>) with that
//     account's home, which holds nothing; the person's book is in /<person>/.
// So the supervisor serves msgvault one address book itself: TLS on a
// private address of the container, a name that resolves nowhere
// (kontakte.archiv.invalid, pinned in msgvault's config), a certificate
// from a CA made at every opening that may sign only that name, the
// discovery answered here (principal and home /<person>/, one book in
// it), and every request on the book passed to Radicale with the archive's
// Radicale login. Only reading passes: PROPFIND, REPORT, GET, HEAD,
// OPTIONS. A PUT, DELETE, PROPPATCH, MKCOL, MOVE … gets 403 here and never
// reaches Radicale — msgvault cannot publish to the person's address
// book, whatever its book roles say. The Radicale password stays in the
// supervisor; msgvault knows only the relay's own password, made once and
// kept in the vault.

const (
	relaisHost     = "kontakte.archiv.invalid" // RFC 2606: resolves nowhere, msgvault dials the pinned address
	relaisBenutzer = "msgvault"
	kontakteCAName = "kontakte-ca.pem"
	systemCAs      = "/etc/ssl/certs/ca-certificates.crt"
)

type Relais struct {
	Radicale  *Radicale
	Port      int                          // KONTAKTE_RELAIS_PORT, in the container's own network namespace
	Adressen  func() ([]netip.Addr, error) // the container's addresses; nil: its interfaces
	SystemCAs string                       // the bundle msgvault trusts besides the relay's CA

	mu       sync.Mutex
	adresse  netip.Addr
	cert     *tls.Certificate
	passwort []byte
}

// BasisURL is what msgvault's add-carddav is given; it stays the same
// across openings (the address behind the name may change, see Vorbereiten).
func (r *Relais) BasisURL() string { return "https://" + relaisHost + ":" + strconv.Itoa(r.Port) + "/" }

func (r *Relais) origin() string { return strings.TrimSuffix(r.BasisURL(), "/") }

// Passwort is the relay's password (a copy) once prepared.
func (r *Relais) Passwort() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.passwort...)
}

// Vorbereiten runs at every opening, before msgvault starts: it picks the
// container's private address, makes a CA and the relay's certificate,
// writes the CA beside the system's for msgvault (SSL_CERT_FILE), pins
// the name to the address in msgvault's config.toml, and loads (or makes)
// the relay's password in the vault.
func (r *Relais) Vorbereiten(innen Innen) error {
	addr, err := r.privateAdresse()
	if err != nil {
		return err
	}
	cert, caPEM, err := relaisZertifikat(time.Now())
	if err != nil {
		return fmt.Errorf("Zertifikat: %w", err)
	}
	system := r.SystemCAs
	if system == "" {
		system = systemCAs
	}
	buendel, _ := os.ReadFile(system) // none: msgvault trusts only the relay; it talks to nothing else
	if len(buendel) > 0 && !bytes.HasSuffix(buendel, []byte("\n")) {
		buendel = append(buendel, '\n')
	}
	if err := schreibenAtomar(innen.KontakteCA(), append(buendel, caPEM...), 0o644); err != nil {
		return err
	}
	pw, err := relaisPasswort(innen.KontakteRelais())
	if err != nil {
		return err
	}
	if err := vertrauenSetzen(innen.Home()+"/config.toml", r.origin(), addr.String()); err != nil {
		return fmt.Errorf("msgvault-Konfiguration: %w", err)
	}
	r.mu.Lock()
	r.adresse, r.cert, r.passwort = addr, &cert, pw
	r.mu.Unlock()
	return nil
}

// Lauf serves the relay until ctx ends (the vault closes).
func (r *Relais) Lauf(ctx context.Context) error {
	ln, err := r.Lauschen()
	if err != nil {
		return err
	}
	return r.Dienen(ctx, ln)
}

// Lauschen opens the relay's TLS port on the prepared address.
func (r *Relais) Lauschen() (net.Listener, error) {
	r.mu.Lock()
	addr, cert := r.adresse, r.cert
	r.mu.Unlock()
	if cert == nil {
		return nil, errors.New("das Relais ist nicht vorbereitet")
	}
	ln, err := net.Listen("tcp", netip.AddrPortFrom(addr, uint16(r.Port)).String())
	if err != nil {
		return nil, err
	}
	return tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12}), nil
}

// Dienen serves on ln until ctx ends.
func (r *Relais) Dienen(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: r, ReadHeaderTimeout: 20 * time.Second}
	fertig := make(chan error, 1)
	go func() { fertig <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		ende, aus := context.WithTimeout(context.Background(), 5*time.Second)
		defer aus()
		srv.Shutdown(ende)
		<-fertig
		return nil
	case err := <-fertig:
		return err
	}
}

// privateAdresse is the container's first address in a range msgvault
// accepts as a pinned private destination (IPv4 first).
func (r *Relais) privateAdresse() (netip.Addr, error) {
	quelle := r.Adressen
	if quelle == nil {
		quelle = schnittstellenAdressen
	}
	alle, err := quelle()
	if err != nil {
		return netip.Addr{}, err
	}
	var v6 netip.Addr
	for _, a := range alle {
		a = a.Unmap()
		if !privatFuerMsgvault(a) {
			continue
		}
		if a.Is4() {
			return a, nil
		}
		if !v6.IsValid() {
			v6 = a
		}
	}
	if v6.IsValid() {
		return v6, nil
	}
	return netip.Addr{}, errors.New("der Container hat keine private Adresse (10/8, 172.16/12, 192.168/16, 100.64/10, fc00::/7), an der msgvault das Relais erreichen darf")
}

// privatFuerMsgvault: the ranges msgvault 0.22.0 allows in trusted_addresses
// (internal/netguard/trusted.go).
func privatFuerMsgvault(a netip.Addr) bool {
	for _, p := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7"} {
		if netip.MustParsePrefix(p).Contains(a) && a.Zone() == "" {
			return true
		}
	}
	return false
}

func schnittstellenAdressen() ([]netip.Addr, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				out = append(out, p.Addr())
			}
		}
	}
	return out, nil
}

// relaisZertifikat makes a CA that may sign only relaisHost (a name
// constraint: trusting it lets nobody impersonate another server to
// msgvault) and the relay's certificate. The CA's key is dropped here;
// each opening makes a new pair.
func relaisZertifikat(jetzt time.Time) (tls.Certificate, []byte, error) {
	seriell := func() *big.Int { n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120)); return n }
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	ca := &x509.Certificate{SerialNumber: seriell(), Subject: pkix.Name{CommonName: "Archiv: Relais zum Adressbuch (nur dieser Tresor)"},
		NotBefore: jetzt.Add(-time.Hour), NotAfter: jetzt.Add(90 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign, PermittedDNSDomainsCritical: true, PermittedDNSDomains: []string{relaisHost}}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caKey.Public(), caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	blatt := &x509.Certificate{SerialNumber: seriell(), Subject: pkix.Name{CommonName: relaisHost}, DNSNames: []string{relaisHost},
		NotBefore: jetzt.Add(-time.Hour), NotAfter: jetzt.Add(90 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, blatt, caCert, key.Public(), caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, caPEM, nil
}

// relaisPasswort loads the relay's password from the vault, or makes it.
func relaisPasswort(datei string) ([]byte, error) {
	if b, err := os.ReadFile(datei); err == nil && len(trimNL(b)) >= 32 {
		return trimNL(b), nil
	}
	roh := make([]byte, 32)
	if _, err := rand.Read(roh); err != nil {
		return nil, err
	}
	pw := []byte(hex.EncodeToString(roh))
	if err := schreibenAtomar(datei, pw, 0o600); err != nil {
		return nil, err
	}
	return pw, nil
}

// vertrauenSetzen pins msgvault's CardDAV client to the relay: in the
// [carddav] table of its config.toml (written by msgvault itself once
// add-carddav saved the connection, so edited line by line, not
// rewritten), trusted_origin and trusted_addresses are set to the relay's
// origin and today's address; everything else stays as it is.
func vertrauenSetzen(cfg, origin, adresse string) error {
	b, err := os.ReadFile(cfg)
	if err != nil {
		return err
	}
	unsere := []string{"trusted_origin = " + strconv.Quote(origin), "trusted_addresses = [" + strconv.Quote(adresse) + "]"}
	var out []string
	inCard, eingefuegt, ueberspringen := false, false, false
	for _, z := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		t := strings.TrimSpace(z)
		if ueberspringen { // the rest of a trusted_addresses array over several lines
			if strings.Contains(t, "]") {
				ueberspringen = false
			}
			continue
		}
		if strings.HasPrefix(t, "[") {
			inCard = t == "[carddav]"
			out = append(out, z)
			if inCard {
				out = append(out, unsere[0], unsere[1])
				eingefuegt = true
			}
			continue
		}
		if inCard {
			k, rest, ok := strings.Cut(t, "=")
			k = strings.TrimSpace(k)
			if ok && (k == "trusted_origin" || k == "trusted_addresses") {
				if k == "trusted_addresses" && strings.Contains(rest, "[") && !strings.Contains(rest, "]") {
					ueberspringen = true
				}
				continue
			}
		}
		out = append(out, z)
	}
	if !eingefuegt {
		out = append(out, "", "[carddav]", unsere[0], unsere[1])
	}
	return schreibenAtomar(cfg, []byte(strings.Join(out, "\n")+"\n"), 0o600)
}

// --- what msgvault sees

func (r *Relais) ServeHTTP(w http.ResponseWriter, q *http.Request) {
	user, pw, ok := q.BasicAuth()
	if !ok || user != relaisBenutzer || subtle.ConstantTimeCompare([]byte(pw), r.Passwort()) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="archiv-kontakte"`)
		http.Error(w, "Anmeldung fehlt", http.StatusUnauthorized)
		return
	}
	switch q.Method {
	case http.MethodOptions:
		w.Header().Set("DAV", "1, 2, 3, addressbook")
		w.Header().Set("Allow", "OPTIONS, PROPFIND, REPORT, GET, HEAD")
		return
	case "PROPFIND", "REPORT", http.MethodGet, http.MethodHead:
	default:
		http.Error(w, "Das Archiv liest das Adressbuch nur; schreiben kann msgvault hier nicht.", http.StatusForbidden)
		return
	}
	home := r.Radicale.Home()
	buch := home + adressbuchName + "/"
	p := q.URL.Path
	switch {
	case p == "/" || p == "/.well-known/carddav" || p == "/.well-known/carddav/":
		if q.Method != "PROPFIND" {
			http.NotFound(w, q)
			return
		}
		multistatus(w, davAntwort(p, "<D:resourcetype><D:collection/></D:resourcetype>"+r.prinzipal(home)))
	case p == home || p+"/" == home:
		if q.Method != "PROPFIND" {
			http.NotFound(w, q)
			return
		}
		antworten := davAntwort(home, "<D:resourcetype><D:collection/></D:resourcetype><D:displayname>"+xmlText(r.Radicale.Person)+"</D:displayname>"+r.prinzipal(home))
		if q.Header.Get("Depth") == "1" {
			da, err := r.buchDa(q.Context())
			if err != nil {
				log.Printf("archiv: Relais: Radicale fragen: %v", err)
				http.Error(w, "Radicale antwortet nicht", http.StatusBadGateway)
				return
			}
			if da {
				antworten += davAntwort(buch, `<D:resourcetype><D:collection/><C:addressbook/></D:resourcetype>`+
					`<D:displayname>Handy-Kontakte</D:displayname>`+
					`<D:supported-report-set><D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report>`+
					`<D:supported-report><D:report><C:addressbook-multiget/></D:report></D:supported-report></D:supported-report-set>`+
					`<D:current-user-privilege-set><D:privilege><D:read/></D:privilege></D:current-user-privilege-set>`+
					`<C:supported-address-data><C:address-data-type content-type="text/vcard" version="3.0"/><C:address-data-type content-type="text/vcard" version="4.0"/></C:supported-address-data>`)
			}
		}
		multistatus(w, antworten)
	case strings.HasPrefix(p, buch) || p+"/" == buch:
		r.weiter(w, q)
	default:
		http.NotFound(w, q)
	}
}

func (r *Relais) prinzipal(home string) string {
	return "<D:current-user-principal><D:href>" + xmlText(home) + "</D:href></D:current-user-principal>" +
		"<C:addressbook-home-set><D:href>" + xmlText(home) + "</D:href></C:addressbook-home-set>" +
		"<D:current-user-privilege-set><D:privilege><D:read/></D:privilege></D:current-user-privilege-set>"
}

func (r *Relais) buchDa(ctx context.Context) (bool, error) {
	samml, err := r.Radicale.Sammlungen(ctx)
	if err != nil {
		return false, err
	}
	for _, s := range samml {
		if s.Name == adressbuchName && s.Adressbuch {
			return true, nil
		}
	}
	return false, nil
}

// weiter passes a read on the book to Radicale, with the archive's login
// instead of msgvault's; the paths are Radicale's own (the relay mirrors
// them), so its answer needs no rewriting.
func (r *Relais) weiter(w http.ResponseWriter, q *http.Request) {
	pw, err := r.Radicale.passwort()
	if err != nil {
		log.Printf("archiv: Relais: %v", err)
		http.Error(w, "Radicale-Zugang fehlt", http.StatusBadGateway)
		return
	}
	defer nullen(pw)
	ziel := *r.Radicale.Basis
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&ziel)
			pr.Out.URL.Path, pr.Out.URL.RawPath = q.URL.Path, q.URL.RawPath
			pr.Out.Host = ziel.Host
			for _, h := range []string{"Authorization", "Cookie", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
				pr.Out.Header.Del(h)
			}
			pr.Out.SetBasicAuth(r.Radicale.Benutzer, string(pw))
		},
		Transport: r.Radicale.Client.Transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("archiv: Relais: Radicale: %v", err)
			http.Error(w, "Radicale antwortet nicht", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, q)
}

func davAntwort(href, props string) string {
	return "<D:response><D:href>" + xmlText(href) + "</D:href><D:propstat><D:prop>" + props +
		"</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>"
}

func multistatus(w http.ResponseWriter, antworten string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n"+`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">`+antworten+`</D:multistatus>`)
}

func xmlText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

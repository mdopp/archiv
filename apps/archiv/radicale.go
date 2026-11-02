package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

// Radicale is the box's CalDAV/CardDAV server, read here and only read:
// the person's address book handy-kontakte (Haus mirrors the phone's
// contacts there) and their calendars google-* (laib's kalender mirrors
// the person's Google calendars) and privat-* (made in Haus). The archive
// logs in with its own Radicale account (RADICALE_USER, e.g. archiv-anna),
// which Radicale lets read exactly those collections of exactly this
// person and write nothing (laib templates/radicale/pre-start). Every
// request the archive makes is PROPFIND, REPORT or GET.
type Radicale struct {
	Basis         *url.URL // RADICALE_URL: the server's root, e.g. http://host.containers.internal:5232/
	Benutzer      string   // RADICALE_USER
	PasswortDatei string   // RADICALE_PASSWORD_FILE: the secret as a file, never an environment variable
	Person        string   // the home collection /<person>/
	Client        *http.Client
}

const (
	saysRadicaleFehlt     = "Der Zugang zu deinem Adressbuch und deinen Kalendern ist auf der Box noch nicht eingerichtet. Das macht der Admin der Box."
	saysRadicaleAnmeldung = "Das Archiv kommt an dein Adressbuch und deine Kalender nicht heran: die Box lehnt seine Anmeldung ab. Sag dem Admin der Box Bescheid."
	saysRadicaleWeg       = "Das Archiv erreicht dein Adressbuch und deine Kalender gerade nicht. Es versucht es in ein paar Stunden und beim nächsten Öffnen wieder."
	adressbuchName        = "handy-kontakte"
)

var (
	errRadicaleAnmeldung = errors.New(saysRadicaleAnmeldung)
	errPasswortFehlt     = errors.New("das Radicale-Passwort fehlt")
	sammlungName         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// RadicaleAusUmgebung reads RADICALE_URL, RADICALE_USER and
// RADICALE_PASSWORD_FILE. nil: not set up (the template does not give it
// yet); the page says so. A URL with a path is refused: the hrefs Radicale
// answers with are paths from its root, and the relay for msgvault
// (relais.go) serves the same paths.
func RadicaleAusUmgebung(person string) (*Radicale, error) {
	roh, benutzer := os.Getenv("RADICALE_URL"), os.Getenv("RADICALE_USER")
	datei := env("RADICALE_PASSWORD_FILE", "/run/secrets/radicale-password")
	if roh == "" && benutzer == "" {
		return nil, nil
	}
	u, err := url.Parse(roh)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return nil, fmt.Errorf("RADICALE_URL %q ist keine http(s)-Adresse", roh)
	case u.Path != "" && u.Path != "/":
		return nil, fmt.Errorf("RADICALE_URL %q: Radicale muss an der Wurzel liegen, ohne Pfad", roh)
	case benutzer == "":
		return nil, errors.New("RADICALE_USER fehlt")
	}
	u.Path = "/"
	return &Radicale{Basis: u, Benutzer: benutzer, PasswortDatei: datei, Person: person, Client: &http.Client{Timeout: 2 * time.Minute}}, nil
}

// Home is the person's collection path, /<person>/.
func (r *Radicale) Home() string { return "/" + url.PathEscape(r.Person) + "/" }

func (r *Radicale) passwort() ([]byte, error) {
	b, err := os.ReadFile(r.PasswortDatei)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPasswortFehlt, err)
	}
	b = trimNL(b)
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: %s ist leer", errPasswortFehlt, r.PasswortDatei)
	}
	return b, nil
}

// anfrage sends one read request with the archive's login. Only PROPFIND,
// REPORT and GET: the archive never writes to Radicale.
func (r *Radicale) anfrage(ctx context.Context, method, pfad, depth string, body []byte, kopf http.Header) (*http.Response, error) {
	switch method {
	case "PROPFIND", "REPORT", http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		return nil, fmt.Errorf("Radicale: %s schreibt, das Archiv liest nur", method)
	}
	pw, err := r.passwort()
	if err != nil {
		return nil, err
	}
	defer nullen(pw)
	ziel := *r.Basis
	ziel.Path = pfad
	ziel.RawPath = ""
	var rumpf io.Reader
	if body != nil {
		rumpf = bytes.NewReader(body)
	}
	q, err := http.NewRequestWithContext(ctx, method, ziel.String(), rumpf)
	if err != nil {
		return nil, err
	}
	for k, v := range kopf {
		q.Header[k] = v
	}
	if depth != "" {
		q.Header.Set("Depth", depth)
	}
	if body != nil && q.Header.Get("Content-Type") == "" {
		q.Header.Set("Content-Type", "application/xml; charset=utf-8")
	}
	q.SetBasicAuth(r.Benutzer, string(pw))
	resp, err := r.Client.Do(q)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, errRadicaleAnmeldung
	}
	return resp, nil
}

// Sammlung is one collection in the person's home.
type Sammlung struct {
	Name       string // the last path segment: handy-kontakte, google-arbeit, privat-sport
	Anzeige    string // its displayname, if any
	Kalender   bool
	Adressbuch bool
}

// Gelesen says whether the archive reads this collection: the address
// book handy-kontakte, and the calendars google-* (not google-kontakte,
// a Takeout's address book) and privat-*.
func (s Sammlung) KalenderZumLesen() bool {
	return s.Kalender && s.Name != "google-kontakte" && (strings.HasPrefix(s.Name, "google-") || strings.HasPrefix(s.Name, "privat-"))
}

const propfindSammlungen = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/><D:displayname/></D:prop></D:propfind>`

type davMultistatus struct {
	Responses []struct {
		Href      string `xml:"DAV: href"`
		Propstats []struct {
			Status string `xml:"DAV: status"`
			Prop   struct {
				DisplayName  string `xml:"DAV: displayname"`
				ResourceType struct {
					Calendar    *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
					Addressbook *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook"`
				} `xml:"DAV: resourcetype"`
			} `xml:"DAV: prop"`
		} `xml:"DAV: propstat"`
	} `xml:"DAV: response"`
}

// Sammlungen lists the collections in the person's home that the
// archive's account may read (Radicale leaves out the others).
func (r *Radicale) Sammlungen(ctx context.Context) ([]Sammlung, error) {
	resp, err := r.anfrage(ctx, "PROPFIND", r.Home(), "1", []byte(propfindSammlungen), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound: // nothing made for this person yet
		return nil, nil
	case resp.StatusCode == http.StatusForbidden:
		return nil, errRadicaleAnmeldung
	case resp.StatusCode != http.StatusMultiStatus:
		return nil, fmt.Errorf("Radicale PROPFIND %s: %s", r.Home(), resp.Status)
	}
	var ms davMultistatus
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("Radicale PROPFIND %s: %w", r.Home(), err)
	}
	var out []Sammlung
	for _, a := range ms.Responses {
		p, err := url.PathUnescape(a.Href)
		if err != nil {
			continue
		}
		if u, err := url.Parse(p); err == nil && u.Host != "" {
			p = u.Path
		}
		p = strings.TrimSuffix(p, "/")
		if path.Dir(p) != strings.TrimSuffix(r.Home(), "/") {
			continue // the home itself, or something not directly in it
		}
		s := Sammlung{Name: path.Base(p)}
		if !sammlungName.MatchString(s.Name) {
			continue
		}
		for _, ps := range a.Propstats {
			if !strings.Contains(ps.Status, " 200") {
				continue
			}
			if ps.Prop.DisplayName != "" {
				s.Anzeige = strings.TrimSpace(ps.Prop.DisplayName)
			}
			s.Kalender = s.Kalender || ps.Prop.ResourceType.Calendar != nil
			s.Adressbuch = s.Adressbuch || ps.Prop.ResourceType.Addressbook != nil
		}
		out = append(out, s)
	}
	return out, nil
}

// KalenderHolen fetches a whole calendar collection as one iCalendar
// (Radicale answers a GET on a collection with all of its items).
func (r *Radicale) KalenderHolen(ctx context.Context, name string) ([]byte, error) {
	if !sammlungName.MatchString(name) {
		return nil, fmt.Errorf("Kalendername %q", name)
	}
	resp, err := r.anfrage(ctx, http.MethodGet, r.Home()+name+"/", "", nil, http.Header{"Accept": {"text/calendar"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, errRadicaleAnmeldung
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Radicale GET %s: %s", name, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 256<<20 {
		return nil, fmt.Errorf("Kalender %s ist größer als 256 MiB", name)
	}
	return b, nil
}

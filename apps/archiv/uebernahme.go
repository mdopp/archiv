package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// „Übernehmen“ (archiv#27 step 2): an answer the person gave in the vault
// goes into the address books only through Homes. The archive writes no
// book (Radicale is read only, AGENTS.md); it hands Homes a signed packet
// in a form the person posts with one tap, Homes shows it on its own page
// with where each line goes, and writes only after the person confirms
// there (homes internal/pages/uebernehmen.go).
//
// Where a line goes (owner 2026-11-10: a relation is written where the
// contact lives): both in the household book (homes/haushalt-kontakte) →
// both household cards, each naming the other by UID only; otherwise →
// only the person's own phone, on each end's phone card, by name. A
// household card never gets a name from the person's phone or photos: the
// packet does not even carry a household UID unless both ends have one.
//
// The packet (version 1, a contract with Homes):
//
//	d = base64url(JSON {"person", "bis", "zeilen": [{"x", "y", "art"}]}), no padding
//	s = base64url(HMAC-SHA256(ARCHIV_HOMES_SECRET, "archiv-uebernahme-1." + d)), no padding
//
// x and y are {"name", "haushalt"?, "handy"?} (card UIDs); art is one of
// arten: x is art of y. person is ARCHIV_PERSON: Homes takes the packet
// only from that person's login and checks s with that person's archive
// secret, so neither another person nor a page that never saw the secret
// can make one. bis is at most two hours ahead (uebernahmeGilt); a packet
// is the same for the same answer within an hour, so the live page does
// not swap its forms on every change.

const (
	uebernahmeVersion = "archiv-uebernahme-1."
	uebernahmeGilt    = 2 * time.Hour
	wohinHaushalt     = "haushalt"
	wohinHandy        = "handy"
)

// uebernahmeEnde is one end of a line: its name and its cards' UIDs.
type uebernahmeEnde struct {
	Name     string `json:"name"`
	Haushalt string `json:"haushalt,omitempty"`
	Handy    string `json:"handy,omitempty"`
}

// uebernahmeZeile is one relation: X is Art of Y.
type uebernahmeZeile struct {
	X   uebernahmeEnde `json:"x"`
	Y   uebernahmeEnde `json:"y"`
	Art string         `json:"art"`
}

type uebernahmePaket struct {
	Person string            `json:"person"`
	Bis    time.Time         `json:"bis"`
	Zeilen []uebernahmeZeile `json:"zeilen"`
}

// Uebernahme is an answer as Homes would write it; Wohin "" when it has
// nowhere to go (neither both in the household book nor one on the phone).
type Uebernahme struct {
	Wohin string
	Zeile uebernahmeZeile
}

// karten are node id's card UIDs: its household card's and its phone card's
// (the first of each); n.mu is held.
func (n *Netz) karten(id string) (haushalt, handy string) {
	for _, k := range n.st.Kontakte {
		if n.von[k.Key] != id {
			continue
		}
		if uid, ok := strings.CutPrefix(k.Key, "haushalt/"); ok && haushalt == "" {
			haushalt = uid
		}
		if uid, ok := strings.CutPrefix(k.Key, "privat/"); ok && handy == "" {
			handy = uid
		}
	}
	return haushalt, handy
}

// Uebernahme is x is art of y as Homes would write it.
func (n *Netz) Uebernahme(x, y, art string) Uebernahme {
	if n == nil {
		return Uebernahme{}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.uebernahme(x, y, art)
}

// uebernahme is x is art of y as Homes would write it; n.mu is held.
func (n *Netz) uebernahme(x, y, art string) Uebernahme {
	hx, px := n.karten(x)
	hy, py := n.karten(y)
	z := uebernahmeZeile{X: uebernahmeEnde{Name: n.knoten[x].Name}, Y: uebernahmeEnde{Name: n.knoten[y].Name}, Art: art}
	switch {
	case hx != "" && hy != "":
		z.X.Haushalt, z.Y.Haushalt = hx, hy
		return Uebernahme{Wohin: wohinHaushalt, Zeile: z}
	case px != "" || py != "":
		z.X.Handy, z.Y.Handy = px, py
		return Uebernahme{Wohin: wohinHandy, Zeile: z}
	}
	return Uebernahme{}
}

// uebernahmeSignieren makes the packet of zeilen for person, valid until
// two hours after the start of now's hour.
func uebernahmeSignieren(secret, person string, now time.Time, zeilen ...uebernahmeZeile) (d, s string) {
	b, _ := json.Marshal(uebernahmePaket{Person: person, Bis: now.UTC().Truncate(time.Hour).Add(uebernahmeGilt), Zeilen: zeilen})
	d = base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(uebernahmeVersion + d))
	return d, base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// homesURL is Homes' address: HOMES_URL, else PUBLIC_URL's with its first
// label www (archiv.<domain> → www.<domain>, where Homes lives); "" none.
func homesURL(getenv func(string) string) string {
	raw := getenv("HOMES_URL")
	if raw == "" {
		u, err := url.Parse(getenv("PUBLIC_URL"))
		if err != nil || u.Host == "" {
			return ""
		}
		_, rest, ok := strings.Cut(u.Host, ".")
		if !ok {
			return ""
		}
		raw = u.Scheme + "://www." + rest
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// wohinText says where a line goes, as the page and Homes' page say it.
func wohinText(wohin string) string {
	return map[string]string{wohinHaushalt: "Haushalts-Adressbuch, sieht die Familie", wohinHandy: "nur dein Handy"}[wohin]
}

// uebernehmenKnopf is the one-tap form that hands u to Homes' confirmation
// page; "" when Homes is not connected or the answer has nowhere to go.
func (w *Web) uebernehmenKnopf(u Uebernahme) string {
	if u.Wohin == "" || w.HomesSecret == "" || w.HomesURL == "" {
		return ""
	}
	d, s := uebernahmeSignieren(w.HomesSecret, w.Person, w.jetzt(), u.Zeile)
	return fmt.Sprintf(`<form method=post action="%s" class=knoepfe><input type=hidden name=d value="%s"><input type=hidden name=s value="%s"><button data-laeuft="Öffnet Homes …">Übernehmen</button><span class=k>%s</span></form>`,
		esc(w.HomesURL+"/kontakte/uebernehmen"), esc(d), esc(s), esc("→ "+wohinText(u.Wohin)))
}

func (w *Web) jetzt() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

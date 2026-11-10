package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Eintragen (archiv#27 step 2, owner 2026-11-10): the person's tap on a
// kind in their own open archive saves the answer in the vault and writes
// it into the address books at once — no second page, no second confirm;
// the tap is their consent. The archive writes no book itself (Radicale is
// read only, AGENTS.md): it posts the line to Homes, server to server, on
// Homes' own port from the box (ARCHIV_HOMES_INTERN, laib's template sets
// http://host.containers.internal:8650), never through the public route
// and never through the browser. Rückgängig takes it back the same way.
//
// Where a line goes (owner 2026-11-10: a relation is written where the
// contact lives): both in the household book (homes/haushalt-kontakte) →
// both household cards, each naming the other by UID only; otherwise →
// only the person's own phone, on each end's phone card, by name. A
// household card never gets a name from the person's phone or photos: the
// packet does not even carry a household UID unless both ends have one.
// Neither (a person only on photos): it stays in the vault.
//
// The packet (version 2, a contract with Homes' internal/uebernahme):
//
//	POST <ARCHIV_HOMES_INTERN>/intern/uebernahme, form d, s
//	d = base64url(JSON {"person", "am", "nonce", "weg"?, "zeilen": [{"x", "y", "art"}]}), no padding
//	s = base64url(HMAC-SHA256(ARCHIV_HOMES_SECRET, "archiv-uebernahme-2." + d)), no padding
//
// x and y are {"name", "haushalt"?, "handy"?} (card UIDs); art is one of
// arten: x is art of y. person is ARCHIV_PERSON: Homes checks s with that
// person's archive secret, so this archive writes only for its person. am
// is now (Homes takes it for two minutes), the nonce is new every time
// (Homes takes it once), weg takes the lines back.

const (
	uebernahmeVersion = "archiv-uebernahme-2."
	uebernahmePfad    = "/intern/uebernahme"
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
	Am     time.Time         `json:"am"`
	Nonce  string            `json:"nonce"`
	Weg    bool              `json:"weg,omitempty"`
	Zeilen []uebernahmeZeile `json:"zeilen"`
}

// Uebernahme is an answer as Homes would write it; Wohin "" when it has
// nowhere to go (neither both in the household book nor one on the phone),
// and then Ohne names the ends in no address book.
type Uebernahme struct {
	Wohin string
	Zeile uebernahmeZeile
	Ohne  []string
}

// Eingetragen is what the vault keeps of an answer Homes wrote: where, the
// line as written (Rückgängig sends it back), when.
type Eingetragen struct {
	Wohin string          `json:"wohin"`
	Zeile uebernahmeZeile `json:"zeile"`
	Am    time.Time       `json:"am"`
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
	u := Uebernahme{}
	for _, e := range []struct{ name, haushalt string }{{z.X.Name, hx}, {z.Y.Name, hy}} {
		if e.haushalt == "" {
			u.Ohne = append(u.Ohne, e.name)
		}
	}
	return u
}

// uebernahmeSignieren makes the packet of zeilen for person at now.
func uebernahmeSignieren(secret, person string, now time.Time, weg bool, zeilen ...uebernahmeZeile) (d, s string) {
	nonce := make([]byte, 16)
	rand.Read(nonce)
	b, _ := json.Marshal(uebernahmePaket{Person: person, Am: now.UTC(), Nonce: hex.EncodeToString(nonce), Weg: weg, Zeilen: zeilen})
	d = base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(uebernahmeVersion + d))
	return d, base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// homesIntern is Homes' address on the box (ARCHIV_HOMES_INTERN), "" none.
func homesIntern(getenv func(string) string) string {
	u, err := url.Parse(strings.TrimSpace(getenv("ARCHIV_HOMES_INTERN")))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

var errKeinHomes = errors.New("Homes ist mit dem Archiv nicht verbunden")

// homesVerbunden: the archive can write into Homes.
func (w *Web) homesVerbunden() bool { return w.HomesSecret != "" && w.HomesIntern != "" }

// homesSchreiben posts z to Homes (weg: takes it back), within ten seconds.
func (w *Web) homesSchreiben(ctx context.Context, z uebernahmeZeile, weg bool) error {
	if !w.homesVerbunden() {
		return errKeinHomes
	}
	ctx, aus := context.WithTimeout(ctx, 10*time.Second)
	defer aus()
	d, s := uebernahmeSignieren(w.HomesSecret, w.Person, w.jetzt(), weg, z)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.HomesIntern+uebernahmePfad, strings.NewReader(url.Values{"d": {d}, "s": {s}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("Homes %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (w *Web) jetzt() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

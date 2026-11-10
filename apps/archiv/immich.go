package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Immich, read with the person's own API key (archiv#27, step 1): the
// key lives in the vault (Innen.ImmichSchluessel), the person enters it
// on the page, and the archive only reads with it: who is on which photo,
// when and where. The base URL comes from the box (IMMICH_URL); without
// it Umfeld is not set up. The API as Immich 3.3.5 serves it, the same
// calls Homes' internal/immich makes.

const (
	saysImmichNichtEingerichtet = "Umfeld ist auf dieser Box noch nicht eingerichtet: Das Archiv kennt die Adresse von Immich nicht. Das richtet der Admin der Box ein."
	saysImmichSchluesselFormat  = "Das sieht nicht wie ein Immich-API-Schlüssel aus. Kopier ihn in Immich noch einmal ganz, ohne Leerzeichen."
	saysImmichSchluesselFalsch  = "Immich kennt diesen Schlüssel nicht. Leg in Immich einen neuen an und kopier ihn ganz hierher."
	saysImmichWeg               = "Immich antwortet gerade nicht. Versuch es in ein paar Minuten noch einmal."
	saysImmichSchluesselFehlt   = "Für dein Umfeld braucht das Archiv einen API-Schlüssel deines Immich-Kontos. Trag ihn auf der Archiv-Seite unter „Umfeld“ ein."
	saysImmichNichtMehr         = "Immich nimmt deinen Schlüssel nicht mehr an (vielleicht wurde er gelöscht). Leg in Immich einen neuen an und trag ihn unter „Umfeld“ ein."
	saysImmichLesen             = "Die Fotos ließen sich gerade nicht aus Immich lesen. Das Archiv versucht es in einer Stunde wieder; bis dahin bleibt das letzte Umfeld."
)

// umfeldRechte are what the key must allow, each with what it is for,
// as the person reads it.
var umfeldRechte = []struct{ Recht, Wofuer string }{
	{"asset.read", "Fotos und ihre Aufnahmedaten lesen"},
	{"asset.view", "Fotos ansehen"},
	{"person.read", "Personen lesen"},
}

// fehlendeRechte are the needed permissions the key lacks ("all" grants all).
func fehlendeRechte(hat []string) (out []string) {
	if slices.Contains(hat, "all") {
		return nil
	}
	for _, r := range umfeldRechte {
		if !slices.Contains(hat, r.Recht) {
			out = append(out, r.Recht)
		}
	}
	return out
}

// saysRechteFehlen says in German which permissions are missing and how
// to get a key with them.
func saysRechteFehlen(fehlen []string) string {
	var teile []string
	for _, f := range fehlen {
		for _, r := range umfeldRechte {
			if r.Recht == f {
				teile = append(teile, "„"+r.Wofuer+"“ ("+f+")")
			}
		}
	}
	return "Diesem Schlüssel fehlen Rechte: " + strings.Join(teile, ", ") + ". Leg in Immich unter Kontoeinstellungen → API-Schlüssel einen neuen Schlüssel an, kreuz dabei diese Rechte an und trag ihn hier ein."
}

// immichSchluessel takes what the person pasted: no whitespace inside,
// letters, digits, - and _ only.
func immichSchluessel(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) < 20 || len(s) > 200 {
		return nil, errFormat
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return nil, errFormat
		}
	}
	return []byte(s), nil
}

// Immich is the client: the base URL and the person's key.
type Immich struct {
	URL  string
	Key  string
	HTTP *http.Client
}

// immichURL is IMMICH_URL without its trailing slash; "" not set up.
func immichURL() string {
	u := strings.TrimRight(strings.TrimSpace(os.Getenv("IMMICH_URL")), "/")
	if p, err := url.Parse(u); u == "" || err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return ""
	}
	return u
}

// immichFehltRecht is Immich refusing for a permission the key lacks
// (403 „Missing required permission: <recht>“).
type immichFehltRecht struct{ Recht string }

func (e immichFehltRecht) Error() string { return "immich: dem Schlüssel fehlt " + e.Recht }

// errImmichSchluessel: Immich does not know the key (401).
var errImmichSchluessel = errors.New("immich: Schlüssel abgelehnt")

// Rechte are the key's permissions (GET /api/api-keys/me, any key may ask).
func (c *Immich) Rechte(ctx context.Context) ([]string, error) {
	var answer struct{ Permissions []string }
	err := c.json(ctx, http.MethodGet, "/api/api-keys/me", nil, &answer)
	return answer.Permissions, err
}

// Wessen is the e-mail of the key's account (GET /api/users/me; needs
// user.read, which the archive does not ask for: "" when refused).
func (c *Immich) Wessen(ctx context.Context) string {
	var answer struct{ Email string }
	if c.json(ctx, http.MethodGet, "/api/users/me", nil, &answer) != nil {
		return ""
	}
	return strings.TrimSpace(answer.Email)
}

// immichAufnahme is one photo as the net reads it: its id, when it was
// taken (dateTimeOriginal, an instant), where, and who is in it.
type immichAufnahme struct {
	ID   string `json:"id"`
	Exif struct {
		Zeit  string   `json:"dateTimeOriginal"`
		Lat   *float64 `json:"latitude"`
		Lng   *float64 `json:"longitude"`
		Stadt string   `json:"city"`
	} `json:"exifInfo"`
	Personen []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Versteckt bool   `json:"isHidden"`
		Geb       string `json:"birthDate"` // JJJJ-MM-TT, "" none
	} `json:"people"`
}

// immichSeite is how many photos one page asks for: 500, not Immich's
// 1000, so a page with its exif and faces stays well under maxImmich.
const (
	immichSeite      = 500
	immichMaxSeiten  = 400 // 200,000 photos
	maxImmichAntwort = 32 << 20
)

// Aufnahmen are all of the account's photos with at least one person,
// every page (POST /api/search/metadata, withExif, withPeople, page and
// its nextPage; asset.read). gelesen is told the photos read so far.
func (c *Immich) Aufnahmen(ctx context.Context, gelesen func(n int)) (out []immichAufnahme, err error) {
	n := 0
	for page := 1; page > 0 && page <= immichMaxSeiten; {
		var answer struct {
			Assets struct {
				Items []immichAufnahme `json:"items"`
				Next  *string          `json:"nextPage"`
			} `json:"assets"`
		}
		b, _ := json.Marshal(map[string]any{"withExif": true, "withPeople": true, "size": immichSeite, "page": page})
		if err := c.json(ctx, http.MethodPost, "/api/search/metadata", b, &answer); err != nil {
			return nil, err
		}
		for _, a := range answer.Assets.Items {
			if len(a.Personen) > 0 {
				out = append(out, a)
			}
		}
		n += len(answer.Assets.Items)
		if gelesen != nil {
			gelesen(n)
		}
		page = 0
		if answer.Assets.Next != nil {
			page, _ = strconv.Atoi(*answer.Assets.Next)
		}
	}
	return out, nil
}

func (c *Immich) json(ctx context.Context, method, pfad string, body []byte, v any) error {
	var rumpf io.Reader
	if body != nil {
		rumpf = bytes.NewReader(body)
	}
	q, err := http.NewRequestWithContext(ctx, method, c.URL+pfad, rumpf)
	if err != nil {
		return err
	}
	q.Header.Set("x-api-key", c.Key)
	q.Header.Set("Accept", "application/json")
	if body != nil {
		q.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: time.Minute}
	}
	resp, err := hc.Do(q)
	if err != nil {
		return fmt.Errorf("immich %s: %w", pfad, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
	case http.StatusUnauthorized:
		return errImmichSchluessel
	case http.StatusForbidden:
		var answer struct{ Message string }
		json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&answer)
		if recht, ok := strings.CutPrefix(answer.Message, "Missing required permission: "); ok {
			return immichFehltRecht{strings.TrimSpace(recht)}
		}
		return fmt.Errorf("immich %s: %s", pfad, resp.Status)
	default:
		return fmt.Errorf("immich %s: %s", pfad, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxImmichAntwort)).Decode(v)
}

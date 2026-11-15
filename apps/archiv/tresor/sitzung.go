package tresor

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A session (ADR 0001 §3) starts with the person's unlock and carries
// what she granted each program for it. A program gets a short-lived key
// bound to its areas; every use checks the key against the session that
// is open now, so locking, or the session's end, voids every key at once,
// and a key of an earlier session never opens a later one.

var (
	ErrAbgeschlossen    = errors.New("der Tresor ist abgeschlossen, oder der Schlüssel gilt nicht mehr")
	ErrNichtFreigegeben = errors.New("dieser Bereich ist dem Programm in dieser Sitzung nicht freigegeben")
	ErrUnbekannt        = errors.New("diesen Namen gibt es in diesem Bereich nicht")
)

type sitzung struct {
	id         string
	seit, bis  time.Time
	freigaben  Freigaben
	schluessel map[string]*Zugang // by the hash of their token
}

func neueSitzung(jetzt time.Time, dauer time.Duration, f Freigaben) *sitzung {
	kopie := Freigaben{}
	for p, r := range f {
		rr := Rechte{}
		for b, x := range r {
			if x&(Lesen|Schreiben) != 0 && bekannt(b) {
				rr[b] = x & (Lesen | Schreiben)
			}
		}
		if len(rr) > 0 {
			kopie[p] = rr
		}
	}
	return &sitzung{id: zufall(), seit: jetzt, bis: jetzt.Add(dauer), freigaben: kopie, schluessel: map[string]*Zugang{}}
}

func bekannt(b Bereich) bool {
	for _, x := range Bereiche {
		if x == b {
			return true
		}
	}
	return false
}

func zufall() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// SitzungStand is the open session as the page shows it.
type SitzungStand struct {
	Seit, Bis time.Time
	Freigaben Freigaben
}

// Sitzung is the open session; ok false while there is none.
func (t *Tresor) Sitzung() (SitzungStand, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sitzung
	if s == nil {
		return SitzungStand{}, false
	}
	f := Freigaben{}
	for p, r := range s.freigaben {
		rr := Rechte{}
		for b, x := range r {
			rr[b] = x
		}
		f[p] = rr
	}
	return SitzungStand{Seit: s.seit, Bis: s.bis, Freigaben: f}, true
}

// Zugang is one key: a program's, for the areas the person granted it in
// one session, until bis. Its token is what a program outside this
// process will present; it is never written anywhere.
type Zugang struct {
	t        *Tresor
	token    string
	programm string
	sitzung  string
	rechte   Rechte
	bis      time.Time
}

func (z *Zugang) Programm() string { return z.programm }
func (z *Zugang) Token() string    { return z.token }
func (z *Zugang) Bis() time.Time   { return z.bis }

// Schluessel gives programm a new key for what the person granted it in
// this session, at most Konfig.Schluessel long and never past the session.
func (t *Tresor) Schluessel(programm string) (*Zugang, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sitzung
	if s == nil {
		return nil, ErrAbgeschlossen
	}
	r, ok := s.freigaben[programm]
	if !ok {
		return nil, ErrNichtFreigegeben
	}
	jetzt := t.now()
	bis := jetzt.Add(t.k.Schluessel)
	if s.bis.Before(bis) {
		bis = s.bis
	}
	for h, alt := range s.schluessel { // the expired ones go
		if !jetzt.Before(alt.bis) {
			delete(s.schluessel, h)
		}
	}
	z := &Zugang{t: t, token: zufall(), programm: programm, sitzung: s.id, rechte: r, bis: bis}
	s.schluessel[tokenHash(z.token)] = z
	return z, nil
}

// Vorgezeigt is the live key whose token a program presented, or
// ErrAbgeschlossen: an unknown, expired or voided token are the same.
func (t *Tresor) Vorgezeigt(token string) (*Zugang, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sitzung == nil || token == "" {
		return nil, ErrAbgeschlossen
	}
	z := t.sitzung.schluessel[tokenHash(token)]
	if z == nil || !t.now().Before(z.bis) {
		return nil, ErrAbgeschlossen
	}
	return z, nil
}

// gilt checks a key for b and r against the session open now; t.mu is held.
func (z *Zugang) gilt(b Bereich, r Recht) error {
	t := z.t
	if t.sitzung == nil || t.sitzung.id != z.sitzung || !t.now().Before(z.bis) {
		return ErrAbgeschlossen
	}
	if !z.rechte[b].Hat(r) {
		return ErrNichtFreigegeben
	}
	return nil
}

// Darf says whether the key holds b with r now, without an access: for a
// page that offers only what a program may do.
func (z *Zugang) Darf(b Bereich, r Recht) bool {
	z.t.mu.Lock()
	defer z.t.mu.Unlock()
	return z.gilt(b, r) == nil
}

// zugreifen runs tun with the vault's path of name, after the check of
// the key for b and r and the name's area, and keeps the access in the
// protocol, refused or not. tun runs under the access lock: the vault
// does not unmount meanwhile.
func (z *Zugang) zugreifen(b Bereich, r Recht, name string, ordner bool, tun func(pfad string) error) error {
	t := z.t
	t.zugriff.RLock()
	defer t.zugriff.RUnlock()
	t.mu.Lock()
	err := z.gilt(b, r)
	sitzungID := ""
	if t.sitzung != nil {
		sitzungID = t.sitzung.id
	}
	jetzt := t.now()
	t.mu.Unlock()
	rel := sauber(name)
	if err == nil {
		e, ok := bereichVon(rel)
		switch {
		case !ok || e.b != b:
			err = ErrUnbekannt
		case ordner && (!e.ordner || e.name != rel):
			err = ErrUnbekannt
		case !ordner && (nurAlsOrdner[strings.SplitN(rel, "/", 2)[0]] || e.ordner && e.name == rel):
			err = ErrUnbekannt
		}
	}
	t.prot.notieren(Zugriff{Am: jetzt, Sitzung: kurz(sitzungID), Programm: z.programm, Bereich: b, Recht: r.String(), Was: rel, Verweigert: verweigert(err)})
	if err != nil {
		return err
	}
	return tun(t.pfad(rel))
}

func verweigert(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNichtFreigegeben):
		return "nicht freigegeben"
	case errors.Is(err, ErrUnbekannt):
		return "nicht in diesem Bereich"
	}
	return "Schlüssel gilt nicht"
}

func kurz(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Pruefen is an access without a file: data of area b a program takes
// from another program's memory (the net another program computed). It
// checks the key as a read or write would and keeps it in the protocol.
func (z *Zugang) Pruefen(b Bereich, r Recht, was string) error {
	return z.zugreifen(b, r, was, false, func(string) error { return nil })
}

// Lesen reads one file of area b.
func (z *Zugang) Lesen(b Bereich, name string) ([]byte, error) {
	var out []byte
	err := z.zugreifen(b, Lesen, name, false, func(p string) (err error) {
		out, err = os.ReadFile(p)
		return err
	})
	return out, err
}

// Schreiben writes one file of area b whole (atomically, owner-only).
func (z *Zugang) Schreiben(b Bereich, name string, inhalt []byte) error {
	return z.zugreifen(b, Schreiben, name, false, func(p string) error {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		return SchreibenAtomar(p, inhalt, 0o600)
	})
}

// Entfernen removes one file of area b; a missing one is no error.
func (z *Zugang) Entfernen(b Bereich, name string) error {
	return z.zugreifen(b, Schreiben, name, false, func(p string) error {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

// Seit says when a file of area b was written, and false when it is
// missing or empty. It is a read.
func (z *Zugang) Seit(b Bereich, name string) (time.Time, bool) {
	var am time.Time
	da := false
	z.zugreifen(b, Lesen, name, false, func(p string) error {
		fi, err := os.Stat(p)
		if err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
			am, da = fi.ModTime(), true
		}
		return err
	})
	return am, da
}

// Namen are the files in a folder of area b, sorted; none when it is missing.
func (z *Zugang) Namen(b Bereich, ordner string) ([]string, error) {
	var out []string
	err := z.zugreifen(b, Lesen, ordner, true, func(p string) error {
		es, err := os.ReadDir(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		for _, e := range es {
			if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
				out = append(out, e.Name())
			}
		}
		return err
	})
	sort.Strings(out)
	return out, err
}

// Ordner is the path of a folder of area b, for a process that works in
// it (msgvault's imports, the import's work folder). With Schreiben it is
// made. What the process then does there is no single access: the
// protocol keeps that the folder was handed out.
func (z *Zugang) Ordner(b Bereich, r Recht, name string) (string, error) {
	var out string
	err := z.zugreifen(b, r, name, true, func(p string) error {
		out = p
		if r.Hat(Schreiben) {
			return os.MkdirAll(p, 0o700)
		}
		return nil
	})
	return out, err
}

// MsgvaultSchluessel is msgvault's API key, for a request that reads (r
// Lesen) or changes (Schreiben) messages; was names it in the protocol
// (a path, never a query). "" with an error while closed or not granted.
func (z *Zugang) MsgvaultSchluessel(r Recht, was string) (string, error) {
	t := z.t
	t.zugriff.RLock()
	defer t.zugriff.RUnlock()
	t.mu.Lock()
	err := z.gilt(Nachrichten, r)
	key, sitzungID := t.apiKey, ""
	if t.sitzung != nil {
		sitzungID = t.sitzung.id
	}
	if err == nil && (key == "" || !t.bereit) {
		err = ErrAbgeschlossen
	}
	jetzt := t.now()
	t.mu.Unlock()
	t.prot.notieren(Zugriff{Am: jetzt, Sitzung: kurz(sitzungID), Programm: z.programm, Bereich: Nachrichten, Recht: r.String(), Was: "msgvault:" + path.Clean("/"+was), Verweigert: verweigert(err)})
	if err != nil {
		return "", err
	}
	return key, nil
}

// Programm is how a program inside this process holds its keys: each
// access takes the current key, or a new one when it is near its end,
// always within what the person granted the program in this session.
type Programm struct {
	t    *Tresor
	name string
	mu   sync.Mutex
	z    *Zugang
}

// Programm is the handle of the program called name. It grants nothing:
// what it may do is what the person granted name for the open session.
func (t *Tresor) Programm(name string) *Programm { return &Programm{t: t, name: name} }

func (p *Programm) Name() string { return p.name }

// Schluessel is the program's key now.
func (p *Programm) Schluessel() (*Zugang, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if z := p.z; z != nil {
		p.t.mu.Lock()
		ok := p.t.sitzung != nil && p.t.sitzung.id == z.sitzung && p.t.now().Add(time.Minute).Before(z.bis)
		p.t.mu.Unlock()
		if ok {
			return z, nil
		}
	}
	z, err := p.t.Schluessel(p.name)
	if err != nil {
		p.z = nil
		return nil, err
	}
	p.z = z
	return z, nil
}

// Darf says whether the program holds b with r in this session.
func (p *Programm) Darf(b Bereich, r Recht) bool {
	z, err := p.Schluessel()
	return err == nil && z.Darf(b, r)
}

func (p *Programm) Pruefen(b Bereich, r Recht, was string) error {
	z, err := p.Schluessel()
	if err != nil {
		return p.verweigert(b, r, was, err)
	}
	return z.Pruefen(b, r, was)
}

func (p *Programm) Lesen(b Bereich, name string) ([]byte, error) {
	z, err := p.Schluessel()
	if err != nil {
		return nil, p.verweigert(b, Lesen, name, err)
	}
	return z.Lesen(b, name)
}

func (p *Programm) Schreiben(b Bereich, name string, inhalt []byte) error {
	z, err := p.Schluessel()
	if err != nil {
		return p.verweigert(b, Schreiben, name, err)
	}
	return z.Schreiben(b, name, inhalt)
}

func (p *Programm) Entfernen(b Bereich, name string) error {
	z, err := p.Schluessel()
	if err != nil {
		return p.verweigert(b, Schreiben, name, err)
	}
	return z.Entfernen(b, name)
}

func (p *Programm) Seit(b Bereich, name string) (time.Time, bool) {
	z, err := p.Schluessel()
	if err != nil {
		p.verweigert(b, Lesen, name, err)
		return time.Time{}, false
	}
	return z.Seit(b, name)
}

func (p *Programm) Namen(b Bereich, ordner string) ([]string, error) {
	z, err := p.Schluessel()
	if err != nil {
		return nil, p.verweigert(b, Lesen, ordner, err)
	}
	return z.Namen(b, ordner)
}

func (p *Programm) Ordner(b Bereich, r Recht, name string) (string, error) {
	z, err := p.Schluessel()
	if err != nil {
		return "", p.verweigert(b, r, name, err)
	}
	return z.Ordner(b, r, name)
}

func (p *Programm) MsgvaultSchluessel(r Recht, was string) (string, error) {
	z, err := p.Schluessel()
	if err != nil {
		return "", p.verweigert(Nachrichten, r, "msgvault:"+was, err)
	}
	return z.MsgvaultSchluessel(r, was)
}

// verweigert keeps a refusal without a key in the protocol: a program
// the person granted nothing still leaves a trace of what it tried.
func (p *Programm) verweigert(b Bereich, r Recht, name string, err error) error {
	t := p.t
	t.zugriff.RLock()
	defer t.zugriff.RUnlock()
	t.mu.Lock()
	id := ""
	if t.sitzung != nil {
		id = t.sitzung.id
	}
	jetzt := t.now()
	t.mu.Unlock()
	t.prot.notieren(Zugriff{Am: jetzt, Sitzung: kurz(id), Programm: p.name, Bereich: b, Recht: r.String(), Was: sauber(name), Verweigert: verweigert(err)})
	return err
}

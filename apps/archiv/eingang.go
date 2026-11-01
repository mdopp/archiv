package main

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The inbox (ADR 0029 §4): Haus writes the person's phone backups into
// /eingang/<quelle>/…, still encrypted by the messenger, its partials in
// /eingang/.teil/. A dot-entry is never a backup. A file is imported only
// when it is stable: the same size and mtime for Ruhe, as seen by this
// process (Haus sets the phone's mtime, so the mtime alone says nothing
// about when the file arrived).

// Quellen are the inbox folders, in the order the page shows them.
var Quellen = []string{"signal", "whatsapp", "discord"}

var quellenName = map[string]string{"signal": "Signal", "whatsapp": "WhatsApp", "discord": "Discord"}

// istSicherung says whether a file (its name in the source's folder) is
// a backup of that source: the files the status counts and, for Signal,
// the files that are imported.
func istSicherung(quelle, rel string) bool {
	name := path.Base(rel)
	switch quelle {
	case "signal":
		return strings.HasPrefix(name, "signal-") && strings.HasSuffix(name, ".backup")
	case "whatsapp": // Databases/msgstore.db.crypt15 and the copies Haus keeps beside it (.crypt15.<YYYYMMDD-HHMMSS>); not Media/
		return !strings.HasPrefix(rel, "Media/") && strings.HasPrefix(name, "msgstore") && strings.Contains(name, ".crypt15")
	case "discord":
		return strings.HasSuffix(strings.ToLower(name), ".zip")
	}
	return false
}

// Datei is one backup in the inbox.
type Datei struct {
	Quelle  string
	Pfad    string // absolute
	Rel     string // below the source's folder
	Groesse int64
	Mtime   time.Time
	Seit    time.Time // when it arrived (ctime: the rename into place and Haus' chtimes set it)
}

func (d Datei) schluessel() string { return d.Pfad }

type beobachtung struct {
	groesse int64
	mtime   time.Time
	erst    time.Time
}

// Eingang is the inbox of one person.
type Eingang struct {
	Dir  string
	Ruhe time.Duration
	Now  func() time.Time

	mu      sync.Mutex
	gesehen map[string]beobachtung
}

func (e *Eingang) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Liste is every backup of a source, oldest first (by the phone's mtime,
// then the name).
func (e *Eingang) Liste(quelle string) ([]Datei, error) {
	wurzel := filepath.Join(e.Dir, quelle)
	var out []Datei
	err := filepath.WalkDir(wurzel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == wurzel && os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return nil
		}
		if p != wurzel && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // no symlinks, no devices
			return nil
		}
		rel, _ := filepath.Rel(wurzel, p)
		rel = filepath.ToSlash(rel)
		if !istSicherung(quelle, rel) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		x := Datei{Quelle: quelle, Pfad: p, Rel: rel, Groesse: fi.Size(), Mtime: fi.ModTime(), Seit: fi.ModTime()}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			x.Seit = time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
		}
		out = append(out, x)
		return nil
	})
	if os.IsNotExist(err) {
		err = nil
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Mtime.Equal(out[j].Mtime) {
			return out[i].Mtime.Before(out[j].Mtime)
		}
		return out[i].Rel < out[j].Rel
	})
	return out, err
}

// Stabil is the backups of a source that kept their size and mtime for
// Ruhe since this process first saw them that way, oldest first.
func (e *Eingang) Stabil(quelle string) ([]Datei, error) {
	alle, err := e.Liste(quelle)
	if err != nil {
		return nil, err
	}
	jetzt := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gesehen == nil {
		e.gesehen = map[string]beobachtung{}
	}
	da := map[string]bool{}
	var out []Datei
	for _, d := range alle {
		da[d.schluessel()] = true
		b, ok := e.gesehen[d.schluessel()]
		if !ok || b.groesse != d.Groesse || !b.mtime.Equal(d.Mtime) {
			e.gesehen[d.schluessel()] = beobachtung{groesse: d.Groesse, mtime: d.Mtime, erst: jetzt}
			continue
		}
		if jetzt.Sub(b.erst) >= e.Ruhe {
			out = append(out, d)
		}
	}
	for k := range e.gesehen {
		if !da[k] {
			delete(e.gesehen, k)
		}
	}
	return out, nil
}

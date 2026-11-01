package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- the inbox

func legen(t *testing.T, dir, rel string, groesse int, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(strings.Repeat("x", groesse)), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mtime, mtime)
	return p
}

var tag = time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)

func TestEingangIgnoresDotEntriesAndOtherFiles(t *testing.T) {
	dir := t.TempDir()
	legen(t, dir, "signal/signal-2026-10-01-03-00-00.backup", 10, tag)
	legen(t, dir, ".teil/0123", 10, tag)                                // Haus' partials
	legen(t, dir, "signal/.signal-2026-10-02-03-00-00.backup", 10, tag) // hidden
	legen(t, dir, "signal/.zwischen/signal-2026-10-03-03-00-00.backup", 10, tag)
	legen(t, dir, "signal/notizen.txt", 10, tag)
	legen(t, dir, "whatsapp/Databases/msgstore.db.crypt15", 10, tag)
	legen(t, dir, "whatsapp/Databases/msgstore.db.crypt15.20261001-030000", 10, tag)
	legen(t, dir, "whatsapp/Media/WhatsApp Images/IMG-1.jpg", 10, tag)
	os.Symlink("/etc/passwd", filepath.Join(dir, "signal", "signal-link.backup"))
	e := &Eingang{Dir: dir}
	l, err := e.Liste("signal")
	if err != nil || len(l) != 1 || l[0].Rel != "signal-2026-10-01-03-00-00.backup" {
		t.Fatalf("signal: %v %+v", err, l)
	}
	w := wartende(e)
	if w["signal"].Wartend != 1 || w["discord"].Wartend != 0 {
		t.Fatalf("wartend: %+v", w)
	}
}

func TestWhatsappBackupsCountedInDatabases(t *testing.T) {
	dir := t.TempDir()
	legen(t, dir, "whatsapp/Databases/msgstore.db.crypt15", 10, tag)
	legen(t, dir, "whatsapp/Databases/msgstore.db.crypt15.20261001-030000", 10, tag)
	legen(t, dir, "whatsapp/Media/WhatsApp Images/IMG-1.jpg", 10, tag)
	if n := wartende(&Eingang{Dir: dir})["whatsapp"].Wartend; n != 2 {
		t.Fatalf("whatsapp backups: %d, want the database and its kept copy", n)
	}
}

func TestEingangOldestFirst(t *testing.T) {
	dir := t.TempDir()
	legen(t, dir, "signal/signal-b.backup", 1, tag.Add(48*time.Hour))
	legen(t, dir, "signal/signal-a.backup", 1, tag.Add(72*time.Hour)) // newer, though its name sorts first
	legen(t, dir, "signal/alt/signal-c.backup", 1, tag)
	l, _ := (&Eingang{Dir: dir}).Liste("signal")
	var got []string
	for _, d := range l {
		got = append(got, d.Rel)
	}
	if strings.Join(got, " ") != "alt/signal-c.backup signal-b.backup signal-a.backup" {
		t.Fatalf("order: %v", got)
	}
}

func TestEingangWaitsUntilAFileIsStable(t *testing.T) {
	dir := t.TempDir()
	u := &uhr{t: tag}
	e := &Eingang{Dir: dir, Ruhe: time.Minute, Now: u.Now}
	p := legen(t, dir, "signal/signal-1.backup", 10, tag)
	if s, _ := e.Stabil("signal"); len(s) != 0 {
		t.Fatal("stable at first sight")
	}
	u.vor(59 * time.Second)
	if s, _ := e.Stabil("signal"); len(s) != 0 {
		t.Fatal("stable before a minute")
	}
	os.WriteFile(p, []byte(strings.Repeat("x", 20)), 0o644) // still growing
	os.Chtimes(p, tag, tag)
	u.vor(30 * time.Second)
	if s, _ := e.Stabil("signal"); len(s) != 0 {
		t.Fatal("a file that changed counted as stable")
	}
	u.vor(59 * time.Second)
	if s, _ := e.Stabil("signal"); len(s) != 0 {
		t.Fatal("stable 59 s after its last change")
	}
	u.vor(time.Second)
	if s, _ := e.Stabil("signal"); len(s) != 1 {
		t.Fatal("not stable a minute after its last change")
	}
}

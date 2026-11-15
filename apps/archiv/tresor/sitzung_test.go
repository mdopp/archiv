package tresor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// protokoll reads the vault's protocol file as it lies in the open vault.
func (tt *testTresor) protokoll(t *testing.T) []Zugriff {
	t.Helper()
	f, err := os.Open(filepath.Join(tt.mnt, protokollName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Zugriff
	s := bufio.NewScanner(f)
	for s.Scan() {
		var z Zugriff
		if err := json.Unmarshal(s.Bytes(), &z); err != nil {
			t.Fatalf("protocol line %q: %v", s.Text(), err)
		}
		out = append(out, z)
	}
	return out
}

func TestAKeyWithoutTheAreaIsRefusedAndTheRefusalIsInTheProtocol(t *testing.T) {
	tt := neuerTresor(t)
	tt.offen(t, Freigaben{"stammbaum": {Beziehungen: Lesen | Schreiben}, "leser": {Beziehungen: Lesen}})
	st, _ := tt.Schluessel("stammbaum")
	if err := st.Schreiben(Beziehungen, "umfeld/netz.json", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("a granted write: %v", err)
	}
	if b, err := st.Lesen(Beziehungen, "umfeld/netz.json"); err != nil || string(b) != `{"a":1}` {
		t.Fatalf("a granted read: %q %v", b, err)
	}
	// the area it was not granted, with a name of that area
	if _, err := st.Lesen(Nachrichten, "signal-passphrase"); !errors.Is(err, ErrNichtFreigegeben) {
		t.Fatalf("Nachrichten without a grant: %v", err)
	}
	if _, err := st.MsgvaultSchluessel(Lesen, "/api/v1/messages"); !errors.Is(err, ErrNichtFreigegeben) {
		t.Fatalf("msgvault without Nachrichten: %v", err)
	}
	if _, err := st.Ordner(Nachrichten, Schreiben, "msgvault"); !errors.Is(err, ErrNichtFreigegeben) {
		t.Fatalf("msgvault's home without Nachrichten: %v", err)
	}
	// a name of another area under the granted area's name
	if _, err := st.Lesen(Beziehungen, "signal-passphrase"); !errors.Is(err, ErrUnbekannt) {
		t.Fatalf("a Nachrichten file asked as Beziehungen: %v", err)
	}
	if _, err := st.Lesen(Beziehungen, "umfeld/sozial.json"); !errors.Is(err, ErrUnbekannt) {
		t.Fatalf("the social export is Kontakte, not Beziehungen: %v", err)
	}
	for _, weg := range []string{"umfeld/../signal-passphrase", "../tresor/gocryptfs.conf", "/etc/passwd", protokollName, "umfeld"} {
		if _, err := st.Lesen(Beziehungen, weg); err == nil {
			t.Fatalf("%q was read", weg)
		}
	}
	// read only is read only
	le, _ := tt.Schluessel("leser")
	if err := le.Schreiben(Beziehungen, "umfeld/netz.json", []byte("x")); !errors.Is(err, ErrNichtFreigegeben) {
		t.Fatalf("a write with a read grant: %v", err)
	}
	if b, _ := le.Lesen(Beziehungen, "umfeld/netz.json"); string(b) != `{"a":1}` {
		t.Fatalf("the refused write changed the file: %q", b)
	}
	// a program the person granted nothing gets no key
	if _, err := tt.Schluessel("fremd"); !errors.Is(err, ErrNichtFreigegeben) {
		t.Fatalf("a key for an ungranted program: %v", err)
	}
	var verweigert, erlaubt int
	for _, z := range tt.protokoll(t) {
		if z.Programm == "tresor" {
			continue
		}
		if z.Verweigert != "" {
			verweigert++
		} else {
			erlaubt++
		}
		if strings.Contains(z.Was, `"a"`) {
			t.Fatalf("the protocol holds what was written: %+v", z)
		}
	}
	if erlaubt != 3 || verweigert != 11 {
		t.Fatalf("protocol: %d allowed, %d refused, want 3 and 11", erlaubt, verweigert)
	}
	u := tt.Zugriffe()
	if len(u.Letzte) < 2 || u.Letzte[0].Programm != "leser" || u.Letzte[1].Verweigert != "nicht freigegeben" {
		t.Fatalf("the newest access first: %+v", u.Letzte)
	}
}

func TestLockingVoidsEveryKeyAtOnceAndAReopeningDoesNotRevive(t *testing.T) {
	tt := neuerTresor(t)
	f := Freigaben{"stammbaum": {Beziehungen: Lesen | Schreiben, Nachrichten: Lesen}}
	tt.offen(t, f)
	z, _ := tt.Schluessel("stammbaum")
	p := tt.Programm("stammbaum")
	if err := z.Schreiben(Beziehungen, "umfeld/antworten.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := z.MsgvaultSchluessel(Lesen, "/api/v1/stats"); err != nil {
		t.Fatal(err)
	}
	token := z.Token()
	if v, err := tt.Vorgezeigt(token); err != nil || v != z {
		t.Fatalf("the token does not find its key: %v", err)
	}
	if err := tt.Abschliessen(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := z.Lesen(Beziehungen, "umfeld/antworten.json"); !errors.Is(err, ErrAbgeschlossen) {
		t.Fatalf("a key after the lock: %v", err)
	}
	if _, err := p.Lesen(Beziehungen, "umfeld/antworten.json"); !errors.Is(err, ErrAbgeschlossen) {
		t.Fatalf("a program after the lock: %v", err)
	}
	if _, err := tt.Vorgezeigt(token); !errors.Is(err, ErrAbgeschlossen) {
		t.Fatalf("the token after the lock: %v", err)
	}
	if err := tt.Oeffnen(context.Background(), []byte(pw), f); err != nil {
		t.Fatal(err)
	}
	tt.bereit(t)
	if _, err := z.Lesen(Beziehungen, "umfeld/antworten.json"); !errors.Is(err, ErrAbgeschlossen) {
		t.Fatalf("the old session's key opened the new one: %v", err)
	}
	if _, err := z.MsgvaultSchluessel(Lesen, "/api/v1/stats"); !errors.Is(err, ErrAbgeschlossen) {
		t.Fatalf("the old key reached msgvault in the new session: %v", err)
	}
	if _, err := tt.Vorgezeigt(token); !errors.Is(err, ErrAbgeschlossen) {
		t.Fatalf("the old token in the new session: %v", err)
	}
	if b, err := p.Lesen(Beziehungen, "umfeld/antworten.json"); err != nil || string(b) != "{}" {
		t.Fatalf("the program's new key: %q %v", b, err)
	}
}

func TestAKeyIsShortLivedAndTheSessionEnds(t *testing.T) {
	tt := neuerTresor(t)
	tt.offen(t, Freigaben{"p": {Notizen: Lesen | Schreiben}})
	z, _ := tt.Schluessel("p")
	if want := tt.uhr.Now().Add(SchluesselStandard); !z.Bis().Equal(want) {
		t.Fatalf("key until %v, want %v", z.Bis(), want)
	}
	tt.uhr.vor(SchluesselStandard)
	if err := z.Schreiben(Notizen, "pflege-passt.json", []byte("{}")); !errors.Is(err, ErrAbgeschlossen) {
		t.Fatalf("an expired key: %v", err)
	}
	if _, err := tt.Vorgezeigt(z.Token()); !errors.Is(err, ErrAbgeschlossen) {
		t.Fatalf("an expired token: %v", err)
	}
	p := tt.Programm("p")
	if err := p.Schreiben(Notizen, "pflege-passt.json", []byte("{}")); err != nil {
		t.Fatalf("the program takes a new key within its grant: %v", err)
	}
	// near the session's end a key ends with it
	tt.Aktiv()
	tt.uhr.vor(SitzungStandard - SchluesselStandard - 5*time.Minute)
	tt.Aktiv()
	n, _ := tt.Schluessel("p")
	s, _ := tt.Sitzung()
	if !n.Bis().Equal(s.Bis) {
		t.Fatalf("a key past the session: %v, session until %v", n.Bis(), s.Bis)
	}
	tt.uhr.vor(10 * time.Minute)
	tt.Pruefen(context.Background())
	if tt.Stand().Zustand != Verschlossen {
		t.Fatalf("the session ended and the vault stayed open: %+v", tt.Stand())
	}
}

func TestMsgvaultsHomeIsHandedOutOnlyWholeAndOnlyForWriting(t *testing.T) {
	tt := neuerTresor(t)
	tt.offen(t, Freigaben{"import": {Nachrichten: Lesen | Schreiben}, "leser": {Nachrichten: Lesen}})
	im, _ := tt.Schluessel("import")
	if _, err := im.Lesen(Nachrichten, "msgvault/api-key"); !errors.Is(err, ErrUnbekannt) {
		t.Fatalf("msgvault's key read as a file: %v", err)
	}
	if home, err := im.Ordner(Nachrichten, Schreiben, "msgvault"); err != nil || home != filepath.Join(tt.mnt, "msgvault") {
		t.Fatalf("msgvault's home: %q %v", home, err)
	}
	le, _ := tt.Schluessel("leser")
	if _, err := le.Ordner(Nachrichten, Schreiben, "msgvault"); !errors.Is(err, ErrNichtFreigegeben) {
		t.Fatalf("msgvault's home with a read grant: %v", err)
	}
	if _, err := im.Ordner(Nachrichten, Schreiben, "signal-passphrase"); !errors.Is(err, ErrUnbekannt) {
		t.Fatalf("a file handed out as a folder: %v", err)
	}
	if err := im.Schreiben(Nachrichten, "signal-passphrase", []byte("123")); err != nil {
		t.Fatal(err)
	}
	if am, ok := le.Seit(Nachrichten, "signal-passphrase"); !ok || am.IsZero() {
		t.Fatal("Seit does not see the stored passphrase")
	}
}

func TestEveryNameIsInExactlyOneArea(t *testing.T) {
	for name, want := range map[string]Bereich{
		"umfeld/netz.json": Beziehungen, "umfeld/sozial.json": Kontakte, "kalender/x.json": Kontakte,
		"kontakte/handy-kontakte.json": Kontakte, "immich-schluessel": Fotos, "pflege-passt.json": Notizen,
		"arbeit/paket-1/a": Nachrichten, "whatsapp-stand": Nachrichten,
	} {
		if e, ok := bereichVon(name); !ok || e.b != want {
			t.Errorf("%s: %v %v, want %v", name, e.b, ok, want)
		}
	}
	for _, name := range []string{protokollName, protokollAlt, "", ".", "..", "x", "umfeldx/a"} {
		if e, ok := bereichVon(name); ok {
			t.Errorf("%q is in %v", name, e.b)
		}
	}
}

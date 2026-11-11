package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

// Real-shaped exports: Facebook's JSON as it writes it (names escaped byte
// by byte, „Jürgen“ as JÃ¼rgen), LinkedIn's Connections.csv with
// its Notes preamble and the URL column.
const facebookFreundeJSON = `{
  "friends_v2": [
    {
      "name": "Stefan Ostermann",
      "timestamp": 1404158400
    },
    {
      "name": "JÃ¼rgen GroÃ\u009f",
      "timestamp": 1262304000
    },
    {
      "name": "",
      "timestamp": 1262304000
    }
  ]
}`

const linkedinConnections = "Notes:\n" +
	`"When exporting your connection data, you may notice that some of the email addresses are missing. You will only see email addresses for connections who have allowed their connections to see or download their email address using this setting https://www.linkedin.com/psettings/privacy/email. You can learn more here https://www.linkedin.com/help/linkedin/answer/261"` + "\n\n" +
	"First Name,Last Name,URL,Email Address,Company,Position,Connected On\n" +
	"Stefan,Ostermann,https://www.linkedin.com/in/stefan-ostermann-1a2b3c,s.ostermann@example.org,\"ACME GmbH, Köln\",Softwareentwickler,12 Mar 2019\n" +
	"Petra,Neumann,https://www.linkedin.com/in/petra-neumann,,Beispiel AG,Leiterin Einkauf,03 Jan 2021\n"

func zipMit(t *testing.T, dateien map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, inhalt := range dateien {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(inhalt))
	}
	z.Close()
	return b.Bytes()
}

func datei(t *testing.T, inhalt []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "export")
	os.WriteFile(p, inhalt, 0o600)
	return p
}

func TestSocialExportsAreToldByTheirContent(t *testing.T) {
	fall := []struct {
		was    string
		inhalt []byte
		quelle string
		n      int
	}{
		{"Facebook ZIP, 2024 layout", zipMit(t, map[string]string{
			"connections/friends/your_friends.json":        facebookFreundeJSON,
			"connections/friends/removed_friends.json":     `{"deleted_friends_v2":[{"name":"Weg","timestamp":1}]}`,
			"your_facebook_activity/posts/your_posts.json": `[]`,
		}), "facebook", 2},
		{"Facebook ZIP, 2022 layout in a folder", zipMit(t, map[string]string{"facebook-anna/friends_and_followers/friends.json": facebookFreundeJSON}), "facebook", 2},
		{"Facebook friends JSON alone", []byte(facebookFreundeJSON), "facebook", 2},
		{"LinkedIn ZIP", zipMit(t, map[string]string{"Connections.csv": linkedinConnections, "Profile.csv": "First Name,Last Name\nAnna,Dopp\n"}), "linkedin", 2},
		{"LinkedIn CSV alone, with BOM", append([]byte("\ufeff"), linkedinConnections...), "linkedin", 2},
	}
	for _, f := range fall {
		q, es, err := sozialLesen(datei(t, f.inhalt))
		if err != nil || q != f.quelle || len(es) != f.n {
			t.Fatalf("%s: %q %d %v", f.was, q, len(es), err)
		}
	}
	_, es, _ := sozialLesen(datei(t, []byte(facebookFreundeJSON)))
	if es[1].Name != "Jürgen Groß" || es[0].Seit != "2014-06-30" {
		t.Fatalf("Facebook's escaping or time: %+v", es)
	}
	_, es, _ = sozialLesen(datei(t, []byte(linkedinConnections)))
	want := sozialEintrag{Quelle: "linkedin", Name: "Stefan Ostermann", Mail: "s.ostermann@example.org", Firma: "ACME GmbH, Köln", Position: "Softwareentwickler", Seit: "2019-03-12"}
	if es[0] != want || es[1].Mail != "" {
		t.Fatalf("LinkedIn: %+v", es)
	}
}

func TestOtherFilesAreNoSocialExport(t *testing.T) {
	for was, inhalt := range map[string][]byte{
		"a Takeout ZIP":            zipMit(t, map[string]string{"Takeout/Standortverlauf/Records.json": `{"locations":[]}`}),
		"a music ZIP":              zipMit(t, map[string]string{"Album/01.mp3": "ID3"}),
		"Facebook's removed only":  zipMit(t, map[string]string{"connections/friends/removed_friends.json": `{"deleted_friends_v2":[]}`}),
		"another JSON":             []byte(`{"locations":[{"latitudeE7":1}]}`),
		"a CSV without the header": []byte("Name,Telefon\nAnna,123\n"),
		"text":                     []byte("Hallo"),
		"empty":                    nil,
	} {
		if _, _, err := sozialLesen(datei(t, inhalt)); err != errSozialFremd {
			t.Fatalf("%s: %v", was, err)
		}
	}
	html := zipMit(t, map[string]string{"connections/friends/your_friends.html": "<html></html>"})
	if _, _, err := sozialLesen(datei(t, html)); err != errSozialHTML {
		t.Fatalf("Facebook's HTML export: %v", err)
	}
}

func TestSocialEntriesOnlyGrow(t *testing.T) {
	alt := []sozialEintrag{{Quelle: "linkedin", Name: "Petra Neumann", Firma: "Alt AG", Seit: "2021-01-03"}, {Quelle: "facebook", Name: "Weg Gegangen"}}
	neu := []sozialEintrag{{Quelle: "linkedin", Name: "Petra Neumann", Firma: "Beispiel AG"}}
	m := sozialMischen(alt, neu)
	if len(m) != 2 || m[1].Firma != "Beispiel AG" || m[1].Seit != "2021-01-03" {
		t.Fatalf("%+v", m)
	}
}

// sozialNetz: the person (her card), Stefan with a tie to her (same
// address), Jürgen Groß on photos, and two cards named Kim.
func sozialNetz(es []sozialEintrag) *Netz {
	st := netzTabelle{
		Namen: map[string]string{"p-juergen": "Juergen Gross"},
		Kontakte: []netzKontakt{
			{Key: "privat/ich", Name: "Anna Dopp"},
			{Key: "privat/stefan", Name: "Stefan O.", Mails: []string{"S.Ostermann@example.org"}},
			{Key: "privat/kim1", Name: "Kim"}, {Key: "privat/kim2", Name: "Kim"},
		},
		Fest: []festBand{{A: "privat/ich", B: "privat/stefan", Art: bandAdresse}},
		Ich:  "privat/ich",
	}
	n := neuesNetz(st, nil, nil, time.UTC, func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) })
	n.sozialSetzen(es)
	return n
}

func TestSocialEntriesGiveReasonsAndNamesButNoTies(t *testing.T) {
	es := []sozialEintrag{
		{Quelle: "linkedin", Name: "Stefan Ostermann", Mail: "s.ostermann@example.org", Firma: "ACME GmbH", Position: "Entwickler", Seit: "2019-03-12"},
		{Quelle: "facebook", Name: "Stefan Ostermann", Seit: "2014-06-30"},
		{Quelle: "facebook", Name: "Jürgen Groß", Seit: "2010-01-01"},
		{Quelle: "facebook", Name: "Kim"},
		{Quelle: "facebook", Name: "Lea Fremd"},
	}
	n := sozialNetz(es)
	ich := n.Ich()
	p, ok := n.Paar(ich, "privat/stefan")
	if !ok || !slices.Contains(p.Ausserdem, "Kollege bei ACME GmbH (Entwickler, LinkedIn seit 2019)") {
		t.Fatalf("LinkedIn by e-mail: %q", p.Ausserdem)
	}
	if slices.Contains(p.Ausserdem, "kennen sich (Facebook seit 2014)") {
		t.Fatalf("Facebook's „Stefan Ostermann“ is not the card „Stefan O.“: %q", p.Ausserdem)
	}
	if p, _ := n.Paar(ich, "p-juergen"); !slices.Contains(p.Ausserdem, "kennen sich (Facebook seit 2010)") {
		t.Fatalf("Facebook by the name, umlauts alike: %q", p.Ausserdem)
	}
	for _, k := range []string{"privat/kim1", "privat/kim2"} {
		if p, _ := n.Paar(ich, k); slices.ContainsFunc(p.Ausserdem, func(g string) bool { return strings.Contains(g, "Facebook") }) {
			t.Fatalf("two cards named Kim: neither is Facebook's Kim: %q", p.Ausserdem)
		}
	}
	ks := n.Finden("lea fremd")
	if len(ks) != 1 || ks[0].ID != "sozial:leafremd" || !slices.Contains(n.Namen(), "Lea Fremd") || !slices.Contains(n.Namen(), "Stefan Ostermann") {
		t.Fatalf("names for „+ Beziehung“: %+v %q", ks, n.Namen())
	}
	if p, ok := n.Paar(ich, "sozial:leafremd"); !ok || p.Kante.belegt() || !slices.Contains(p.Ausserdem, "kennen sich (Facebook)") {
		t.Fatalf("a Facebook friend alone is a node to name, with its reason, no tie: %+v", p)
	}
	if personen, _ := n.Groesse(); personen != 2 {
		t.Fatalf("the social entries add no tie: %d people tied", personen)
	}
	for _, v := range n.Nahe(ich, 50) {
		if strings.HasPrefix(v.Mit.ID, "sozial:") {
			t.Fatalf("„Steht nahe“ shows a social entry alone: %+v", v)
		}
	}
}

func TestASocialExportGoesFromTheInboxIntoTheVault(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	inbox, data := t.TempDir(), t.TempDir()
	vomHandy := func(name string, an age.Recipient, inhalt []byte) string {
		os.MkdirAll(filepath.Join(inbox, "sozial"), 0o700)
		p := filepath.Join(inbox, "sozial", name)
		f, _ := os.Create(p)
		w, _ := age.Encrypt(f, an)
		w.Write(inhalt)
		w.Close()
		f.Close()
		return p
	}
	fb := vomHandy("facebook-20261111T120000Z.zip.age", id.Recipient(), zipMit(t, map[string]string{"connections/friends/your_friends.json": facebookFreundeJSON}))
	li := vomHandy("linkedin-20261111T120100Z.csv.age", id.Recipient(), []byte(linkedinConnections))
	fremd, _ := age.GenerateX25519Identity()
	kaputt := vomHandy("facebook-alt.zip.age", fremd.Recipient(), []byte(facebookFreundeJSON))
	if roh, _ := os.ReadFile(li); bytes.Contains(roh, []byte("Ostermann")) {
		t.Fatal("test setup: the file is not encrypted")
	}
	e := &Eingang{Dir: inbox}
	e.Stabil("sozial")
	innen := Innen{Wurzel: t.TempDir()}
	os.MkdirAll(innen.Arbeit(), 0o700)
	os.WriteFile(innen.Identitaet(), []byte(id.String()), 0o600)
	neu := 0
	pb := &Postbote{Post: filepath.Join(data, "briefkasten"), Eingang: e, Schluessel: Schluessel{Dir: filepath.Join(data, "schluessel")},
		Protokoll: LadeProtokoll(filepath.Join(data, "stand.json")), Run: &fakeMsgvault{}, Msgvault: "msgvault", SozialNeu: func(Innen) { neu++ }}

	pb.Runde(context.Background(), innen) // no number needed: nothing goes to msgvault

	for _, p := range []string{fb, li} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s stayed in the inbox", p)
		}
	}
	if _, err := os.Stat(kaputt); err != nil {
		t.Fatal("a file that cannot be decrypted must stay")
	}
	leer(t, innen.Arbeit(), "the vault's work folder")
	var d sozialDatei
	b, _ := os.ReadFile(innen.umfeldSozial())
	if json.Unmarshal(b, &d) != nil || len(d.Eintraege) != 4 || neu != 2 {
		t.Fatalf("in the vault: %s (net told %d times)", b, neu)
	}
	if fi, _ := os.Stat(innen.umfeldSozial()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("sozial.json is %v", fi.Mode().Perm())
	}
	ein, _, _ := pb.Protokoll.Kopie()
	if e, da := ein["sozial"]; !da || e.Anzahl != 2 {
		t.Fatalf("not recorded: %+v", ein["sozial"])
	}

	// a file that is no export leaves the inbox with its sentence
	weg := vomHandy("linkedin-x.csv.age", id.Recipient(), []byte("Name,Telefon\n"))
	e.Stabil("sozial")
	pb.Runde(context.Background(), innen)
	if _, err := os.Stat(weg); !os.IsNotExist(err) {
		t.Fatal("a file that is no export must not wait forever")
	}
	if _, f, _ := pb.Protokoll.Kopie(); f == nil || f.Quelle != "sozial" || !strings.Contains(f.Satz, "weder Facebooks") {
		t.Fatalf("the person is not told: %+v", f)
	}
}

package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mdopp/archiv/apps/archiv/tresor"
)

// A test plays the disk: it writes the files a feature will find in the
// vault, and reads what the feature left there, at the paths the vault
// keeps them. Production code never learns these paths (innen.go); only
// the tests do, through the mount point of their fake vault.

var testWurzeln sync.Map // *tresor.Programm → the fake vault's mount point

// testInnen is a program granted every area, in a vault of its own, open.
func testInnen(t testing.TB) Innen {
	t.Helper()
	tt := neuerTresor(t.(*testing.T))
	tt.Tresor.Lauf = nil
	if err := tt.Anlegen(context.Background(), []byte("richtig-langes-pw")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tt.Abschliessen(context.Background(), "test") })
	return tt.alles
}

func (i Innen) wurzel() string {
	if i.p == nil {
		return ""
	}
	w, _ := testWurzeln.Load(i.p)
	s, _ := w.(string)
	return s
}

func (i Innen) pfad(d Fach) string { return filepath.Join(i.wurzel(), filepath.FromSlash(d.name)) }

func (i Innen) Home() string               { return filepath.Join(i.wurzel(), ordnerMsgvault) }
func (i Innen) Arbeit() string             { return filepath.Join(i.wurzel(), ordnerArbeit) }
func (i Innen) GrosseAnhaenge() string     { return filepath.Join(i.wurzel(), ordnerGross) }
func (i Innen) Kalender() string           { return filepath.Join(i.wurzel(), ordnerKalender) }
func (i Innen) APIKey() string             { return filepath.Join(i.Home(), "api-key") }
func (i Innen) KontakteCA() string         { return filepath.Join(i.Home(), kontakteCAName) }
func (i Innen) Umfeld() string             { return filepath.Join(i.wurzel(), "umfeld") }
func (i Innen) Identitaet() string         { return i.pfad(fachIdentitaet) }
func (i Innen) WhatsAppSchluessel() string { return i.pfad(fachWhatsAppSchluessel) }
func (i Innen) SignalPassphrase() string   { return i.pfad(fachSignalPassphrase) }
func (i Innen) WhatsAppStand() string      { return i.pfad(fachWhatsAppStand) }
func (i Innen) MailAufraeumen() string     { return i.pfad(fachMailAufraeumen) }
func (i Innen) KontakteRelais() string     { return i.pfad(fachKontakteRelais) }
func (i Innen) KontakteVerbunden() string  { return i.pfad(fachKontakteVerbunden) }
func (i Innen) ImmichSchluessel() string   { return i.pfad(fachImmichSchluessel) }
func (i Innen) umfeldTabelle() string      { return i.pfad(fachNetz) }
func (i Innen) umfeldVerborgen() string    { return i.pfad(fachVerborgen) }
func (i Innen) umfeldAntworten() string    { return i.pfad(fachAntworten) }
func (i Innen) umfeldGruppen() string      { return i.pfad(fachGruppen) }
func (i Innen) umfeldGruppenModell() string {
	return i.pfad(fachGruppenModell)
}
func (i Innen) umfeldPaare() string       { return i.pfad(fachPaare) }
func (i Innen) umfeldHerkunft() string    { return i.pfad(fachHerkunft) }
func (i Innen) umfeldGebHinweise() string { return i.pfad(fachGebHinweise) }
func (i Innen) umfeldAngelegt() string    { return i.pfad(fachAngelegt) }
func (i Innen) umfeldHinweise() string    { return i.pfad(fachHinweise) }
func (i Innen) umfeldSozial() string      { return i.pfad(fachSozial) }
func (i Innen) pflegePasst() string       { return i.pfad(fachPflegePasst) }

func kontakteDatei(i Innen) string             { return i.pfad(fachKontakte) }
func kalenderPfad(i Innen, name string) string { return i.pfad(kalenderDatei(name)) }

// festerKey is a msgvault key for a test's fake msgvault ("" : not answering).
func festerKey(k string) func(r tresor.Recht, pfad string) (string, error) {
	return func(tresor.Recht, string) (string, error) {
		if k == "" {
			return "", errKeinProgramm
		}
		return k, nil
	}
}

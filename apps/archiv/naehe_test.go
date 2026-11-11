package main

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// What made the net say more than it knew (owner 2026-11-11, on 6,000
// ties: „weniger mit mehr“) is no tie: a wedding of twenty on two days,
// two class chats of thirty, two mails with eight in copy. What two
// share in a small circle still is: two days alone, two days among five,
// two chats of nine, two threads of three.
func TestNetzNurBedeutsames(t *testing.T) {
	jetzt := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	st := netzTabelle{Namen: map[string]string{"a": "Anna Berg", "jana": "Jana Freund"}, Ich: "h/a", Kontakte: []netzKontakt{{Key: "h/a", Name: "Anna Berg", Person: "a"}}}
	gaeste, fuenf := []string{"a"}, []string{"a"}
	for i := range 19 {
		id := fmt.Sprintf("gast%02d", i)
		st.Namen[id], gaeste = fmt.Sprintf("Gast %02d", i), append(gaeste, id)
	}
	for i := range 4 {
		id := fmt.Sprintf("klein%d", i)
		st.Namen[id], fuenf = fmt.Sprintf("Klein %d", i), append(fuenf, id)
	}
	for d := range 2 {
		tag := time.Date(2024, 6, 14+d, 15, 0, 0, 0, time.UTC)
		st.Fotos = append(st.Fotos, netzFoto{T: tag, Stadt: "Trier", Wer: gaeste}, netzFoto{T: tag, Stadt: "Rom", Wer: []string{"a", "jana"}}, netzFoto{T: tag, Stadt: "Ulm", Wer: fuenf})
	}
	for _, k := range []string{"eltern1", "eltern2", "kopie", "neun", "drei"} {
		st.Kontakte = append(st.Kontakte, netzKontakt{Key: "h/" + k, Name: strings.ToUpper(k[:1]) + k[1:]})
	}
	for i, g := range []netzGespraech{
		{Art: gespraechChat, Titel: "Klasse 3b Eltern", Groesse: 28, Wer: []string{"h/a", "h/eltern1", "h/eltern2"}}, {Art: gespraechChat, Titel: "Klasse 3b Fest", Groesse: 25, Wer: []string{"h/a", "h/eltern1", "h/eltern2"}},
		{Art: gespraechMail, Groesse: 8, Wer: []string{"h/a", "h/kopie"}}, {Art: gespraechMail, Groesse: 9, Wer: []string{"h/a", "h/kopie"}},
		{Art: gespraechChat, Titel: "Chor", Groesse: 9, Wer: []string{"h/a", "h/neun"}}, {Art: gespraechChat, Titel: "Chor Fahrt", Groesse: 9, Wer: []string{"h/a", "h/neun"}},
		{Art: gespraechMail, Groesse: 3, Wer: []string{"h/a", "h/drei"}}, {Art: gespraechMail, Groesse: 3, Wer: []string{"h/a", "h/drei"}},
	} {
		g.ID, g.Nachrichten, g.Zuletzt = fmt.Sprint(i), 5, jetzt
		st.Gespraeche = append(st.Gespraeche, g)
	}
	n := neuesNetz(st, nil, nil, time.UTC, func() time.Time { return jetzt })
	got := namenVon(n.Nahe("a", 100))
	slices.Sort(got)
	if want := []string{"Drei", "Jana Freund", "Klein 0", "Klein 1", "Klein 2", "Klein 3", "Neun"}; !slices.Equal(got, want) {
		t.Fatalf("Anna's ties:\n got %q\nwant %q", got, want)
	}
	if vs := n.Nahe("gast00", 100); len(vs) != 0 {
		t.Fatalf("two wedding guests are tied: %v", namenVon(vs))
	}
	if vs := n.Nahe("h/eltern1", 100); len(vs) != 0 {
		t.Fatalf("two parents of one class are tied: %v", namenVon(vs))
	}
	if _, kanten := n.Groesse(); kanten != 1+10+2 { // Jana, the five among themselves, Neun and Drei: not the wedding's 190
		t.Fatalf("%d ties", kanten)
	}
}

// A mail thread only one wrote in is no conversation; the chat of two is
// a tie from direktMin messages when both wrote, with its reason, and
// through any card of hers.
func TestNetzZuZweitUndEinseitig(t *testing.T) {
	jetzt := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	st := netzTabelle{Namen: map[string]string{}, Ich: "h/a", IchAuch: []string{"p/a2"}}
	for _, k := range []string{"h/a", "p/a2", "h/maier", "h/jana", "h/kurz", "h/still", "h/alt"} {
		st.Kontakte = append(st.Kontakte, netzKontakt{Key: k, Name: k})
	}
	st.Gespraeche = []netzGespraech{
		{ID: "1", Art: gespraechMail, Groesse: 2, Nachrichten: 1, Sender: 1, Zuletzt: jetzt, Wer: []string{"h/a", "h/maier"}},
		{ID: "2", Art: gespraechMail, Groesse: 2, Nachrichten: 3, Sender: 1, Zuletzt: jetzt, Wer: []string{"h/a", "h/maier"}},
		{ID: "3", Art: gespraechDirekt, Groesse: 2, Nachrichten: 840, Sender: 2, Zuletzt: jetzt, Wer: []string{"p/a2", "h/jana"}},
		{ID: "4", Art: gespraechDirekt, Groesse: 2, Nachrichten: direktMin - 1, Sender: 2, Zuletzt: jetzt, Wer: []string{"h/a", "h/kurz"}},
		{ID: "5", Art: gespraechDirekt, Groesse: 2, Nachrichten: 500, Sender: 1, Zuletzt: jetzt, Wer: []string{"h/a", "h/still"}},
		{ID: "6", Art: gespraechMail, Groesse: 2, Nachrichten: 4, Zuletzt: jetzt, Wer: []string{"h/a", "h/alt"}}, // tables kept before the senders were read count as before
		{ID: "7", Art: gespraechMail, Groesse: 2, Nachrichten: 4, Zuletzt: jetzt, Wer: []string{"h/a", "h/alt"}},
	}
	n := neuesNetz(st, nil, nil, time.UTC, func() time.Time { return jetzt })
	vs := n.Nahe("h/a", 10)
	if got := namenVon(vs); !slices.Equal(got, []string{"h/jana", "h/alt"}) {
		t.Fatalf("ties: %q", got)
	}
	if k := vs[0].Kante; k.Direkt != 840 || n.Gruende("h/a", k) != "840 Nachrichten zu zweit" || k.Staerke < 2.9 || k.Staerke > direktDeckel || !k.Kontakt.Equal(jetzt) {
		t.Fatalf("Jana: %+v %q", k, n.Gruende("h/a", k))
	}
	if direktGewicht(10) < 1 || direktGewicht(10) > 1.1 || direktGewicht(1e6) != direktDeckel {
		t.Fatalf("direktGewicht: %v %v", direktGewicht(10), direktGewicht(1e6))
	}
}

// How close is counted, not ranked: the first of a short list was „sehr
// nah (Nr. 1 von 4)“ whatever tied them.
func TestStufen(t *testing.T) {
	buch := func(art, wie string) []festBand { return []festBand{{Art: art, Wie: wie}} }
	for _, c := range []struct {
		k    Kante
		want string
	}{
		{Kante{Tage: 2}, "bekannt"}, {Kante{Chats: 2}, "bekannt"}, {Kante{Fest: buch(bandName, "Mama von Lena")}, "bekannt"}, {Kante{Fest: buch(bandFirma, "Werk AG"), Tage: 2}, "bekannt"},
		{Kante{Tage: 40}, "bekannt"}, {Kante{Direkt: 5000}, "bekannt"}, {Kante{Tage: 3, Direkt: 20}, "bekannt"}, {Kante{Fest: buch(bandAdresse, "")}, "bekannt"}, // one channel, or two thin ones
		{Kante{Fest: buch(bandBeziehung, "Kollege/Kollegin"), Faeden: 5}, "bekannt"},
		{Kante{Fest: buch(bandBeziehung, "Mutter")}, "nah"}, {Kante{Fest: buch(bandBeziehung, "Mutter"), Tage: 2}, "nah"}, {Kante{Tage: 12, Direkt: 20}, "nah"},
		{Kante{Tage: 12, Direkt: 300}, "nah"}, {Kante{Fest: buch(bandAdresse, ""), Tage: 2}, "nah"}, {Kante{Fest: []festBand{{Art: bandBeziehung, Wie: "partner", Gesagt: true}}, Direkt: 10}, "nah"},
		{Kante{Fest: buch(bandBeziehung, "Mutter"), Tage: 12}, "sehr nah"}, {Kante{Fest: append(buch(bandBeziehung, "Ehepartner/in"), festBand{Art: bandAdresse})}, "sehr nah"},
		{Kante{Tage: 12, Direkt: 300, Chats: 2}, "sehr nah"}, {Kante{Fest: []festBand{{Art: bandBeziehung, Wie: "partner", Gesagt: true}}, Direkt: 300}, "sehr nah"},
	} {
		if got := c.k.Stufe(); got != c.want {
			t.Errorf("%+v: %s, want %s", c.k, got, c.want)
		}
	}
	n, m := sternNetz(4) // m on a few days with each of four: the first is not „sehr nah“
	if vs := n.Nahe(m.ID, 10); vs[0].Kante.Stufe() != "bekannt" {
		t.Fatalf("the first of four: %s", vs[0].Kante.Stufe())
	}
}

// „Lange nichts gehört“ and the list in steps, on the pages: Tobias was
// close and is silent since 2022, with when and what; Jana writes daily;
// Ben lives with her; the wedding's guests and the class's parents are
// not on the list at all. „Nicht mehr erinnern“ on his page takes him
// out, Rückgängig brings him back. Without a chat of two read: nobody.
func TestLangeNichtsGehoert(t *testing.T) {
	wt, ut, _, innen, _ := kanteBox(t)
	schauNetz(t, ut, innen)
	n := ut.u.Netz()
	ss := n.Stille()
	if len(ss) != 1 || ss[0].Mit.Name != "Tobias Alt" || stillGrund(ss[0], time.UTC) != "zuletzt im März 2022 · 14 Fototage · 420 Nachrichten zu zweit" {
		t.Fatalf("Stille: %+v", ss)
	}
	page := wt.rufe("GET", "/tresor/umfeld", anna, nil).Body.String()
	for _, want := range []string{`<section class="karte still"><h4>Lange nichts gehört</h4><ul class=nahe><li><a class=zeile href="/tresor/umfeld?mitte=p-tobias">Tobias Alt <span class=k>zuletzt im März 2022 · 14 Fototage · 420 Nachrichten zu zweit</span></a></li></ul></section>`,
		`<h4 class=stufe>Sehr nah (1)</h4><ul class=nahe><li><a class=zeile href="/tresor/umfeld?mitte=p-ben">Ben Berg`,
		`<h4 class=stufe>Nah (2)</h4><ul class=nahe><li><a class=zeile href="/tresor/umfeld?mitte=p-jana">Jana Freund <span class=k>14 Tage auf deinen Fotos seit 2021 · 840 Nachrichten zu zweit</span></a></li>`,
		`<details class=stufe><summary>Bekannt (1)</summary><ul class=nahe><li><a class=zeile href="/tresor/umfeld?mitte=p-kim">Kim Berg`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the page lacks %q:\n%s", want, page)
		}
	}
	for _, nie := range []string{"mitte=p-gast0", "mitte=privat%2Fe0", "mitte=privat%2Fmaier", "mitte=privat%2Fk0", "Mehr zeigen", "Nr. 1 von"} {
		if strings.Contains(page, nie) {
			t.Fatalf("the page says %q", nie)
		}
	}
	if start := wt.rufe("GET", "/tresor?tab=nahe", anna, nil).Body.String(); !strings.Contains(start, "<h4>Lange nichts gehört</h4>") {
		t.Fatalf("the start page lacks it:\n%s", start)
	}
	tobias := wt.rufe("GET", "/tresor/umfeld?mitte=p-tobias", anna, nil).Body.String()
	if !strings.Contains(tobias, `Für dich: noch nichts eingetragen · nah · zuletzt gemeinsam am 12.03.2022`) || !strings.Contains(tobias, `<button class=k name=art value="ruhe" data-laeuft="Merkt es sich …">Nicht mehr erinnern</button>`) {
		t.Fatalf("Tobias' page:\n%s", tobias)
	}
	weg := wt.rufe("POST", "/tresor/umfeld/verbergen", anna, url.Values{"id": {"p-tobias"}, "art": {verbergenRuhe}}).Body.String()
	if strings.Contains(weg, "Lange nichts gehört") || !strings.Contains(weg, "✓ An Tobias Alt erinnert das Archiv nicht mehr") || !strings.Contains(weg, "Tobias Alt <span class=k>14 Tage") || len(ruhenLaden(innen)) != 1 {
		t.Fatalf("after Nicht mehr erinnern (he stays on the list):\n%s", weg)
	}
	ut.u.laden(innen) // kept in the vault
	if len(ut.u.Netz().Stille()) != 0 {
		t.Fatal("named again after a reload")
	}
	wt.rufe("POST", "/tresor/umfeld/verbergen", anna, url.Values{"id": {"p-tobias"}, "art": {""}})
	if len(ut.u.Netz().Stille()) != 1 || len(ruhenLaden(innen)) != 0 {
		t.Fatal("Rückgängig did not bring him back")
	}

	tab, _ := tabelleLaden(innen)
	tab.Gespraeche = slices.DeleteFunc(tab.Gespraeche, func(g netzGespraech) bool { return g.Art == gespraechDirekt })
	if n := neuesNetz(tab, nil, nil, time.UTC, ut.u.Now); len(n.Stille()) != 0 {
		t.Fatalf("no chat of two read, and still somebody is silent: %+v", n.Stille())
	}
}

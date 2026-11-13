package main

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// Verstorben (owner 2026-11-11), one tap on Tobias' page: „Lange nichts
// gehört“ no longer names him, he stays on the list and his page with
// „†“, kept in the vault; the same tap undoes it.
func TestVerstorbenAufDerSeite(t *testing.T) {
	wt, ut, _, innen, _ := kanteBox(t)
	schauNetz(t, ut, innen)
	tobias := wt.rufe("GET", "/tresor/umfeld?mitte=p-tobias", anna, nil).Body.String()
	if !strings.Contains(tobias, `<button class=k name=tu value=verstorben data-laeuft="Merkt es sich …">Ist verstorben</button>`) || strings.Contains(tobias, "†") {
		t.Fatalf("Tobias' page:\n%s", tobias)
	}
	if len(ut.u.Netz().Stille()) != 1 {
		t.Fatal("Tobias is silent before")
	}
	ziel, page := wt.folgen(t, wt.rufe("POST", personPfad, anna, url.Values{"tu": {"verstorben"}, "id": {"p-tobias"}}))
	for _, want := range []string{"<h2>Tobias Alt †</h2>", "Als verstorben markiert.", "† Verstorben. Das Archiv fragt nichts mehr zu Tobias Alt.", `value=lebt`, "Markierung entfernen"} {
		if !strings.Contains(page, want) {
			t.Fatalf("%s lacks %q:\n%s", ziel, want, page)
		}
	}
	ut.u.laden(innen) // kept in the vault
	n := ut.u.Netz()
	if len(n.Stille()) != 0 || len(markeLaden(innen.umfeldVerstorben())) != 1 {
		t.Fatalf("still named: %+v", n.Stille())
	}
	if start := wt.rufe("GET", "/tresor/umfeld", anna, nil).Body.String(); !strings.Contains(start, `href="/tresor/umfeld?mitte=p-tobias">Tobias Alt † <span class=k>`) {
		t.Fatalf("not on the list, or not marked:\n%s", start)
	}
	_, page = wt.folgen(t, wt.rufe("POST", personPfad, anna, url.Values{"tu": {"lebt"}, "id": {"p-tobias"}}))
	if strings.Contains(page, "Tobias Alt †") || !strings.Contains(page, "Markierung entfernt.") || len(ut.u.Netz().Stille()) != 1 || len(markeLaden(innen.umfeldVerstorben())) != 0 {
		t.Fatalf("not undone:\n%s", page)
	}
}

// No question about one deceased: not „Kennst du …?“ (a shared company),
// no relation hint, no birthday, town or employer, no place in the tree; she stays in the
// tree, marked.
func TestVerstorbenFragtNichts(t *testing.T) {
	tot := func(n *Netz, id string) {
		n.mu.Lock()
		n.verstorben = map[string]time.Time{id: time.Now()}
		n.totMarken()
		n.mu.Unlock()
	}
	n := grundNetz(nil)
	if !slices.ContainsFunc(n.Fragen(), func(f Frage) bool { return f.Kennen && f.X.ID == "h/christian" }) {
		t.Fatal("no question about Christian to begin with")
	}
	tot(n, "h/christian")
	if slices.ContainsFunc(n.Fragen(), func(f Frage) bool { return f.X.ID == "h/christian" || f.Y.ID == "h/christian" }) {
		t.Fatal("a question about Christian, deceased")
	}
	n.mu.Lock()
	ok := n.befragbar("h/christian") || !n.befragbar("p-lisa") || !n.fragbar("h/christian")
	n.mu.Unlock()
	if ok {
		t.Fatal("befragbar")
	}
	if k, _ := n.Person("h/christian"); n.Anzeige(k) != "Christian Huesmann †" {
		t.Fatalf("%q", n.Anzeige(k))
	}
	st := netzTabelle{Namen: map[string]string{}, Ich: "h/a", Kontakte: []netzKontakt{{Key: "h/a", Name: "Anna Alt"}, {Key: "h/s", Name: "Sabine (Mama von Lena)"}, {Key: "h/l", Name: "Lena Berg"}}}
	hinweis := neuesNetz(st, nil, nil, time.UTC, time.Now)
	if len(hinweis.ErkannteBeziehungen()) != 1 {
		t.Fatal("no relation hint to begin with")
	}
	if tot(hinweis, "h/s"); len(hinweis.ErkannteBeziehungen()) != 0 {
		t.Fatal("a relation hint about Sabine, deceased")
	}

	am := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	b := baumNetz(map[string]antwort{paarKey("h/zora", "h/uwe"): {X: "h/zora", Art: "onkel", Am: am}})
	tot(b, "h/zora") // the dead have their place in a family tree: still offered for it
	sb := b.Stammbaum()
	if len(sb.Fragen) != 1 || baumPos(sb)["h/zora"].ID == "" {
		t.Fatalf("Zora: without a place %+v, in the tree %+v", sb.Fragen, baumPos(sb)["h/zora"])
	}
	var s strings.Builder
	stammbaumTab(&s, b)
	if !strings.Contains(s.String(), "Zora †") {
		t.Fatalf("not marked in the tree:\n%s", s.String())
	}
}

// A work address (ADR TYPE=work) is a company's (owner 2026-11-11): two
// cards with the same one are no tie and no neighbours, only „Kennst du
// …?“ with the address as the reason; a home address stays a tie.
func TestArbeitsadresseIstFirma(t *testing.T) {
	ks := kartenLesen("h/", karteMit("a", "Horst Brandt", "ADR;TYPE=WORK,pref:;;Hafenstraße 5;Lingen;;49808;\r\n")+
		karteMit("b", "Christian Huesmann", "item1.ADR;type=work:;;Hafenstraße 5;Lingen;;49808;\r\n")+
		karteMit("c", "Vera Alt", "ADR;WORK:;;Hafenstraße 5;Lingen;;49808;\r\nADR;TYPE=home:;;Mühlenweg 4;Lingen;;49808;\r\n")+
		karteMit("d", "Tim Alt", "ADR:;;Mühlenweg 4;Lingen;;49808;\r\n"))
	if len(ks[0].strassen) != 0 || len(ks[0].adressen) != 0 || len(ks[2].strassen) != 1 {
		t.Fatalf("a work address is no home: %+v", ks)
	}
	var got []string
	for _, f := range gruppen(ks) {
		got = append(got, f.Art+"="+f.A+"-"+f.B+"="+f.Wie)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"adresse=h/c-h/d=Mühlenweg 4", "arbeit=h/a-h/b=Hafenstraße 5", "arbeit=h/a-h/c=Hafenstraße 5", "arbeit=h/b-h/c=Hafenstraße 5"}) {
		t.Fatalf("%v", got)
	}

	st := netzTabelle{Namen: map[string]string{}, Ich: "h/a"}
	for _, k := range ks {
		st.Kontakte = append(st.Kontakte, netzKontakt{Key: k.key, Name: k.name, Strassen: k.strassen})
	}
	st.Fest = gruppen(ks)
	n := neuesNetz(st, nil, nil, time.UTC, func() time.Time { return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) })
	if n.hatKante("h/a", "h/b") || n.hatKante("h/b", "h/c") || !n.hatKante("h/c", "h/d") {
		t.Fatalf("ties: %+v", n.kanten)
	}
	var kennen []string
	for _, f := range n.Fragen() {
		if f.Kennen {
			kennen = append(kennen, f.X.ID+"/"+f.Y.ID+": "+f.Weil)
		}
	}
	slices.Sort(kennen)
	if !slices.Equal(kennen, []string{"h/b/h/a: Beide Karten nennen die Arbeitsadresse Hafenstraße 5", "h/b/h/c: Beide Karten nennen die Arbeitsadresse Hafenstraße 5", "h/c/h/a: Beide Karten nennen die Arbeitsadresse Hafenstraße 5"}) {
		t.Fatalf("%q", kennen)
	}
	if p, ok := n.Paar("h/a", "h/b"); !ok || !slices.Contains(p.Ausserdem, "Beide Kontaktkarten nennen die Arbeitsadresse Hafenstraße 5") {
		t.Fatalf("„Warum?“ names the address: %+v", p)
	}
}

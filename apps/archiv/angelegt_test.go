package main

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// A person by hand (owner 2026-11-11): „Zu wem?“ with a name nobody has
// offers „<Name> anlegen“; one tap on a kind makes her (only in the
// vault) and the relation, back where „+ Beziehung“ was, with „✓ <Name>
// angelegt, <Beziehung>.“ and Rückgängig, which takes back both.
func TestAngelegtEinTipp(t *testing.T) {
	wt, _, _, innen := eintragenBox(t)
	zurueck := umfeldMitte("p-ben")
	page := wt.rufe("GET", kantePfad+"?"+url.Values{"a": {"p-ben"}, "wer": {"Wilma Wolf"}, "zurueck": {zurueck}}.Encode(), anna, nil).Body.String()
	for _, want := range []string{"<h2>Wilma Wolf anlegen</h2>", esc(saysAnlegenNiemand), esc("Wilma Wolf ist für Ben Berg …"), `name=tu value=anlegen`,
		`<input type=hidden name=zurueck value="` + esc(zurueck) + `">`, `<button name=art value=kind class="chip"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the offer lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, esc(saysKanteNiemand)) {
		t.Fatalf("the dead-end sentence is still there:\n%s", page)
	}
	rec := wt.rufe("POST", personPfad, anna, url.Values{"tu": {"anlegen"}, "name": {" Wilma  Wolf "}, "y": {"p-ben"}, "art": {"kind"}, "zurueck": {zurueck}})
	ziel, page := wt.folgen(t, rec)
	n := wt.w.Umfeld.Netz()
	ks := n.Finden("wilma wolf")
	if len(ks) != 1 || !istAngelegt(ks[0].ID) || ks[0].Name != "Wilma Wolf" {
		t.Fatalf("not made: %+v", ks)
	}
	id, key := ks[0].ID, paarKey(ks[0].ID, "p-ben")
	if !strings.HasPrefix(ziel, "/tresor/umfeld?") || !strings.Contains(ziel, "mitte=p-ben") || !strings.Contains(ziel, "getan="+url.QueryEscape(key)) {
		t.Fatalf("not back where it came from: %s", ziel)
	}
	for _, want := range []string{"✓ " + esc("Wilma Wolf angelegt, Kind von Ben Berg."), `<input type=hidden name=neu value="` + id + `">`, ">Rückgängig<"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the toast lacks %q:\n%s", want, page)
		}
	}
	if p, _ := wt.w.Umfeld.Netz().Paar(id, "p-ben"); p.Art != "kind" || p.Quelle != "gesagt" {
		t.Fatalf("the relation: %+v", p)
	}
	if b, _ := os.ReadFile(innen.umfeldAngelegt()); !strings.Contains(string(b), "Wilma Wolf") {
		t.Fatalf("not in the vault: %s", b)
	}
	// Rückgängig: the person and the relation are gone
	rec = wt.rufe("POST", antwortPfad, anna, url.Values{"paar": {key}, "tu": {"vergessen"}, "neu": {id}, "zurueck": {zurueck}})
	_, page = wt.folgen(t, rec)
	if _, da := wt.w.Umfeld.Netz().Angelegt(id); da || len(wt.w.Umfeld.Netz().Finden("wilma wolf")) != 0 || !strings.Contains(page, esc(saysKanteZurueck)) {
		t.Fatalf("Rückgängig left her:\n%s", page)
	}
	if b, _ := os.ReadFile(innen.umfeldAngelegt()); strings.Contains(string(b), "Wilma") {
		t.Fatalf("still in the vault: %s", b)
	}
	// from the tree, the person's own „Zu wem?“: back to the tree, the toast there
	rec = wt.rufe("POST", personPfad, anna, url.Values{"tu": {"anlegen"}, "name": {"Oma Ilse"}, "y": {"p-anna"}, "art": {"grosseltern"}, "zurueck": {"/tresor?tab=stammbaum"}})
	if ziel, page := wt.folgen(t, rec); !strings.HasPrefix(ziel, "/tresor?") || !strings.Contains(ziel, "tab=stammbaum") || !strings.Contains(page, esc("Oma Ilse angelegt, Großeltern von dir.")) {
		t.Fatalf("the tree: %s\n%s", ziel, page)
	}
}

// Her page renames her (what Homes wrote of her goes again under the new
// name) or deletes her with her relations, never on one tap.
func TestAngelegtUmbenennenLoeschen(t *testing.T) {
	wt, _, _, _ := eintragenBox(t)
	wt.rufe("POST", personPfad, anna, url.Values{"tu": {"anlegen"}, "name": {"Wilma"}, "y": {"p-anna"}, "art": {"freund"}, "zurueck": {"/tresor"}})
	id := wt.w.Umfeld.Netz().Finden("wilma")[0].ID
	page := wt.rufe("GET", umfeldMitte(id), anna, nil).Body.String()
	for _, want := range []string{esc(saysAngelegtHinweis), "<summary>Umbenennen</summary>", "<summary>Löschen</summary>", ">Wilma löschen<"} {
		if !strings.Contains(page, want) {
			t.Fatalf("her page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, ">Ausblenden<") {
		t.Fatalf("a person by hand is deleted, not hidden:\n%s", page)
	}
	_, page = wt.folgen(t, wt.rufe("POST", personPfad, anna, url.Values{"tu": {"umbenennen"}, "id": {id}, "name": {"Wilma Gnielczyk"}}))
	if k, _ := wt.w.Umfeld.Netz().Person(id); k.Name != "Wilma Gnielczyk" || !strings.Contains(page, "Umbenannt.") {
		t.Fatalf("not renamed: %+v\n%s", k, page)
	}
	if p, _ := wt.w.Umfeld.Netz().Paar(id, "p-anna"); p.Art != "freund" {
		t.Fatalf("the relation went with the name: %+v", p)
	}
	_, page = wt.folgen(t, wt.rufe("POST", personPfad, anna, url.Values{"tu": {"loeschen"}, "id": {id}}))
	if _, da := wt.w.Umfeld.Netz().Person(id); da || len(wt.w.Umfeld.Netz().Ihre(id)) != 0 || !strings.Contains(page, esc(meldungen["person-weg"])) {
		t.Fatalf("not deleted:\n%s", page)
	}
}

// A phone-book card of her name turns up: never merged silently; asked
// once „Ist <Name> aus deinem Telefonbuch dieselbe Person?“ — Nein keeps
// both and asks no more, Ja moves her relations to the card's person.
func TestAngelegtDieselbe(t *testing.T) {
	for _, ja := range []bool{false, true} {
		wt, ut, _, innen := eintragenBox(t)
		wt.rufe("POST", personPfad, anna, url.Values{"tu": {"anlegen"}, "name": {"Wilma Wolf"}, "y": {"p-ben"}, "art": {"kind"}, "zurueck": {"/tresor"}})
		id := wt.w.Umfeld.Netz().Finden("wilma wolf")[0].ID
		tab, err := tabelleLaden(innen)
		if err != nil {
			t.Fatal(err)
		}
		tab.Kontakte = append(tab.Kontakte, netzKontakt{Key: "privat/w", Name: "Wilma Wolf"})
		if err := jsonImTresor(innen.umfeldTabelle(), tab); err != nil {
			t.Fatal(err)
		}
		ut.u.laden(innen)
		n := wt.w.Umfeld.Netz()
		karte := n.Von("privat/w")
		if len(n.Finden("wilma wolf")) != 2 {
			t.Fatalf("merged without asking: %+v", n.Finden("wilma wolf"))
		}
		if page := wt.rufe("GET", "/tresor", anna, nil).Body.String(); !strings.Contains(page, ">1 Vorschlag ansehen<") {
			t.Fatalf("the workplace does not say so:\n%s", page)
		}
		page := wt.rufe("GET", erkanntPfad, anna, nil).Body.String()
		for _, want := range []string{"<h2>Dieselbe Person?</h2>", esc("Ist Wilma Wolf aus deinem Telefonbuch dieselbe Person?"), `name=ziel value="` + karte + `"`, ">Ja<", ">Nein<"} {
			if !strings.Contains(page, want) {
				t.Fatalf("Erkannt lacks %q:\n%s", want, page)
			}
		}
		if page := wt.rufe("GET", umfeldMitte(id), anna, nil).Body.String(); !strings.Contains(page, esc("Ist Wilma Wolf aus deinem Telefonbuch dieselbe Person?")) {
			t.Fatalf("her page does not ask:\n%s", page)
		}
		antwort := "0"
		if ja {
			antwort = "1"
		}
		rec := wt.rufe("POST", personPfad, anna, url.Values{"tu": {"dieselbe"}, "id": {id}, "ziel": {karte}, "ja": {antwort}, "zurueck": {erkanntPfad}})
		_, page = wt.folgen(t, rec)
		n = wt.w.Umfeld.Netz()
		if strings.Contains(page, "Dieselbe Person?") || len(n.GleichePersonen()) != 0 {
			t.Fatalf("asked again (ja %v):\n%s", ja, page)
		}
		_, bleibt := n.Angelegt(id)
		p, _ := n.Paar(karte, "p-ben")
		if ja && (bleibt || p.Art != "kind" || !strings.Contains(page, esc(meldungen["person-ja"]))) {
			t.Fatalf("Ja did not merge: %v %+v\n%s", bleibt, p, page)
		}
		if !ja && (!bleibt || p.Art != "" || len(n.Finden("wilma wolf")) != 2) {
			t.Fatalf("Nein did not keep both: %v %+v", bleibt, p)
		}
		ut.u.laden(innen) // and so it stays, read again from the vault
		if len(wt.w.Umfeld.Netz().GleichePersonen()) != 0 {
			t.Fatalf("asked again after a reload (ja %v)", ja)
		}
	}
}

func TestAngelegtFalsch(t *testing.T) {
	wt, _, _, _ := eintragenBox(t)
	for _, f := range []url.Values{
		{"tu": {"anlegen"}, "name": {"  "}, "y": {"p-ben"}, "art": {"kind"}},
		{"tu": {"anlegen"}, "name": {"Wilma"}, "y": {"p-ben"}, "art": {"fremd"}},
		{"tu": {"anlegen"}, "name": {"Wilma"}, "y": {"niemand"}, "art": {"kind"}},
		{"tu": {"umbenennen"}, "id": {"p-ben"}, "name": {"Ben"}},
		{"tu": {"loeschen"}, "id": {"p-ben"}},
		{"tu": {"dieselbe"}, "id": {"p-ben"}, "ziel": {"p-anna"}, "ja": {"1"}},
	} {
		if rec := wt.rufe("POST", personPfad, anna, f); rec.Code == http.StatusSeeOther {
			t.Fatalf("%v went through", f)
		}
	}
	if k, _ := wt.w.Umfeld.Netz().Person("p-ben"); k.Name != "Ben Berg" {
		t.Fatalf("a phone-book person changed: %+v", k)
	}
}

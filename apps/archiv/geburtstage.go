package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Birthdays from congratulations (archiv#42, owner 2026-11-11: „mehr
// Erkennungen, immer als Vorschlag“). Each round, while the vault is open
// and msgvault answers, it reads (one read-only statement, as hinweise.go
// does, the newest modellTreffer) the messages that wish a happy birthday
// in one of the family's languages, never a belated one. A wish to one
// known person alone (a direct chat or a mail to one) is a sign: the day
// it was sent, in the house's time zone, is that person's birthday. The
// day with the most signs — at least two, from two years or from three
// senders, and more than any other day — is the suggestion. No model: the
// pattern decides, and nothing of a text is kept or logged, only the day
// and how many wished. A person whose birthday a card or Immich already
// has is not suggested.
//
// A tap („Stimmt“) keeps the birthday in the vault (umfeld/geburtstage.json),
// „Stimmt nicht“ keeps that it is not, so it is not suggested again; it
// is not written to an address book (that needs a new Homes packet: archiv#42).

const (
	gebMinBelege   = 2 // signs a day needs
	gebMinSender   = 3 // senders within one year, when there is only one year
	gebVorschlaege = 30
)

// gebWuensche are the wishes, lower case, as an alternation both RE2s read
// (DuckDB's on lower(snippet), and the archive's).
const gebWuensche = `alles gute zum geburtstag|alles liebe zum geburtstag|herzlichen glückwunsch zum geburtstag|herzlichen glueckwunsch zum geburtstag|` +
	`happy birthday|happy bday|feliz cumpleaños|feliz cumpleanos|joyeux anniversaire|iyi ki doğdun|iyi ki dogdun|doğum günün kutlu|dogum gunun kutlu|` +
	`wszystkiego najlepszego z okazji urodzin|sto lat|с днём рождения|с днем рождения|s dnem rozhdeniya|eid milad saeed|عيد ميلاد سعيد`

var (
	glueckwunsch  = regexp.MustCompile(`(?i)(` + gebWuensche + `)`)
	nachtraeglich = regexp.MustCompile(`(?i)(nachträglich|nachtraeglich|verspätet|verspaetet|belated|en retard|atrasado|con retraso|gecikmiş|spóźnion|zapozdal)`)
)

// gebHinweis is what the wishes say of a person's birthday: the day
// („03-14“), how many signs, and their years.
type gebHinweis struct {
	Tag    string `json:"t"`
	Belege int    `json:"b"`
	Jahre  []int  `json:"j"`
}

// gebAntwort is the person's answer to a suggestion: the day, and whether it is.
type gebAntwort struct {
	Tag string    `json:"t"`
	Ja  bool      `json:"ja"`
	Am  time.Time `json:"am"`
}

func (i Innen) umfeldGebHinweise() string {
	return filepath.Join(i.Umfeld(), "geburtstag-hinweise.json")
}
func (i Innen) umfeldGeburtstage() string { return filepath.Join(i.Umfeld(), "geburtstage.json") }

func gebHinweiseLaden(innen Innen) map[string]gebHinweis {
	out := map[string]gebHinweis{}
	if b, err := os.ReadFile(innen.umfeldGebHinweise()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func geburtstageLaden(innen Innen) map[string]gebAntwort {
	out := map[string]gebAntwort{}
	if b, err := os.ReadFile(innen.umfeldGeburtstage()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// geburtstagHinweise reads the wishes of roh: a wish (not a belated one)
// to one known person alone is a sign for its day. n.mu is free.
func (n *Netz) geburtstagHinweise(roh []rohNachricht, ks []buchKarte) map[string]gebHinweis {
	ich := n.Ich()
	loc := cmp.Or(n.Loc, time.Local)
	type zeichen struct {
		jahre  map[int]bool
		sender map[string]bool
		belege map[string]bool // year|sender: a sender's wishes of one year count once
	}
	je := map[string]map[string]*zeichen{} // node → day → signs
	for _, r := range roh {
		if r.Am.IsZero() || !glueckwunsch.MatchString(r.Text) || nachtraeglich.MatchString(r.Text) {
			continue
		}
		von := ""
		if r.VonMir {
			von = ich
		} else if len(r.Von) > 0 {
			von = n.knotenVon(r.Von[0], ks)
		}
		var an []string
		for _, t := range r.An {
			if id := n.knotenVon(t, ks); id != "" && id != von && !slices.Contains(an, id) {
				an = append(an, id)
			}
		}
		if len(an) == 0 && !r.VonMir && ich != "" && ich != von && r.Art == gespraechChat {
			an = []string{ich} // a direct chat: the person got it
		}
		if von == "" || len(an) != 1 || len(r.An) > 1 {
			continue
		}
		t := r.Am.In(loc)
		tag := t.Format("01-02")
		if je[an[0]] == nil {
			je[an[0]] = map[string]*zeichen{}
		}
		z := je[an[0]][tag]
		if z == nil {
			z = &zeichen{jahre: map[int]bool{}, sender: map[string]bool{}, belege: map[string]bool{}}
			je[an[0]][tag] = z
		}
		z.jahre[t.Year()], z.sender[von], z.belege[fmt.Sprint(t.Year(), "|", von)] = true, true, true
	}
	out := map[string]gebHinweis{}
	for id, tage := range je {
		best, zweit, wahl := 0, 0, ""
		for _, tag := range slices.Sorted(maps.Keys(tage)) {
			switch b := len(tage[tag].belege); {
			case b > best:
				best, zweit, wahl = b, best, tag
			case b > zweit:
				zweit = b
			}
		}
		z := tage[wahl]
		if best < gebMinBelege || best == zweit || len(z.jahre) < 2 && len(z.sender) < gebMinSender {
			continue
		}
		out[id] = gebHinweis{Tag: wahl, Belege: best, Jahre: slices.Sorted(maps.Keys(z.jahre))}
	}
	return out
}

// geburtstageRunde reads the wishes from msgvault, keeps the suggestions
// in the vault and gives them to the net. It logs counts only.
func (u *Umfeld) geburtstageRunde(ctx context.Context, innen Innen, n *Netz, ks []buchKarte) {
	if u.Msgvault == nil || n == nil {
		return
	}
	roh, err := u.Msgvault.Schlagwortnachrichten(ctx, gebWuensche)
	if err != nil {
		if !errors.Is(err, errNichtBereit) && !errors.Is(err, errGespraecheSpaeter) && ctx.Err() == nil {
			log.Printf("archiv: Umfeld: Glückwünsche aus msgvault: %v", err)
		}
		return
	}
	h := n.geburtstagHinweise(roh, ks)
	if err := jsonImTresor(innen.umfeldGebHinweise(), h); err != nil {
		log.Printf("archiv: Umfeld: Geburtstage speichern: %v", err)
		return
	}
	n.gebSetzen(h, nil)
	u.geaendert()
}

// gebSetzen gives the net the suggestions and (not nil) the answers.
func (n *Netz) gebSetzen(h map[string]gebHinweis, a map[string]gebAntwort) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if h != nil {
		n.gebHinweise = maps.Clone(h)
	}
	if a != nil {
		n.gebAntworten = maps.Clone(a)
	}
}

// GebVorschlag is a suggested birthday as the page shows it.
type GebVorschlag struct {
	Wer    Knoten
	Ich    bool
	Tag    string
	Belege int
	Jahre  []int
}

// GeburtstagVorschlaege are the suggestions not answered, of persons shown
// whose birthday no card and no Immich person has, the most signs first.
func (n *Netz) GeburtstagVorschlaege() (out []GebVorschlag) {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, h := range n.gebHinweise {
		if a, da := n.gebAntworten[id]; da && a.Tag == h.Tag || !n.fragbar(id) || !n.merkmale[id].geb.IsZero() {
			continue
		}
		out = append(out, GebVorschlag{Wer: n.knoten[id], Ich: id == n.ich, Tag: h.Tag, Belege: h.Belege, Jahre: h.Jahre})
	}
	slices.SortFunc(out, func(a, b GebVorschlag) int {
		return cmp.Or(cmp.Compare(b.Belege, a.Belege), strings.Compare(a.Wer.Name, b.Wer.Name), strings.Compare(a.Wer.ID, b.Wer.ID))
	})
	return out[:min(len(out), gebVorschlaege)]
}

// GebBestaetigt is a birthday the person confirmed.
type GebBestaetigt struct {
	Wer Knoten
	Tag string
}

// Geburtstage are the birthdays the person confirmed, by name.
func (n *Netz) Geburtstage() (out []GebBestaetigt) {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, a := range n.gebAntworten {
		if k, da := n.knoten[id]; da && a.Ja {
			out = append(out, GebBestaetigt{Wer: k, Tag: a.Tag})
		}
	}
	slices.SortFunc(out, func(a, b GebBestaetigt) int {
		return cmp.Or(strings.Compare(a.Wer.Name, b.Wer.Name), strings.Compare(a.Wer.ID, b.Wer.ID))
	})
	return out
}

// GeburtstagSagen keeps the person's answer about id's suggested day (ja:
// it is, else it is not); vergessen drops the answer. It answers all of
// them, for the vault.
func (n *Netz) GeburtstagSagen(id, tag string, ja, vergessen bool) (map[string]gebAntwort, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if vergessen {
		if _, da := n.gebAntworten[id]; !da {
			return nil, errFrageWeg
		}
		delete(n.gebAntworten, id)
		return maps.Clone(n.gebAntworten), nil
	}
	if h, da := n.gebHinweise[id]; !da || h.Tag != tag {
		return nil, errFrageWeg
	}
	n.gebAntworten[id] = gebAntwort{Tag: tag, Ja: ja, Am: n.now()}
	return maps.Clone(n.gebAntworten), nil
}

// GeburtstagSagen keeps the answer in the open vault.
func (u *Umfeld) GeburtstagSagen(innen Innen, id, tag string, ja, vergessen bool) error {
	u.halt.Lock()
	defer u.halt.Unlock()
	n := u.Netz()
	if n == nil {
		return errUmfeldZu
	}
	alle, err := n.GeburtstagSagen(id, tag, ja, vergessen)
	if err != nil {
		return err
	}
	return jsonImTresor(innen.umfeldGeburtstage(), alle)
}

// tagText is „03-14“ as the page says it: „14. März“.
func tagText(tag string) string {
	t, err := time.Parse("01-02", tag)
	if err != nil {
		return tag
	}
	return fmt.Sprintf("%d. %s", t.Day(), monate[t.Month()-1])
}

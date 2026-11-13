package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Filling an empty place (owner 2026-11-13, edit mode: stammbaum_frei.go;
// before it „Verbindungen bauen“, a list of every name to drag onto a
// person, owner 2026-11-12: „viele Seiten, die neu geladen werden, viel
// scrollen, viel suchen“). „Bearbeiten“ shows the tree with its empty
// places; a tap on one opens a sheet at the bottom that says what the
// place is („Kind von Jutta und Rolf“) with one name field. Under it
// first the relatives without a place who fit there, then the names that
// match what is typed, then „… neu anlegen“ (a person by hand,
// angelegt.go, who is asked about once a phone-book card of her name
// turns up). A tap posts in the background to verbindenPfad, which sets
// the line to each of the place's people (both parents of a couple's
// child) as the edge page does (Festlegen, Homes writes it) and answers
// the sentence, what to send for Rückgängig, and the tab anew: no page
// is loaded, the tree stays in edit mode. The same rules as every form:
// the person's session, the open vault, this origin (gleicheHerkunft).
// Without script the tab is the tree to look at and „+ Beziehung
// ergänzen“.

const (
	verbindenPfad   = "/tresor/umfeld/verbinden"
	saysBauSchon    = "Das stand schon so da."
	saysBauZurueck  = "Zurückgenommen."
	saysBauOhne     = "Tipp auf einen freien Platz und trag einen Namen ein."
	saysBauErklaert = "Gestrichelt umrandet ist frei. Tipp auf einen Platz und trag ein, wer dort hingehört."
)

// bauArten are the lines the editor sets: X is that of Y.
var bauArten = []string{"eltern", "kind", "partner", "geschwister"}

var errAusHomes = errors.New("Umfeld: Homes nahm eine alte Antwort nicht zurück")

// bauNamen are the shown people for the sheet of a place, as the script
// reads them: [id, name], by name.
func bauNamen(n *Netz) string {
	n.mu.Lock()
	namen := [][2]string{}
	for id, k := range n.knoten {
		if n.fragbar(id) && k.Name != "" && id != n.ich {
			namen = append(namen, [2]string{id, k.Name})
		}
	}
	n.mu.Unlock()
	slices.SortFunc(namen, func(a, b [2]string) int { return cmp.Or(strings.Compare(a[1], b[1]), strings.Compare(a[0], b[0])) })
	j, _ := json.Marshal(namen)
	return `<div id=bau-namen hidden data-namen="` + esc(string(j)) + `"></div>`
}

// bauAntwort is what verbindenPfad answers: the sentence (or what went
// wrong), the form that takes it back, and the tab as it now is.
type bauAntwort struct {
	Satz    string `json:"satz,omitempty"`
	Fehler  string `json:"fehler,omitempty"`
	Zurueck string `json:"zurueck,omitempty"`
	HTML    string `json:"html,omitempty"`
}

// umfeldVerbinden is the editor's post: x (an id) or neu (a name to
// make a person of) is art of y, a child or a parent of each y of
// several; or tu=zurueck with what the answer before gave.
func (w *Web) umfeldVerbinden(rw http.ResponseWriter, r *http.Request) {
	a, code := bauAntwort{Fehler: saysZu}, http.StatusLocked
	if innen, ok := w.Tresor.Innen(); ok && w.Umfeld.Netz() != nil {
		if r.PostFormValue("tu") == "zurueck" {
			a, code = w.bauZurueck(r, innen)
		} else {
			a, code = w.bauVerbinden(r, innen)
		}
		a.HTML = stammbaumInhalt(w.Umfeld.Netz())
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(code)
	json.NewEncoder(rw).Encode(a)
}

// bauFehler is err as the editor says it, with its status.
func bauFehler(err error) (string, int) {
	switch {
	case errors.Is(err, errAusHomes):
		return saysNichtEin, http.StatusBadGateway
	case errors.Is(err, errFrageWeg), errors.Is(err, errUmfeldZu):
		return saysFrageWeg, http.StatusConflict
	}
	log.Printf("archiv: Umfeld: eine Verbindung nicht gespeichert: %v", err)
	return saysUmfeldVerbergen, http.StatusInternalServerError
}

// bauVerbinden sets the line to each y; what was set before one failed
// stays, and can be taken back.
func (w *Web) bauVerbinden(r *http.Request, innen Innen) (bauAntwort, int) {
	art, ys, x, neu := r.PostFormValue("art"), r.PostForm["y"], r.PostFormValue("x"), r.PostFormValue("neu")
	if !slices.Contains(bauArten, art) || len(ys) == 0 || slices.Contains(ys, "") || (x == "") == (neu == "") || len(ys) > 1 && art != "kind" && art != "eltern" {
		return bauAntwort{Fehler: saysBauOhne}, http.StatusBadRequest
	}
	zurueck := url.Values{}
	fertig := func(err error) (bauAntwort, int) {
		a, code := bauAntwort{Satz: saysBauSchon}, http.StatusOK
		if keys := zurueck["paar"]; len(keys) > 0 {
			a.Satz = w.bauSatz(keys)
		}
		if len(zurueck) > 0 {
			a.Zurueck = zurueck.Encode()
		}
		if err != nil {
			a.Satz = ""
			a.Fehler, code = bauFehler(err)
		}
		return a, code
	}
	if neu != "" {
		key := ""
		if err := w.Umfeld.Angelegt(innen, func(n *Netz) (map[string]antwort, map[string]handPerson, error) {
			k, as, ps, err := n.Anlegen(neu, ys[0], art)
			key = k
			return as, ps, err
		}); err != nil {
			return fertig(err)
		}
		w.eintragenMit(r.Context(), innen, key)
		a, b, _ := strings.Cut(key, "|")
		if x = a; !istAngelegt(a) || a == ys[0] {
			x = b
		}
		zurueck.Add("paar", key)
		zurueck.Add("neu", x)
		ys = ys[1:]
	}
	for _, y := range ys {
		getan, err := w.festlegen(r.Context(), innen, x, y, art)
		if err != nil {
			return fertig(err)
		}
		if getan {
			zurueck.Add("paar", paarKey(x, y))
		}
	}
	return fertig(nil)
}

// bauSatz is what was just set as a sentence, with what the lines make
// her of the person: „Jonas ist Kind von Hedwig · für dich: Cousin/e“;
// the lines of one place name everyone at once: „Hanna ist Kind von
// Greta und Emil“.
func (w *Web) bauSatz(keys []string) string {
	n := w.Umfeld.Netz()
	g, ok := n.Gegebenes(keys[0])
	if !ok {
		return saysBauSchon
	}
	satz := strings.TrimSuffix(gegebenSatz(g), ".")
	wer := []Knoten{g.X, g.Y}
	for _, key := range keys[1:] {
		if h, ok := n.Gegebenes(key); ok && h.X.ID == g.X.ID {
			satz += " und " + duOder(h.Y, h.Ich)
			wer = append(wer, h.Y)
		}
	}
	for _, k := range wer {
		if v := n.Verwandt(k.ID, n.Ich()); v != "" && !g.Ich && k.ID != n.Ich() {
			satz += " · " + k.Name + " ist für dich: " + v
		}
	}
	return satz + "."
}

// bauZurueck takes back what one answer set: the lines (what Festlegen
// replaced comes back, and Homes writes it again), then the people it made.
func (w *Web) bauZurueck(r *http.Request, innen Innen) (bauAntwort, int) {
	for _, key := range slices.Backward(r.PostForm["paar"]) {
		if _, da := w.Umfeld.Netz().Gegebenes(key); !da {
			continue
		}
		if err := w.ausHomes(r.Context(), innen, key); err != nil {
			log.Printf("archiv: Umfeld: Rückgängig nicht an Homes: %v", err)
			return bauAntwort{Fehler: saysNichtZurueck}, http.StatusBadGateway
		}
		if err := w.Umfeld.Vergessen(innen, key); err != nil {
			f, code := bauFehler(err)
			return bauAntwort{Fehler: f}, code
		}
		w.eintragenMit(r.Context(), innen, key)
	}
	for _, id := range r.PostForm["neu"] {
		if _, da := w.Umfeld.Netz().Angelegt(id); !da {
			continue
		}
		if err := w.Umfeld.Angelegt(innen, func(n *Netz) (map[string]antwort, map[string]handPerson, error) { return n.AngelegtWeg(id, true) }); err != nil {
			f, code := bauFehler(err)
			return bauAntwort{Fehler: f}, code
		}
	}
	return bauAntwort{Satz: saysBauZurueck}, http.StatusOK
}

// festlegen sets x as art of y, asked or not, as the edge page does: what
// Homes wrote of the pair and of what followed from it leaves Homes
// first, then the new kind is kept and written with what follows from
// it. false: it stood so already, nothing written, nothing to undo.
func (w *Web) festlegen(ctx context.Context, innen Innen, x, y, art string) (bool, error) {
	key := paarKey(x, y)
	if g, da := w.Umfeld.Netz().Gegebenes(key); da && g.X.ID == x && g.Art == art || da && g.Y.ID == x && cmp.Or(gegen[g.Art], g.Art) == art {
		return false, nil
	}
	if err := w.ausHomes(ctx, innen, key); err != nil {
		log.Printf("archiv: Umfeld: eine alte Antwort nicht aus Homes: %v", err)
		return false, errAusHomes
	}
	if _, err := w.Umfeld.Festlegen(innen, x, y, art); err != nil {
		return false, err
	}
	w.eintragenMit(ctx, innen, key)
	return true, nil
}

// eintragenMit has Homes write the answer key and what followed from it;
// a refusal stays visible: the lists say „noch nicht eingetragen“.
func (w *Web) eintragenMit(ctx context.Context, innen Innen, key string) {
	w.eintragen(ctx, innen, key)
	for _, d := range w.Umfeld.Netz().Gefolgerte(key) {
		w.eintragen(ctx, innen, d.Key)
	}
}

// bauSkript is the editor: see the comment above. tab's classes are the
// mode (bearbeiten, ganz) and outlive the tab's content, which every
// answer fills anew. platz is the place tapped, namen the names to offer
// ([id, name]); leiste the sheet at the bottom: a place's name field
// with its suggestions, then the report with Rückgängig. It stays above
// the keyboard (visualViewport). A stroke that stands for several lines
// (data-mehr) offers them there.
const bauSkript = `<script>
document.addEventListener('DOMContentLoaded',function(){
if(!window.fetch||!window.URLSearchParams||!document.body.closest)return;document.documentElement.classList.add('skript');
var tab=document.getElementById('baum-tab');if(!tab)return;
var platz=null,namen=[],vv=window.visualViewport,leiste=el('div','bau-leiste');leiste.hidden=true;leiste.setAttribute('role','status');document.body.appendChild(leiste);
function el(t,k,x){var e=document.createElement(t);if(k)e.className=k;if(x)e.textContent=x;return e}
function knopf(x,k,f){var b=el('button',k,x);b.type='button';b.addEventListener('click',f);return b}
function klar(s){s=s.toLowerCase().replace(/ä/g,'ae').replace(/ö/g,'oe').replace(/ü/g,'ue').replace(/ß/g,'ss');try{s=s.normalize('NFD').replace(/[̀-ͯ]/g,'')}catch(e){}return s.replace(/\s+/g,' ').trim()}
function an(k){return tab.classList.contains(k)}
function baum(){return tab.querySelector(an('bearbeiten')?'#baum-frei':'#stammbaum')}
function mitte(){var b=baum(),d=b&&b.querySelector('[data-du]');if(!d)return;var r=d.getBoundingClientRect(),c=b.getBoundingClientRect();b.scrollLeft+=r.left-c.left-(c.width-r.width)/2}
function stand(){var m=tab.querySelector('#baum-modus'),g=tab.querySelector('#baum-ganz'),d=tab.querySelector('#bau-namen');
if(m)m.textContent=an('bearbeiten')?'Fertig':'Bearbeiten';if(g)g.textContent=an('ganz')?'Groß zeigen':'Ganzen Baum zeigen';
try{namen=d?JSON.parse(d.dataset.namen):[]}catch(e){namen=[]}}
function zu(){platz=null;leiste.hidden=true;leiste.textContent='';var z=tab.querySelector('a.ziel');if(z)z.classList.remove('ziel')}
function melden(text,zurueck){zu();leiste.appendChild(el('span','',text));var c=el('div','chips');
if(zurueck)c.appendChild(knopf('Rückgängig','chip',function(){var d=new URLSearchParams(zurueck);d.append('tu','zurueck');schicken(d)}));
c.appendChild(knopf('OK','leise',zu));leiste.appendChild(c);leiste.hidden=false}
function schicken(d){zu();leiste.textContent='Speichert …';leiste.hidden=false;
fetch('` + verbindenPfad + `',{method:'POST',body:d,credentials:'same-origin'}).then(function(r){return r.json()}).then(function(a){
if(a.html){var b=baum(),l=b?b.scrollLeft:0;tab.innerHTML=a.html;stand();b=baum();if(b)b.scrollLeft=l}
melden(a.fehler||a.satz||'',a.zurueck)
}).catch(function(){melden('Das hat nicht geklappt. Lade die Seite neu und versuch es noch einmal.')})}
function eintragen(id,name){var d=new URLSearchParams();d.append('art',platz.art);platz.y.forEach(function(y){d.append('y',y)});d.append(id?'x':'neu',id||name);schicken(d)}
function wert(){return leiste.querySelector('input').value.replace(/\s+/g,' ').trim()}
function genau(){var q=klar(wert());return namen.filter(function(n){return klar(n[1])===q&&platz.y.indexOf(n[0])<0})}
function vorschlagen(){var c=leiste.querySelector('.chips'),name=wert(),q=klar(name),vor=[],rest=[];c.textContent='';
namen.forEach(function(n){var passt=platz.vor.indexOf(n[0])>=0;if(platz.y.indexOf(n[0])>=0||(q?klar(n[1]).indexOf(q)<0:!passt))return;
(passt?vor:rest).push(knopf(n[1],'chip'+(passt?' vorschlag':''),function(){eintragen(n[0])}))});
vor.concat(rest).slice(0,8).forEach(function(b){c.appendChild(b)});
if(q&&!genau().length)c.appendChild(knopf('＋ „'+name+'“ neu anlegen','chip neu',function(){eintragen('',name)}));
c.appendChild(knopf('Abbrechen','leise',zu))}
function oeffnen(a){zu();var d=a.dataset;platz={art:d.art,y:d.y.split('|'),vor:d.vor?d.vor.split('|'):[]};a.classList.add('ziel');
var f=el('form'),i=el('input');i.placeholder='Name, auch neu';i.autocomplete='off';i.setAttribute('aria-label',d.text);f.appendChild(i);
leiste.appendChild(el('b','',d.text));leiste.appendChild(f);leiste.appendChild(el('div','chips'));
i.addEventListener('input',vorschlagen);f.addEventListener('submit',function(e){e.preventDefault();var da=genau(),name=wert();
if(da.length>1)i.setCustomValidity('Den Namen gibt es mehrmals. Tipp auf den richtigen.'),i.reportValidity(),i.setCustomValidity('');else if(name)eintragen(da.length?da[0][0]:'',name)});
vorschlagen();leiste.hidden=false;if(!platz.vor.length)i.focus()}
function mehr(m){zu();m.dataset.mehr.split('\n').forEach(function(z){var t=z.split('\t'),a=el('a','',t[1]);a.href=t[0];leiste.appendChild(a)});
leiste.appendChild(knopf('Schließen','leise',zu));leiste.hidden=false}
function hoch(){var u=window.innerHeight-vv.height-vv.offsetTop;leiste.style.bottom=u>60?u+12+'px':''}
if(vv){vv.addEventListener('resize',hoch);vv.addEventListener('scroll',hoch)}
stand();
tab.addEventListener('click',function(e){var t=e.target;
if(t.id==='baum-ganz'){tab.classList.toggle('ganz');stand();return}
if(t.id==='baum-modus'){zu();tab.classList.toggle('bearbeiten');stand();mitte();return}
var f=t.closest('.baum a.frei'),m=t.closest('.baum a[data-mehr]');
if(f){e.preventDefault();oeffnen(f)}else if(m){e.preventDefault();mehr(m)}});
});
</script>`

// bauCSS is the editor's and the new strokes' look.
const bauCSS = `
html:not(.skript) .baum-kopf,#baum-frei,.nur-bearbeiten,#baum-tab.bearbeiten .nur-zeigen{display:none}#baum-tab.bearbeiten #baum-frei,#baum-tab.bearbeiten .nur-bearbeiten{display:block}
.baum-kopf{display:flex;align-items:center;gap:8px;margin:12px 0 0}.baum-kopf+.baum{margin-top:8px}
.baum{touch-action:pan-x pan-y pinch-zoom}#baum-tab.ganz .baum svg{width:100%;height:auto}.baum text.wer{font-size:10px}.baum .linie.mit{stroke-width:1.2}.baum .linie.stamm,.baum .linie.kind,.baum .linie.geschwister{stroke-linecap:square}
.baum a.du rect{fill:var(--accent-soft)}.baum .linie.frei{stroke-width:1.2;stroke-dasharray:2 5;opacity:.7}
.baum a.frei rect{fill:transparent;stroke:var(--muted);stroke-dasharray:5 4;opacity:.75}.baum a.frei text{fill:var(--muted)}.baum a.frei text.plus{font-size:18px;font-weight:400}
.baum a.frei:hover rect,.baum a.frei:focus rect,.baum a.ziel rect{stroke:var(--accent);stroke-width:2.5;fill:var(--accent-soft);opacity:1}
.bau-leiste{position:fixed;left:16px;right:16px;bottom:calc(16px + env(safe-area-inset-bottom));z-index:6;max-width:36rem;margin:0 auto;display:grid;gap:8px;padding:12px 14px;border-radius:14px;background:var(--ink);color:var(--bg);font-size:.95rem;box-shadow:0 4px 18px rgba(0,0,0,.35)}
.bau-leiste a{display:flex;align-items:center;min-height:40px;color:var(--bg);font-weight:600}.bau-leiste button.leise{color:var(--bg)}.bau-leiste .chips{align-items:center;margin:0}.bau-leiste input{margin:0}.bau-leiste .chip.neu{border-style:dashed}
`

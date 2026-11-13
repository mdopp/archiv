package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// „Verbindungen bauen“ (owner 2026-11-12: „Aktuell gibt es viele Seiten,
// die neu geladen werden, viel scrollen, viel suchen … Können wir Namen
// gruppieren und per Drag and Drop verbinden?“): under the tree, every
// name the net knows, grouped by family name, with one field that finds a
// name or makes a new person of it (a person by hand, angelegt.go, who is
// asked about once a phone-book card of her name turns up). A name is
// dragged onto a person in the tree or onto another name, or tapped and
// then the person tapped: four chips ask what she is, Mutter/Vater, Kind,
// Partner/in or Geschwister, nothing else; the rest is read from those
// (verwandt.go). Several names go at once („Mehrere“). The chip posts in
// the background to verbindenPfad, which sets the relation as the edge
// page does (Festlegen, Homes writes it) and answers the sentence, what
// to send for Rückgängig, and the tab anew: no page is loaded. The same
// rules as every form: the person's session, the open vault, this
// origin (gleicheHerkunft). Without script the tab has „+ Beziehung
// ergänzen“ as before.

const (
	verbindenPfad   = "/tresor/umfeld/verbinden"
	saysBauSchon    = "Das stand schon so da."
	saysBauZurueck  = "Zurückgenommen."
	saysBauOhne     = "Wähle erst einen Namen, dann die Person, zu der er gehört."
	saysBauErklaert = "Zieh einen Namen auf eine Person im Baum oder auf einen anderen Namen. Oder tipp erst den Namen an, dann die Person. Wer fehlt, wird mit dem Namen neu angelegt."
)

// bauArten are the lines the editor sets: X is that of Y.
var bauArten = []string{"eltern", "kind", "partner", "geschwister"}

var errAusHomes = errors.New("Umfeld: Homes nahm eine alte Antwort nicht zurück")

// bauGruppe are the names of one family name; "" the rest.
type bauGruppe struct {
	Name string
	Wer  []Knoten
}

// BauNamen are the shown people for the editor, by family name: the
// family circle's names first, then by name; who shares hers with nobody
// under "".
func (n *Netz) BauNamen() (out []bauGruppe) {
	n.mu.Lock()
	defer n.mu.Unlock()
	nach := map[string][]Knoten{}
	for id, k := range n.knoten {
		if n.fragbar(id) && k.Name != "" && id != n.ich {
			nach[n.merkmale[id].family] = append(nach[n.merkmale[id].family], k)
		}
	}
	rest := nach[""]
	delete(nach, "")
	erst := map[string]bool{}
	for id := range n.familienkreis() {
		erst[n.merkmale[id].family] = true
	}
	for _, f := range slices.SortedFunc(maps.Keys(nach), func(a, b string) int {
		return cmp.Or(cmp.Compare(map[bool]int{true: 0, false: 1}[erst[a]], map[bool]int{true: 0, false: 1}[erst[b]]), strings.Compare(a, b))
	}) {
		if len(nach[f]) < 2 {
			rest = append(rest, nach[f]...)
			continue
		}
		g := bauGruppe{Name: f, Wer: nach[f]}
		if ws := strings.Fields(g.Wer[0].Name); klar(ws[len(ws)-1]) == f {
			g.Name = ws[len(ws)-1]
		}
		out = append(out, g)
	}
	out = append(out, bauGruppe{Wer: rest})
	for _, g := range out {
		slices.SortFunc(g.Wer, func(a, b Knoten) int { return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID)) })
	}
	return out
}

// bauAbschnitt is the editor under the tree; drin are those in the tree.
func bauAbschnitt(b *strings.Builder, n *Netz, sb Stammbaum) {
	drin := map[string]bool{}
	for _, p := range sb.Personen {
		drin[p.ID] = true
	}
	fmt.Fprintf(b, `<section class=karte id=bauen><h2>Verbindungen bauen</h2><p class=k>%s</p><input id=bau-suche type=search placeholder="Name suchen oder neu eintragen" autocomplete=off aria-label="Name suchen oder neu eintragen"><button type=button class=bau-name id=bau-neu hidden></button><div class=bau-liste id=bau-liste>`, esc(saysBauErklaert))
	if ich := n.Ich(); ich != "" {
		fmt.Fprintf(b, `<div class=bau-gruppe><button type=button class="bau-name du" data-id="%s" data-name=Du>Du</button></div>`, esc(ich))
	}
	for _, g := range n.BauNamen() {
		if len(g.Wer) == 0 {
			continue
		}
		fmt.Fprintf(b, `<div class=bau-gruppe><h3>%s</h3>`, esc(cmp.Or(g.Name, "Weitere")))
		for _, k := range g.Wer {
			fmt.Fprintf(b, `<button type=button class=bau-name data-id="%s" data-name="%s"%s>%s</button>`, esc(k.ID), esc(k.Name), map[bool]string{true: ` data-drin title="steht im Stammbaum"`}[drin[k.ID]], esc(k.mitKreuz()))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div></section>`)
}

// bauAntwort is what verbindenPfad answers: the sentence (or what went
// wrong), the form that takes it back, and the tab as it now is.
type bauAntwort struct {
	Satz    string `json:"satz,omitempty"`
	Fehler  string `json:"fehler,omitempty"`
	Zurueck string `json:"zurueck,omitempty"`
	HTML    string `json:"html,omitempty"`
}

// umfeldVerbinden is the editor's post: x (ids) and neu (names to make)
// are each art of y; or tu=zurueck with what the answer before gave.
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

// bauVerbinden sets each line; what was set before one failed stays, and
// can be taken back.
func (w *Web) bauVerbinden(r *http.Request, innen Innen) (bauAntwort, int) {
	art, y, xs, neue := r.PostFormValue("art"), r.PostFormValue("y"), r.PostForm["x"], r.PostForm["neu"]
	if !slices.Contains(bauArten, art) || y == "" || len(xs)+len(neue) == 0 {
		return bauAntwort{Fehler: saysBauOhne}, http.StatusBadRequest
	}
	zurueck := url.Values{}
	fertig := func(err error) (bauAntwort, int) {
		a, code := bauAntwort{Satz: saysBauSchon}, http.StatusOK
		if keys := zurueck["paar"]; len(keys) == 1 {
			a.Satz = w.bauSatz(keys[0])
		} else if len(keys) > 1 {
			a.Satz = fmt.Sprintf("%d Verbindungen gespeichert.", len(keys))
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
	for _, x := range xs {
		getan, err := w.festlegen(r.Context(), innen, x, y, art)
		if err != nil {
			return fertig(err)
		}
		if getan {
			zurueck.Add("paar", paarKey(x, y))
		}
	}
	for _, name := range neue {
		key := ""
		if err := w.Umfeld.Angelegt(innen, func(n *Netz) (map[string]antwort, map[string]handPerson, error) {
			k, as, ps, err := n.Anlegen(name, y, art)
			key = k
			return as, ps, err
		}); err != nil {
			return fertig(err)
		}
		w.eintragenMit(r.Context(), innen, key)
		zurueck.Add("paar", key)
		if a, b, _ := strings.Cut(key, "|"); istAngelegt(a) && a != y {
			zurueck.Add("neu", a)
		} else {
			zurueck.Add("neu", b)
		}
	}
	return fertig(nil)
}

// bauSatz is the line just set as a sentence, with what the lines make
// her of the person: „Jonas ist Kind von Hedwig · für dich: Cousin/e“.
func (w *Web) bauSatz(key string) string {
	n := w.Umfeld.Netz()
	g, ok := n.Gegebenes(key)
	if !ok {
		return saysBauSchon
	}
	satz := strings.TrimSuffix(gegebenSatz(g), ".")
	for _, k := range []Knoten{g.X, g.Y} {
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

// bauSkript is the editor: see the comment above. A name is {id | neu,
// name}; wahl the chosen ones, ziel the person they go to; zug a drag (a
// mouse starts one by moving, a finger by holding 350 ms, so a swipe
// still scrolls the page). leiste is the bar at the bottom that says what
// is chosen, asks the kind, and reports with Rückgängig. A stroke that
// stands for several lines (data-mehr) offers them there.
const bauSkript = `<script>
document.addEventListener('DOMContentLoaded',function(){
if(!window.fetch||!window.URLSearchParams||!document.body.closest)return;document.documentElement.classList.add('skript');
function Q(s){var t=document.getElementById('baum-tab');return t&&t.querySelector(s)}
function A(s,f){var t=document.getElementById('baum-tab');if(t)[].forEach.call(t.querySelectorAll(s),f)}
function im(e){return e.target.closest&&e.target.closest('#baum-tab')}
var wahl=[],mehrere=false,ziel=null,zug=null,gezogen=0,leiste=document.createElement('div');
leiste.className='bau-leiste';leiste.hidden=true;leiste.setAttribute('role','status');document.body.appendChild(leiste);
function el(t,k,x){var e=document.createElement(t);if(k)e.className=k;if(x)e.textContent=x;return e}
function knopf(x,k,f){var b=el('button',k,x);b.type='button';b.addEventListener('click',f);return b}
function klar(s){s=s.toLowerCase().replace(/ä/g,'ae').replace(/ö/g,'oe').replace(/ü/g,'ue').replace(/ß/g,'ss');try{s=s.normalize('NFD').replace(/[̀-ͯ]/g,'')}catch(e){}return s.replace(/\s+/g,' ').trim()}
function von(e){return e?{id:e.dataset.id||'',neu:e.dataset.neu||'',name:e.dataset.name||''}:null}
function gleich(a,b){return a.id===b.id&&a.neu===b.neu}
function drin(p){return wahl.some(function(w){return gleich(w,p)})}
function zu(){leiste.hidden=true;leiste.textContent=''}
function lassen(){wahl=[];ziel=null;malen()}
function malen(){
A('.bau-name',function(b){b.classList.toggle('gewaehlt',drin(von(b)))});
leiste.textContent='';
var wer=wahl.map(function(w){return w.name}).join(', ');
if(ziel){
leiste.appendChild(el('b','',wer+(wahl.length>1?' sind':wer==='Du'?' bist':' ist')+' … von '+(ziel.name==='Du'?'dir':ziel.name)));
var c=el('div','chips');[['eltern','Mutter/Vater'],['kind','Kind'],['partner','Partner/in'],['geschwister','Geschwister']].forEach(function(a){c.appendChild(knopf(a[1],'chip',function(){senden(a[0])}))});
c.appendChild(knopf('Abbrechen','leise',lassen));leiste.appendChild(c)
}else if(wahl.length){
leiste.appendChild(el('span','',wer+' gewählt. Tipp jetzt auf die Person, zu der '+(wahl.length>1?'sie gehören':'der Name gehört')+': im Baum oder in der Liste.'));
var d=el('div','chips'),m=knopf(mehrere?'Mehrere: an':'Mehrere','chip'+(mehrere?' gewaehlt':''),function(){mehrere=!mehrere;malen()});m.setAttribute('aria-pressed',mehrere);d.appendChild(m);
d.appendChild(knopf('Abbrechen','leise',lassen));leiste.appendChild(d)}
leiste.hidden=!leiste.firstChild}
function fragen(z){if(!z||z.neu||drin(z)||!wahl.length)return;ziel=z;malen()}
function melden(text,zurueck){leiste.textContent='';leiste.appendChild(el('span','',text));var c=el('div','chips');
if(zurueck)c.appendChild(knopf('Rückgängig','chip',function(){senden('',zurueck)}));
c.appendChild(knopf('OK','leise',zu));leiste.appendChild(c);leiste.hidden=false}
function filtern(){var s=Q('#bau-suche'),n=Q('#bau-neu');if(!s||!n)return;var q=klar(s.value),genau=false;
A('#bau-liste .bau-name',function(b){var k=klar(b.dataset.name);b.hidden=!!q&&k.indexOf(q)<0;if(k===q)genau=true});
A('.bau-gruppe',function(g){g.hidden=!g.querySelector('.bau-name:not([hidden])')});
var name=s.value.replace(/\s+/g,' ').trim();n.hidden=!q||genau;n.dataset.neu=n.dataset.name=name;n.textContent='＋ „'+name+'“ neu anlegen'}
function tauschen(h){var tab=document.getElementById('baum-tab'),s=Q('#bau-suche'),q=s?s.value:'',b=Q('.baum'),l=b?b.scrollLeft:0,g=b&&b.classList.contains('ganz'),li=Q('#bau-liste'),t=li?li.scrollTop:0;if(!tab)return;
tab.innerHTML=h;s=Q('#bau-suche');if(s)s.value=q;filtern();b=Q('.baum');if(b){if(g)ganz(b);b.scrollLeft=l}li=Q('#bau-liste');if(li)li.scrollTop=t}
function ganz(b){var an=b.classList.toggle('ganz'),k=Q('#baum-ganz');if(k)k.textContent=an?'Groß zeigen':'Ganzen Baum zeigen'}
function senden(art,zurueck){var d=new URLSearchParams(zurueck||'');
if(zurueck)d.append('tu','zurueck');else{d.append('art',art);d.append('y',ziel.id);wahl.forEach(function(w){if(w.neu)d.append('neu',w.neu);else d.append('x',w.id)})}
leiste.textContent='Speichert …';wahl=[];ziel=null;
fetch('` + verbindenPfad + `',{method:'POST',body:d,credentials:'same-origin'}).then(function(r){return r.json()}).then(function(a){
if(a.html)tauschen(a.html);malen();melden(a.fehler||a.satz||'',a.zurueck)
}).catch(function(){malen();melden('Das hat nicht geklappt. Lade die Seite neu und versuch es noch einmal.')})}
function mehr(m){leiste.textContent='';m.dataset.mehr.split('\n').forEach(function(z){var t=z.split('\t'),a=el('a','',t[1]);a.href=t[0];leiste.appendChild(a)});
leiste.appendChild(knopf('Schließen','leise',zu));leiste.hidden=false}
document.addEventListener('input',function(e){if(e.target.id==='bau-suche')filtern()});
document.addEventListener('click',function(e){if(!im(e))return;
if(Date.now()-gezogen<400){e.preventDefault();return}
var t=e.target;if(t.id==='baum-ganz'){ganz(Q('.baum'));return}
var n=t.closest('.bau-name'),p=t.closest('.baum a[data-id]'),m=t.closest('.baum a[data-mehr]');
if(n){if(ziel)return;var w=von(n),i=wahl.findIndex(function(x){return gleich(x,w)});
if(i>=0)wahl.splice(i,1);else if(!wahl.length||mehrere)wahl.push(w);else{fragen(w);return}
malen();return}
if(p&&wahl.length){e.preventDefault();if(!ziel)fragen(von(p));return}
if(m){e.preventDefault();mehr(m)}});
function unter(x,y){var e=document.elementFromPoint(x,y);e=e&&e.closest&&(e.closest('.baum a[data-id]')||e.closest('#bau-liste .bau-name'));return e&&!drin(von(e))?e:null}
function los(z){z.an=true;var w=von(z.n);if(!drin(w)){if(!mehrere)wahl=[];wahl.push(w)}ziel=null;malen();
z.geist=el('div','bau-geist',wahl.length>1?wahl.length+' Namen':w.name);document.body.appendChild(z.geist);setzen(z,z.x,z.y);if(navigator.vibrate)navigator.vibrate(10)}
function setzen(z,x,y){z.geist.style.left=x+'px';z.geist.style.top=y+'px';var u=unter(x,y);
if(z.ueber!==u){if(z.ueber)z.ueber.classList.remove('ziel');if(u)u.classList.add('ziel');z.ueber=u}
if(y<70)window.scrollBy(0,-14);else if(y>window.innerHeight-70)window.scrollBy(0,14);
var b=Q('.baum'),r=b&&b.getBoundingClientRect();if(r&&y>r.top&&y<r.bottom){if(x<r.left+40)b.scrollLeft-=14;else if(x>r.right-40)b.scrollLeft+=14}}
function ende(e,ab){var z=zug;if(!z||e.pointerId!==z.id)return;zug=null;clearTimeout(z.zeit);if(!z.an)return;
z.geist.remove();if(z.ueber)z.ueber.classList.remove('ziel');gezogen=Date.now();if(!ab&&z.ueber)fragen(von(z.ueber))}
document.addEventListener('pointerdown',function(e){var n=im(e)&&e.target.closest('.bau-name');if(!n||ziel||e.button>0)return;
var z={n:n,x:e.clientX,y:e.clientY,id:e.pointerId,an:false};zug=z;if(e.pointerType!=='mouse')z.zeit=setTimeout(function(){if(zug===z)los(z)},350)});
document.addEventListener('pointermove',function(e){var z=zug;if(!z||e.pointerId!==z.id)return;
if(z.an){e.preventDefault();setzen(z,e.clientX,e.clientY);return}
var d=Math.abs(e.clientX-z.x)+Math.abs(e.clientY-z.y);if(e.pointerType==='mouse'){if(d>6)los(z)}else if(d>10){clearTimeout(z.zeit);zug=null}});
document.addEventListener('pointerup',function(e){ende(e,false)});
document.addEventListener('pointercancel',function(e){ende(e,true)});
document.addEventListener('touchmove',function(e){if(zug&&zug.an)e.preventDefault()},{passive:false});
document.addEventListener('contextmenu',function(e){if(im(e)&&e.target.closest('.bau-name'))e.preventDefault()});
});
</script>`

// bauCSS is the editor's and the new strokes' look.
const bauCSS = `
html:not(.skript) #bauen,html:not(.skript) #baum-ganz{display:none}
.baum{touch-action:pan-x pan-y pinch-zoom}.baum.ganz svg{width:100%;height:auto}.baum text.wer{font-size:10px}.baum .linie.mit{stroke-width:1.2}.baum .linie.stamm,.baum .linie.kind,.baum .linie.geschwister{stroke-linecap:square}
.baum a.du rect{fill:var(--accent-soft)}.baum a.ziel rect{stroke:var(--accent);stroke-width:3;fill:var(--accent-soft)}
.bau-liste{max-height:15rem;overflow-y:auto;overscroll-behavior:contain;background:var(--card);border-radius:14px;padding:10px 10px 4px;margin-top:8px}
.bau-gruppe h3{font-size:.8rem;font-weight:600;color:var(--muted);margin:2px 2px 6px}
.bau-name{background:var(--panel);color:var(--ink);border:1.5px solid var(--edge);font-weight:400;margin:0 6px 6px 0;touch-action:manipulation;user-select:none;-webkit-user-select:none;-webkit-touch-callout:none}
.bau-name[data-drin]::before{content:"● ";color:var(--ok);font-size:.7em}.bau-name.du{border-color:var(--accent)}#bau-neu{margin-top:8px;border-style:dashed;color:var(--accent)}
.bau-name.gewaehlt{background:var(--accent);border-color:var(--accent);color:#fff}.bau-name.ziel{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-soft)}
.bau-geist{position:fixed;z-index:9;pointer-events:none;transform:translate(-50%,-130%);padding:8px 14px;border-radius:999px;background:var(--accent);color:#fff;font-weight:600;box-shadow:0 4px 14px rgba(0,0,0,.4);white-space:nowrap}
.bau-leiste{position:fixed;left:16px;right:16px;bottom:calc(16px + env(safe-area-inset-bottom));z-index:6;max-width:36rem;margin:0 auto;display:grid;gap:8px;padding:12px 14px;border-radius:14px;background:var(--ink);color:var(--bg);font-size:.95rem;box-shadow:0 4px 18px rgba(0,0,0,.35)}
.bau-leiste a{display:flex;align-items:center;min-height:40px;color:var(--bg);font-weight:600}.bau-leiste button.leise{color:var(--bg)}.bau-leiste .chips{align-items:center}
`

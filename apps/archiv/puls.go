package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Puls tells open pages that something changed (the vault, a packet, the
// mail, an import): each page holds a server-sent event stream and fetches
// what changed, instead of reloading itself on a timer.
type Puls struct {
	mu      sync.Mutex
	abos    map[chan struct{}]struct{}
	schluss chan struct{}
}

func (p *Puls) ende() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.schluss == nil {
		p.schluss = make(chan struct{})
	}
	return p.schluss
}

// Schluss ends every stream: the server is stopping, and an open tab must
// not hold the shutdown (whose time the vault needs to close).
func (p *Puls) Schluss() {
	p.ende()
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.schluss:
	default:
		close(p.schluss)
	}
}

// Abo is one listener; Ende ends it.
func (p *Puls) Abo() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	p.mu.Lock()
	if p.abos == nil {
		p.abos = map[chan struct{}]struct{}{}
	}
	p.abos[c] = struct{}{}
	p.mu.Unlock()
	return c, func() {
		p.mu.Lock()
		delete(p.abos, c)
		p.mu.Unlock()
	}
}

// Senden wakes every listener; one that has not read yet keeps one wake.
func (p *Puls) Senden() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.abos {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// pulsHerz keeps a quiet stream alive through proxies.
var pulsHerz = 25 * time.Second

// live is GET /tresor/live: an event per change, a comment as heartbeat.
func (w *Web) live(rw http.ResponseWriter, r *http.Request) {
	fl, ok := rw.(http.Flusher)
	if !ok || w.Puls == nil {
		http.Error(rw, "kein Live-Kanal", http.StatusNotImplemented)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Accel-Buffering", "no")
	c, ende := w.Puls.Abo()
	defer ende()
	fmt.Fprint(rw, "retry: 3000\n\n")
	fl.Flush()
	herz := time.NewTicker(pulsHerz)
	defer herz.Stop()
	schluss := w.Puls.ende()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-schluss:
			return
		case <-c:
			fmt.Fprint(rw, "event: neu\ndata: 1\n\n")
		case <-herz.C:
			fmt.Fprint(rw, ": da\n\n")
		}
		fl.Flush()
	}
}

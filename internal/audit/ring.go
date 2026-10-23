package audit

import (
	"slices"
	"strings"
	"sync"
	"time"
)

// Record is one connection a node served: which node, and where it went.
type Record struct {
	ID uint64    `json:"id"`
	At time.Time `json:"at"`
	// Node is the key of the node the connection arrived on; Name is what the
	// page calls it.
	Node     string `json:"node"`
	Name     string `json:"name"`
	Machine  string `json:"machine"`
	Port     int    `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Target   string `json:"target"`
	Count    int    `json:"count"`
}

func (r *Record) sameVisit(o *Record) bool {
	return r.Node == o.Node && r.Target == o.Target
}

// Capacity is how many records the panel keeps. Nothing is persisted: the list
// is a live view, and a restart starts it over.
const Capacity = 1000

// mergeWindow folds repeated visits together. A browser opens several
// connections to the same host within a second; listing each one would push
// everything else out of the buffer without saying anything new.
const mergeWindow = time.Minute

// mergeScan bounds how far back a repeat is looked for.
const mergeScan = 64

// Ring holds the newest records first.
type Ring struct {
	mu      sync.Mutex
	records []*Record
	nextID  uint64
}

func NewRing() *Ring { return &Ring{} }

// Add stores a record, folding it into a recent identical visit if there is one.
func (r *Ring) Add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := 0; i < len(r.records) && i < mergeScan; i++ {
		old := r.records[i]
		if old.sameVisit(&rec) && absDuration(rec.At.Sub(old.At)) <= mergeWindow {
			old.Count++
			if rec.At.After(old.At) {
				old.At = rec.At
			}
			// The repeat is the newest activity, so it moves to the top.
			copy(r.records[1:i+1], r.records[:i])
			r.records[0] = old
			return
		}
	}

	r.nextID++
	rec.ID = r.nextID
	if rec.Count == 0 {
		rec.Count = 1
	}
	r.records = slices.Insert(r.records, 0, &rec)
	if len(r.records) > Capacity {
		r.records = r.records[:Capacity]
	}
}

// Filter narrows a page. Zero values match everything.
type Filter struct {
	Machine string
	Query   string
	// Until, when set, hides records added after the one with this ID, so a
	// paused view pages through a fixed list while new connections arrive.
	Until uint64
}

func (f Filter) match(r *Record) bool {
	if f.Until != 0 && r.ID > f.Until {
		return false
	}
	if f.Machine != "" && r.Machine != f.Machine {
		return false
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	if q == "" {
		return true
	}
	for _, s := range []string{r.Name, r.Machine, r.Protocol, r.Target} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// Page returns one page of matching records, newest first, and the number of
// matches in total. page counts from 1.
func (r *Ring) Page(page, size int, f Filter) ([]Record, int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if page < 1 {
		page = 1
	}
	start := (page - 1) * size
	out := make([]Record, 0, size)
	total := 0
	for _, rec := range r.records {
		if !f.match(rec) {
			continue
		}
		if total >= start && len(out) < size {
			out = append(out, *rec)
		}
		total++
	}
	return out, total
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

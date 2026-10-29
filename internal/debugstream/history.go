package debugstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const historyBytes = 4 << 20
const historyRecords = 1024

type packet struct {
	seq  int64
	data []byte
}
type history struct {
	mu      sync.Mutex
	header  []byte
	records []packet
	bytes   int
	changed chan struct{}
}

func (h *history) publish(raw []byte) {
	var meta struct {
		Type string `json:"type"`
		Seq  int64  `json:"seq"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if meta.Type == "header" {
		h.header = raw
	} else {
		for len(h.records) > 0 && (len(h.records) >= historyRecords || h.bytes+len(raw) > historyBytes) {
			h.bytes -= len(h.records[0].data)
			h.records[0] = packet{}
			h.records = h.records[1:]
		}
		if len(raw) <= historyBytes {
			h.records = append(h.records, packet{meta.Seq, raw})
			h.bytes += len(raw)
		}
	}
	close(h.changed)
	h.changed = make(chan struct{})
}
func (h *history) after(seq int64) ([]packet, <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []packet
	if seq == 0 && h.header != nil {
		out = append(out, packet{1, h.header})
	}
	for _, p := range h.records {
		if p.seq > seq {
			out = append(out, p)
		}
	}
	return out, h.changed
}
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	control := http.NewResponseController(w)
	if control.Flush() != nil {
		return
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	var seq int64
	for {
		packets, changed := s.ring.after(seq)
		for _, p := range packets {
			_ = control.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := fmt.Fprintf(w, "data: %s\n\n", p.data); err != nil {
				return
			}
			seq = p.seq
		}
		if len(packets) > 0 && control.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		case <-tick.C:
			_ = control.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if control.Flush() != nil {
				return
			}
		}
	}
}

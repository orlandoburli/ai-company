package agent

import "sync"

// StreamEvent is what live job subscribers receive: an appended log entry or a
// job state transition. The SSE handler serialises these as `log` / `state`.
type StreamEvent struct {
	Type string
	Log  *LogEntry
	Job  *Job
}

// Hub fans job events out to in-process subscribers (the SSE endpoint). It is
// deliberately tiny: the MVP runs one server process, and a subscriber that
// falls behind is dropped rather than allowed to block the worker — the client
// reconnects with after_seq and replays from Postgres, which is the durable
// log.
type Hub struct {
	mu     sync.Mutex
	nextID int
	subs   map[string]map[int]chan StreamEvent
}

// NewHub builds an empty hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[int]chan StreamEvent)}
}

// Subscribe returns a stream of events for one job and an unsubscribe func.
// Callers should subscribe before replaying persisted logs and skip live
// entries already replayed by seq.
func (h *Hub) Subscribe(jobID string) (<-chan StreamEvent, func()) {
	ch := make(chan StreamEvent, 256)
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	if h.subs[jobID] == nil {
		h.subs[jobID] = make(map[int]chan StreamEvent)
	}
	h.subs[jobID][id] = ch
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if m := h.subs[jobID]; m != nil {
				if c, ok := m[id]; ok {
					delete(m, id)
					close(c)
				}
				if len(m) == 0 {
					delete(h.subs, jobID)
				}
			}
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

// Publish delivers an event to every subscriber of the job. A subscriber whose
// buffer is full is dropped (and its channel closed) rather than blocking the
// worker; it can reconnect and replay.
func (h *Hub) Publish(ev StreamEvent) {
	jobID := ""
	switch {
	case ev.Log != nil:
		jobID = ev.Log.JobID
	case ev.Job != nil:
		jobID = ev.Job.ID
	}
	if jobID == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.subs[jobID]
	for id, ch := range m {
		select {
		case ch <- ev:
		default:
			delete(m, id)
			close(ch)
		}
	}
	if len(m) == 0 {
		delete(h.subs, jobID)
	}
}

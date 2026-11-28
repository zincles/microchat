package live

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	LeaseDuration     = 90 * time.Second
	subscriberBuffer = 16
)

var ErrInvalidClientID = errors.New("client_id must be a UUID")

type Event struct {
	Name      string
	SessionID string
	Revision  uint64
	Clients   int
}

func (event Event) MarshalJSON() ([]byte, error) {
	switch event.Name {
	case "session_changed", "session_deleted":
		return json.Marshal(struct {
			SessionID string `json:"session_id"`
			Revision  uint64 `json:"revision"`
		}{SessionID: event.SessionID, Revision: event.Revision})
	case "presence":
		return json.Marshal(struct {
			Clients int `json:"online_clients"`
		}{Clients: event.Clients})
	case "agents_changed":
		return []byte("{}"), nil
	default:
		return nil, errors.New("unknown live event")
	}
}

type subscriber struct {
	events chan Event
	closed bool
}

// Hub owns process-local client leases and global invalidation subscribers.
type Hub struct {
	mu          sync.Mutex
	clients     map[string]time.Time
	subscribers map[*subscriber]struct{}
	revision    uint64
	now         func() time.Time
	stop        chan struct{}
	once        sync.Once
}

func NewHub() *Hub {
	h := &Hub{clients: make(map[string]time.Time), subscribers: make(map[*subscriber]struct{}), now: time.Now, stop: make(chan struct{})}
	go h.expireLoop()
	return h
}

// Close stops lease maintenance; it is primarily useful for orderly server shutdown and tests.
func (h *Hub) Close() { h.once.Do(func() { close(h.stop) }) }

func validClientID(id string) bool {
	if len(id) != 36 || strings.TrimSpace(id) != id {
		return false
	}
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == strings.ToLower(id)
}

func (h *Hub) Heartbeat(clientID string) (int, error) {
	if !validClientID(clientID) {
		return 0, ErrInvalidClientID
	}
	now := h.now()
	h.mu.Lock()
	before := h.onlineCountLocked(now)
	h.clients[clientID] = now
	after := h.onlineCountLocked(now)
	if after != before {
		h.broadcastLocked(Event{Name: "presence", Clients: after})
	}
	h.mu.Unlock()
	return after, nil
}

func (h *Hub) PublishSessionChanged(sessionID string) { h.publishSession("session_changed", sessionID) }
func (h *Hub) PublishSessionDeleted(sessionID string) { h.publishSession("session_deleted", sessionID) }

// PublishAgentsChanged 通知所有订阅者重新校验智能体和提示词片段。
func (h *Hub) PublishAgentsChanged() {
	h.mu.Lock()
	h.broadcastLocked(Event{Name: "agents_changed"})
	h.mu.Unlock()
}

func (h *Hub) publishSession(name, sessionID string) {
	h.mu.Lock()
	h.revision++
	h.broadcastLocked(Event{Name: name, SessionID: sessionID, Revision: h.revision})
	h.mu.Unlock()
}

func (h *Hub) onlineCountLocked(now time.Time) int {
	count := 0
	for id, last := range h.clients {
		if now.Sub(last) >= LeaseDuration {
			delete(h.clients, id)
		} else {
			count++
		}
	}
	return count
}

func (h *Hub) expireLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.mu.Lock()
			before := len(h.clients)
			after := h.onlineCountLocked(h.now())
			if after != before {
				h.broadcastLocked(Event{Name: "presence", Clients: after})
			}
			h.mu.Unlock()
		case <-h.stop:
			return
		}
	}
}

func (h *Hub) broadcastLocked(event Event) {
	for sub := range h.subscribers {
		select {
		case sub.events <- event:
		default:
			// Force EventSource to reconnect; the client then reconciles missed invalidations.
			delete(h.subscribers, sub)
			if !sub.closed {
				sub.closed = true
				close(sub.events)
			}
		}
	}
}

func (h *Hub) Subscribe() (<-chan Event, func()) {
	sub := &subscriber{events: make(chan Event, subscriberBuffer)}
	h.mu.Lock()
	h.subscribers[sub] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return sub.events, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers, sub)
			if !sub.closed {
				sub.closed = true
				close(sub.events)
			}
			h.mu.Unlock()
		})
	}
}

func (h *Hub) ServeEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	events, unsubscribe := h.Subscribe()
	defer unsubscribe()
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case event, open := <-events:
			if !open {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("event: " + event.Name + "\ndata: " + string(data) + "\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

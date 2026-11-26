package live

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHeartbeatCountsUniqueClientsAndBroadcastsChanges(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	first := "018f47b2-8f12-7abc-8def-1234567890ab"
	second := "018f47b2-8f12-7abc-8def-1234567890ac"
	if count, err := hub.Heartbeat(first); err != nil || count != 1 {
		t.Fatalf("first heartbeat = %d, %v", count, err)
	}
	if event := <-events; event.Name != "presence" || event.Clients != 1 {
		t.Fatalf("first presence event = %+v", event)
	}
	if count, err := hub.Heartbeat(first); err != nil || count != 1 {
		t.Fatalf("repeat heartbeat = %d, %v", count, err)
	}
	if len(events) != 0 {
		t.Fatal("same client heartbeat broadcast despite unchanged count")
	}
	if count, err := hub.Heartbeat(second); err != nil || count != 2 {
		t.Fatalf("second heartbeat = %d, %v", count, err)
	}
	if event := <-events; event.Name != "presence" || event.Clients != 2 {
		t.Fatalf("second presence event = %+v", event)
	}
}

func TestHeartbeatExpiresLeasesAndBoundsClientIDs(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	now := time.Now()
	hub.now = func() time.Time { return now }
	oldID := "018f47b2-8f12-7abc-8def-1234567890ab"
	newID := "018f47b2-8f12-7abc-8def-1234567890ac"
	if _, err := hub.Heartbeat(oldID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(LeaseDuration + time.Nanosecond)
	if count, err := hub.Heartbeat(newID); err != nil || count != 1 {
		t.Fatalf("expired lease count = %d, %v", count, err)
	}
	if _, err := hub.Heartbeat("unbounded-not-a-uuid"); err != ErrInvalidClientID {
		t.Fatalf("invalid client id error = %v", err)
	}
}

func TestSessionBroadcastRevisionAndPayloadShape(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	hub.PublishSessionChanged("session-a")
	hub.PublishSessionDeleted("session-b")
	changed, deleted := <-events, <-events
	if changed.Name != "session_changed" || changed.SessionID != "session-a" || changed.Revision != 1 {
		t.Fatalf("changed event = %+v", changed)
	}
	if deleted.Name != "session_deleted" || deleted.SessionID != "session-b" || deleted.Revision != 2 {
		t.Fatalf("deleted event = %+v", deleted)
	}
	data, err := json.Marshal(changed)
	if err != nil || string(data) != `{"session_id":"session-a","revision":1}` {
		t.Fatalf("event payload = %s, %v", data, err)
	}
	presence, err := json.Marshal(Event{Name: "presence", Clients: 0})
	if err != nil || string(presence) != `{"online_clients":0}` {
		t.Fatalf("zero presence payload = %s, %v", presence, err)
	}
}

func TestSubscriberDisconnectAndSlowSubscriberAreBounded(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	events, unsubscribe := hub.Subscribe()
	unsubscribe()
	hub.PublishSessionChanged("removed")
	if _, open := <-events; open {
		t.Fatal("unsubscribed channel remained open")
	}

	slow, stop := hub.Subscribe()
	for range subscriberBuffer {
		hub.PublishSessionChanged("session")
	}
	hub.PublishSessionChanged("overflow")
	stop()
	for range subscriberBuffer {
		if _, open := <-slow; !open {
			t.Fatal("buffer closed before queued events were drained")
		}
	}
	if _, open := <-slow; open {
		t.Fatal("slow subscriber remained open after overflow")
	}
	hub.mu.Lock()
	subscribers := len(hub.subscribers)
	hub.mu.Unlock()
	if subscribers != 0 {
		t.Fatalf("subscriber count after overflow = %d", subscribers)
	}

}

func TestSSERequestCancellationUnsubscribes(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/v1/live/events", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		hub.ServeEvents(httptest.NewRecorder(), req)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop on request cancellation")
	}
	hub.mu.Lock()
	subscribers := len(hub.subscribers)
	hub.mu.Unlock()
	if subscribers != 0 {
		t.Fatalf("subscriber count after disconnect = %d", subscribers)
	}
}

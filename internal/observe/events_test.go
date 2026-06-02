package observe

import "testing"

func TestHubFiltersBySessionAndDoesNotBlock(t *testing.T) {
	hub := NewHub()
	id, ch := hub.Subscribe("web:one")
	defer hub.Unsubscribe(id)
	hub.Publish(Event{Type: "runtime_started", SessionKey: "web:two"})
	select {
	case event := <-ch:
		t.Fatalf("unexpected event %#v", event)
	default:
	}
	hub.Publish(Event{Type: "runtime_started", SessionKey: "web:one"})
	select {
	case event := <-ch:
		if event.Type != "runtime_started" || event.SessionKey != "web:one" {
			t.Fatalf("unexpected event %#v", event)
		}
	default:
		t.Fatal("expected session event")
	}
}

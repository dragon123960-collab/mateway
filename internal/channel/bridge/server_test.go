package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
)

func TestServerAcceptsEventAndQueuesReply(t *testing.T) {
	done := make(chan struct{})
	server := NewServer(config.BridgeConfig{Token: "secret"}, func(ctx context.Context, event Event) (Reply, error) {
		close(done)
		return OutboundToReply(event, channel.OutboundMessage{Channel: event.Channel, ThreadID: event.ThreadID, Text: "ok", Style: "completed"}), nil
	})
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	body, _ := json.Marshal(Event{ID: "m1", Channel: "demo", PeerID: "p1", Text: "hello"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/channels/demo/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("receiver was not called")
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/channels/demo/replies", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Replies []Reply `json:"replies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Replies) != 1 || payload.Replies[0].Text != "ok" {
		t.Fatalf("replies = %#v", payload.Replies)
	}
}

func TestServerRejectsMissingToken(t *testing.T) {
	server := NewServer(config.BridgeConfig{Token: "secret"}, nil)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	body, _ := json.Marshal(Event{ID: "m1", Channel: "demo", PeerID: "p1", Text: "hello"})
	resp, err := http.Post(ts.URL+"/channels/demo/events", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

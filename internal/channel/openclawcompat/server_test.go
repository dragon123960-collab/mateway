package openclawcompat

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

func TestSendMessageEnqueuesTextReplyWithContextToken(t *testing.T) {
	received := make(chan channel.InboundMessage, 1)
	server := NewServer(config.OpenClawCompatConfig{Token: "secret", LongPollTimeoutMS: 10}, func(ctx context.Context, msg channel.InboundMessage) (channel.OutboundMessage, error) {
		received <- msg
		return channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: "pong", Style: "completed"}, nil
	})
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	body, _ := json.Marshal(SendMessageRequest{Msg: WeixinMessage{
		MessageID:    42,
		FromUserID:   "user",
		ToUserID:     "bot",
		SessionID:    "s1",
		ContextToken: "ctx",
		ItemList:     []MessageItem{{Type: 1, TextItem: &TextItem{Text: "ping"}}},
	}})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/sendmessage", bytes.NewReader(body))
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	select {
	case msg := <-received:
		if msg.Text != "ping" || msg.Metadata["context_token"] != "ctx" || msg.SessionKey != "openclaw-weixin:bot:user" {
			t.Fatalf("inbound = %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("receiver was not called")
	}
	time.Sleep(20 * time.Millisecond)

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/getupdates", bytes.NewReader([]byte(`{"get_updates_buf":""}`)))
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Ret           int             `json:"ret"`
		Msgs          []WeixinMessage `json:"msgs"`
		GetUpdatesBuf string          `json:"get_updates_buf"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Ret != 0 || len(payload.Msgs) != 1 {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Msgs[0].ContextToken != "ctx" || payload.Msgs[0].ItemList[0].TextItem.Text != "pong" {
		t.Fatalf("reply = %#v", payload.Msgs[0])
	}
	if payload.GetUpdatesBuf == "" {
		t.Fatal("expected cursor")
	}
}

func TestSendMessageRejectsUnsupportedItems(t *testing.T) {
	server := NewServer(config.OpenClawCompatConfig{}, func(ctx context.Context, msg channel.InboundMessage) (channel.OutboundMessage, error) {
		t.Fatal("receiver should not be called")
		return channel.OutboundMessage{}, nil
	})
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	body, _ := json.Marshal(SendMessageRequest{Msg: WeixinMessage{ItemList: []MessageItem{{Type: 2}}}})
	resp, err := http.Post(ts.URL+"/sendmessage", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Ret int `json:"ret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Ret == 0 {
		t.Fatalf("expected unsupported ret, got %#v", payload)
	}
}

func TestGetConfigAndSendTypingAreNoops(t *testing.T) {
	server := NewServer(config.OpenClawCompatConfig{}, nil)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	for _, path := range []string{"/getconfig", "/sendtyping"} {
		resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Ret int `json:"ret"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if payload.Ret != 0 {
			t.Fatalf("%s payload = %#v", path, payload)
		}
	}
}

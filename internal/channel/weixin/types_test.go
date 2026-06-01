package weixin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/channel"
)

func TestMessageToInboundText(t *testing.T) {
	msg, ok := Message{
		MessageID:    42,
		FromUserID:   "wxid_user",
		ToUserID:     "bot_account",
		SessionID:    "session",
		ContextToken: "ctx",
		MessageType:  1,
		MessageState: 2,
		ItemList:     []Item{{Type: 1, TextItem: &TextItem{Text: " hello "}}},
	}.ToInbound("acct")
	if !ok {
		t.Fatal("expected text message")
	}
	if msg.ID != "42" || msg.Channel != "weixin" || msg.Text != "hello" {
		t.Fatalf("unexpected inbound: %#v", msg)
	}
	if msg.SessionKey != "weixin:acct:wxid_user" {
		t.Fatalf("SessionKey = %q", msg.SessionKey)
	}
	if msg.Metadata["context_token"] != "ctx" {
		t.Fatalf("metadata = %#v", msg.Metadata)
	}
}

func TestMessageToInboundRejectsNonText(t *testing.T) {
	_, ok := Message{MessageID: 42, ItemList: []Item{{Type: 2}}}.ToInbound("acct")
	if ok {
		t.Fatal("expected non-text message rejected")
	}
}

func TestReplyToMessagePreservesContextToken(t *testing.T) {
	reply := ReplyToMessage(
		mustInbound(t, `{"Metadata":{"peer_id":"wxid_user","context_token":"ctx"}}`),
		channelReply("pong"),
	)
	if reply.ToUserID != "wxid_user" || reply.ContextToken != "ctx" {
		t.Fatalf("reply = %#v", reply)
	}
	if len(reply.ItemList) != 1 || reply.ItemList[0].TextItem.Text != "pong" {
		t.Fatalf("items = %#v", reply.ItemList)
	}
}

func TestClientAddsILinkHeadersAndBaseInfo(t *testing.T) {
	var gotHeaders http.Header
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ret":0,"msgs":[]}`))
	}))
	defer server.Close()
	_, err := (Client{BaseURL: server.URL, Token: "tok"}).GetUpdates(t.Context(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if gotHeaders.Get("Authorization") != "Bearer tok" || gotHeaders.Get("AuthorizationType") != "ilink_bot_token" {
		t.Fatalf("headers = %#v", gotHeaders)
	}
	if gotHeaders.Get("iLink-App-Id") != "bot" || gotHeaders.Get("iLink-App-ClientVersion") == "" {
		t.Fatalf("iLink headers = %#v", gotHeaders)
	}
	if _, ok := gotPayload["base_info"].(map[string]any); !ok {
		t.Fatalf("payload missing base_info: %#v", gotPayload)
	}
}

func TestSaveAndLoadAccount(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accounts")
	if err := SaveAccount(dir, Account{AccountID: "acct", Token: "tok", BaseURL: "https://base"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "acct.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	account, err := LoadAccount(dir, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if account.Token != "tok" || account.BaseURL != "https://base" {
		t.Fatalf("account = %#v", account)
	}
}

func TestMapStringTreatsMissingAndNilAsEmpty(t *testing.T) {
	values := map[string]any{"nil": nil, "ok": " value "}
	if got := mapString(values, "missing"); got != "" {
		t.Fatalf("missing = %q", got)
	}
	if got := mapString(values, "nil"); got != "" {
		t.Fatalf("nil = %q", got)
	}
	if got := mapString(values, "ok"); got != "value" {
		t.Fatalf("ok = %q", got)
	}
}

func TestIntFromAnyHandlesILinkErrcodes(t *testing.T) {
	if got := intFromAny(float64(-22)); got != -22 {
		t.Fatalf("float errcode = %d", got)
	}
	if got := intFromAny("-13"); got != -13 {
		t.Fatalf("string errcode = %d", got)
	}
	if got := intFromAny(nil); got != 0 {
		t.Fatalf("nil errcode = %d", got)
	}
}

func mustInbound(t *testing.T, raw string) channel.InboundMessage {
	t.Helper()
	var msg channel.InboundMessage
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func channelReply(text string) channel.OutboundMessage {
	return channel.OutboundMessage{Text: text}
}

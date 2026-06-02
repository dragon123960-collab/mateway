package weixin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestMessageToInboundImageOnly(t *testing.T) {
	msg, ok := Message{
		MessageID:  42,
		FromUserID: "wxid_user",
		ToUserID:   "bot_account",
		ItemList: []Item{{Type: 2, ImageItem: &MediaItem{
			URL:      "https://example.test/image.png",
			MimeType: "image/png",
			Name:     "image.png",
			Size:     123,
		}}},
	}.ToInbound("acct")
	if !ok {
		t.Fatal("expected image message")
	}
	if msg.Text != "" || len(msg.Parts) != 1 || msg.Parts[0].Type != channel.PartImage || msg.Parts[0].URI != "https://example.test/image.png" {
		t.Fatalf("unexpected inbound image: %#v", msg)
	}
}

func TestMessageToInboundTextAndImage(t *testing.T) {
	msg, ok := Message{
		MessageID:  42,
		FromUserID: "wxid_user",
		ToUserID:   "bot_account",
		ItemList: []Item{
			{Type: 1, TextItem: &TextItem{Text: "看图"}},
			{Type: 2, ImageItem: &MediaItem{Path: "/tmp/image.png", MimeType: "image/png"}},
		},
	}.ToInbound("acct")
	if !ok {
		t.Fatal("expected mixed message")
	}
	if msg.Text != "看图" || len(msg.Parts) != 1 || msg.Parts[0].URI != "file:///tmp/image.png" {
		t.Fatalf("unexpected mixed inbound: %#v", msg)
	}
}

func TestMessageToInboundRejectsUnsupportedNonText(t *testing.T) {
	_, ok := Message{MessageID: 42, ItemList: []Item{{Type: 9}}}.ToInbound("acct")
	if ok {
		t.Fatal("expected unsupported message rejected")
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
	if reply.ClientID == "" || reply.MessageType != 2 || reply.MessageState != 2 {
		t.Fatalf("reply metadata = %#v", reply)
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

func TestLoadLatestAccount(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accounts")
	if err := SaveAccount(dir, Account{AccountID: "old", Token: "old-token", BaseURL: "https://old"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := SaveAccount(dir, Account{AccountID: "new", Token: "new-token", BaseURL: "https://new"}); err != nil {
		t.Fatal(err)
	}
	account, err := LoadLatestAccount(dir)
	if err != nil {
		t.Fatal(err)
	}
	if account.AccountID != "new" {
		t.Fatalf("account = %#v", account)
	}
}

func TestEnableConfigWritesAccountButNotToken(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "config", "channels")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "weixin.yaml")
	if err := os.WriteFile(path, []byte("weixin:\n  enabled: false\n  token: \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnableConfig(home, Account{AccountID: "acct", Token: "secret-token", BaseURL: "https://base"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "enabled: true") || !strings.Contains(text, "account_id: acct") || !strings.Contains(text, "base_url: https://base") {
		t.Fatalf("config = %s", text)
	}
	if strings.Contains(text, "secret-token") {
		t.Fatalf("token leaked into config: %s", text)
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

func TestLoginStartResponseParsesQRCodeImgContent(t *testing.T) {
	var resp LoginStartResponse
	if err := json.Unmarshal([]byte(`{"qrcode":"token","qrcode_img_content":"weixin://scan-url"}`), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.QRCode != "token" || resp.QRCodeImgContent != "weixin://scan-url" {
		t.Fatalf("response = %#v", resp)
	}
	if got := firstNonEmpty(resp.QRCodeImgContent, resp.QRCodeURL, resp.QRCode); got != "weixin://scan-url" {
		t.Fatalf("qr scan data = %q", got)
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

package tools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserProvider(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Hello</h1><p>Browser fetch summary test.</p></body></html>`))
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	defer server.Close()

	provider := BrowserProvider{Enabled: true, HTTPClient: server.Client()}
	toolList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := toolList[0].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]string{"url": server.URL})})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("expected summarized browser content")
	}
}

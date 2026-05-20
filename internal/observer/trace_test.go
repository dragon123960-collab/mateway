package observer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShowTraceFiltersByTraceID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events-2026-05-19.jsonl")
	data := strings.Join([]string{
		`{"event":"runtime.receive","trace_id":"keep","ts":"2026-05-19T10:00:00+08:00","session_key":"cli:cli","text":"现在几点"}`,
		`{"event":"runtime.receive","trace_id":"skip","ts":"2026-05-19T10:00:01+08:00","session_key":"cli:cli","text":"忽略"}`,
		`{"event":"runtime.reply","trace_id":"keep","ts":"2026-05-19T10:00:02+08:00","failed":false,"reply_chars":12}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ShowTrace(dir, "keep", TraceShowOptions{}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "runtime.receive") || !strings.Contains(text, "runtime.reply") {
		t.Fatalf("expected matching events, got %q", text)
	}
	if strings.Contains(text, "忽略") || strings.Contains(text, "trace=skip") {
		t.Fatalf("unexpected non-matching event in %q", text)
	}
}

func TestTailTraceNoFollowShowsLastLines(t *testing.T) {
	dir := t.TempDir()
	path := todayTracePath(dir)
	data := strings.Join([]string{
		`{"event":"runtime.receive","trace_id":"a","ts":"2026-05-19T10:00:00+08:00","text":"first"}`,
		`{"event":"runtime.receive","trace_id":"b","ts":"2026-05-19T10:00:01+08:00","text":"second"}`,
		`{"event":"runtime.receive","trace_id":"c","ts":"2026-05-19T10:00:02+08:00","text":"third"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := TailTrace(context.Background(), dir, TraceTailOptions{Lines: 2, Follow: false}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "trace=a") {
		t.Fatalf("expected only last two lines, got %q", text)
	}
	if !strings.Contains(text, "trace=b") || !strings.Contains(text, "trace=c") {
		t.Fatalf("missing last lines in %q", text)
	}
}

package observer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type TraceTailOptions struct {
	Lines  int
	Follow bool
	Raw    bool
}

type TraceShowOptions struct {
	Raw bool
}

func TailTrace(ctx context.Context, traceDir string, opts TraceTailOptions, out io.Writer) error {
	if opts.Lines < 0 {
		opts.Lines = 0
	}
	path := todayTracePath(traceDir)
	var offset int64
	if err := printExistingTail(path, opts, out, &offset); err != nil && !os.IsNotExist(err) {
		return err
	}
	if !opts.Follow {
		return nil
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			nextPath := todayTracePath(traceDir)
			if nextPath != path {
				path = nextPath
				offset = 0
			}
			nextOffset, err := printNewLines(path, offset, opts.Raw, out)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			offset = nextOffset
		}
	}
}

func ShowTrace(traceDir, traceID string, opts TraceShowOptions, out io.Writer) error {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return fmt.Errorf("trace_id is required")
	}
	paths, err := traceFiles(traceDir)
	if err != nil {
		return err
	}
	found := 0
	for _, path := range paths {
		if err := scanTraceFile(path, func(raw string, ev map[string]any) error {
			if stringField(ev, "trace_id") != traceID {
				return nil
			}
			found++
			printTraceLine(raw, ev, opts.Raw, out)
			return nil
		}); err != nil {
			return err
		}
	}
	if found == 0 {
		return fmt.Errorf("trace_id %q not found under %s", traceID, traceDir)
	}
	return nil
}

func todayTracePath(traceDir string) string {
	return filepath.Join(traceDir, "events-"+time.Now().Format("2006-01-02")+".jsonl")
}

func printExistingTail(path string, opts TraceTailOptions, out io.Writer, offset *int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	*offset = info.Size()
	lines, err := readAllLines(file)
	if err != nil {
		return err
	}
	if opts.Lines > 0 && len(lines) > opts.Lines {
		lines = lines[len(lines)-opts.Lines:]
	}
	for _, line := range lines {
		printTraceRawLine(line, opts.Raw, out)
	}
	return nil
}

func printNewLines(path string, offset int64, raw bool, out io.Writer) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return offset, err
	}
	if info.Size() < offset {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		printTraceRawLine(scanner.Text(), raw, out)
	}
	if err := scanner.Err(); err != nil {
		return offset, err
	}
	nextOffset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return offset, err
	}
	return nextOffset, nil
}

func readAllLines(r io.Reader) ([]string, error) {
	var lines []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func traceFiles(traceDir string) ([]string, error) {
	entries, err := os.ReadDir(traceDir)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("trace dir does not exist: %s", traceDir)
	}
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "events-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(traceDir, name))
	}
	sort.Strings(paths)
	return paths, nil
}

func scanTraceFile(path string, fn func(raw string, ev map[string]any) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		raw := scanner.Text()
		ev, err := parseTraceLine(raw)
		if err != nil {
			continue
		}
		if err := fn(raw, ev); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func printTraceRawLine(line string, raw bool, out io.Writer) {
	ev, err := parseTraceLine(line)
	if err != nil {
		fmt.Fprintln(out, line)
		return
	}
	printTraceLine(line, ev, raw, out)
}

func parseTraceLine(line string) (map[string]any, error) {
	var ev map[string]any
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil, err
	}
	return ev, nil
}

func printTraceLine(raw string, ev map[string]any, rawMode bool, out io.Writer) {
	if rawMode {
		fmt.Fprintln(out, raw)
		return
	}
	ts := timeOnly(stringField(ev, "ts"))
	event := stringField(ev, "event")
	traceID := stringField(ev, "trace_id")
	parts := []string{ts, event}
	if traceID != "" {
		parts = append(parts, "trace="+traceID)
	}
	switch event {
	case "runtime.receive":
		parts = append(parts, compactKV("session", stringField(ev, "session_key")))
		parts = append(parts, compactKV("text", shorten(stringField(ev, "text"), 120)))
		parts = append(parts, compactKV("resolved", shorten(stringField(ev, "resolved_query"), 120)))
	case "runtime.task_binding_started":
		parts = append(parts, compactKV("session", stringField(ev, "session_key")))
		parts = append(parts, compactKV("active_task", stringField(ev, "active_task")))
	case "runtime.followup_resolved":
		parts = append(parts, compactKV("kind", stringField(ev, "kind")))
		parts = append(parts, compactKV("target", stringField(ev, "target_task_id")))
		parts = append(parts, compactKV("source", stringField(ev, "source_task_id")))
		parts = append(parts, compactKV("resolved", shorten(stringField(ev, "resolved_query"), 140)))
		parts = append(parts, compactKV("reason", shorten(stringField(ev, "reason"), 100)))
	case "runtime.task_activated":
		parts = append(parts, compactKV("task", stringField(ev, "task_id")))
		parts = append(parts, compactKV("kind", stringField(ev, "kind")))
		parts = append(parts, compactKV("status", stringField(ev, "task_status")))
	case "runtime.task_continuation_created":
		parts = append(parts, compactKV("source", stringField(ev, "source_task_id")))
		parts = append(parts, compactKV("target", stringField(ev, "target_task_id")))
	case "runtime.task_pending_input":
		parts = append(parts, compactKV("task", stringField(ev, "task_id")))
		parts = append(parts, compactKV("fields", shorten(jsonCompact(ev["fields"]), 160)))
	case "runtime.task_pending_approval":
		parts = append(parts, compactKV("task", stringField(ev, "task_id")))
	case "runtime.artifact_lookup":
		parts = append(parts, compactKV("matched", fmt.Sprint(ev["matched"])))
		parts = append(parts, compactKV("task", stringField(ev, "top_task_id")))
		parts = append(parts, compactKV("kind", stringField(ev, "top_kind")))
		parts = append(parts, compactKV("path", shorten(stringField(ev, "top_path"), 100)))
		parts = append(parts, compactKV("url", shorten(stringField(ev, "top_source_url"), 100)))
		parts = append(parts, compactKV("score", numberString(ev["top_score"])))
	case "runtime.session_loaded":
		parts = append(parts, compactKV("session", stringField(ev, "session_key")))
		parts = append(parts, compactKV("exists", fmt.Sprint(ev["exists"])))
		parts = append(parts, compactKV("turns", numberString(ev["turn_count"])))
		parts = append(parts, compactKV("last_task", stringField(ev, "last_task_id")))
		parts = append(parts, compactKV("last_status", stringField(ev, "last_status")))
	case "runtime.session_saved":
		parts = append(parts, compactKV("session", stringField(ev, "session_key")))
		parts = append(parts, compactKV("turns", numberString(ev["turn_count"])))
		parts = append(parts, compactKV("task", stringField(ev, "task_id")))
		parts = append(parts, compactKV("status", stringField(ev, "task_status")))
		parts = append(parts, compactKV("results", numberString(ev["result_count"])))
	case "runtime.skills_selected":
		parts = append(parts, compactKV("stage", stringField(ev, "stage")))
		parts = append(parts, compactKV("skills", shorten(jsonCompact(ev["skills"]), 220)))
	case "runtime.plan", "runtime.plan_repair":
		parts = append(parts, compactKV("summary", shorten(stringField(ev, "summary"), 120)))
		parts = append(parts, compactKV("tools", joinStringArray(ev["tool_names"])))
	case "runtime.tool_start":
		parts = append(parts, compactKV("step", stringField(ev, "step_id")))
		parts = append(parts, compactKV("tool", stringField(ev, "tool")))
		parts = append(parts, compactKV("goal", shorten(stringField(ev, "goal"), 100)))
	case "runtime.tool_done":
		parts = append(parts, compactKV("step", stringField(ev, "step_id")))
		parts = append(parts, compactKV("tool", stringField(ev, "tool")))
		parts = append(parts, compactKV("ok", fmt.Sprint(ev["ok"])))
		parts = append(parts, compactKV("error", stringField(ev, "error")))
		parts = append(parts, compactKV("control", stringField(ev, "control")))
	case "runtime.reply":
		parts = append(parts, compactKV("failed", fmt.Sprint(ev["failed"])))
		parts = append(parts, compactKV("reply_chars", numberString(ev["reply_chars"])))
	case "runtime.failed", "runtime.synthesize_failed", "runtime.plan_repair_failed", "runtime.session_load_failed", "runtime.session_save_failed":
		parts = append(parts, compactKV("error", shorten(firstNonEmpty(stringField(ev, "error"), stringField(ev, "reason")), 160)))
	case "runtime.control":
		parts = append(parts, compactKV("control", stringField(ev, "control")))
		parts = append(parts, compactKV("style", stringField(ev, "style")))
	default:
		parts = append(parts, compactKV("message", shorten(firstNonEmpty(stringField(ev, "text"), stringField(ev, "summary"), stringField(ev, "error")), 160)))
	}
	fmt.Fprintln(out, strings.Join(nonEmpty(parts), " | "))
}

func jsonCompact(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func timeOnly(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return firstNonEmpty(ts, "--:--:--")
	}
	return t.Format("15:04:05")
}

func stringField(ev map[string]any, key string) string {
	value, ok := ev[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func joinStringArray(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fmt.Sprint(item))
	}
	return strings.Join(out, ",")
}

func compactKV(key, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "<nil>" {
		return ""
	}
	value = strings.ReplaceAll(value, "\n", `\n`)
	return key + "=" + value
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, item := range in {
		if strings.TrimSpace(item) != "" {
			out = append(out, item)
		}
	}
	return out
}

func shorten(text string, limit int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func numberString(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Sprint(v)
		}
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprint(v)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

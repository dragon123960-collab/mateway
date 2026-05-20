package observer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Logger struct {
	Quiet    bool
	TraceDir string
}

func (l Logger) Event(event string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"] = event
	fields["ts"] = time.Now().Format(time.RFC3339)
	b, _ := json.Marshal(fields)
	if !l.Quiet {
		fmt.Println(string(b))
	}
	if l.TraceDir != "" {
		_ = os.MkdirAll(l.TraceDir, 0o755)
		path := filepath.Join(l.TraceDir, "events-"+time.Now().Format("2006-01-02")+".jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.Write(append(b, '\n'))
	}
}

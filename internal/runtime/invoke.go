package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/reflection"
	"github.com/dongping/mateway/internal/skills"
)

type Result struct {
	Status   string
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

type Invoker struct {
	Workspace string
}

func (i Invoker) Invoke(ctx context.Context, skill skills.Skill) (Result, error) {
	start := time.Now()
	var (
		res Result
		err error
	)
	switch skill.Manifest.Type {
	case skills.TypeCLI:
		if strings.TrimSpace(skill.Manifest.Entry) == "" {
			err = fmt.Errorf("skill %q is not executable: missing cli entry in _meta.json", skill.Manifest.Name)
			break
		}
		res, err = i.runCLI(ctx, skill)
	case skills.TypeAPI:
		if strings.TrimSpace(skill.Manifest.URL) == "" {
			err = fmt.Errorf("skill %q is not executable: missing api url in _meta.json", skill.Manifest.Name)
			break
		}
		res, err = i.runAPI(ctx, skill)
	default:
		err = fmt.Errorf("skill %q is not executable by the runtime", skill.Manifest.Name)
	}
	res.Duration = time.Since(start)
	status := "success"
	if err != nil {
		status = "failed"
	}
	_ = reflection.Append(i.Workspace, reflection.Record{
		CreatedAt: time.Now().Format(time.RFC3339),
		SkillName: skill.Manifest.Name,
		Type:      string(skill.Manifest.Type),
		Status:    status,
		Duration:  res.Duration.String(),
		ExitCode:  res.ExitCode,
		RiskLevel: skill.Manifest.RiskLevel,
		Stdout:    truncate(res.Stdout),
		Stderr:    truncate(res.Stderr),
		Metadata: map[string]any{
			"directory": skill.Directory,
		},
	})
	return res, err
}

func (i Invoker) runCLI(ctx context.Context, skill skills.Skill) (Result, error) {
	entry := skill.Manifest.Entry
	if !filepath.IsAbs(entry) {
		entry = filepath.Join(skill.Directory, entry)
	}
	var cmd *exec.Cmd
	ext := strings.ToLower(filepath.Ext(entry))
	switch {
	case ext == ".py":
		cmd = exec.CommandContext(ctx, "python3", entry)
	case ext == ".js":
		cmd = exec.CommandContext(ctx, "node", entry)
	case ext == ".cmd" || ext == ".bat":
		cmd = exec.CommandContext(ctx, "cmd", "/C", entry)
	case ext == ".ps1":
		cmd = exec.CommandContext(ctx, "powershell", "-File", entry)
	case runtime.GOOS == "windows":
		cmd = exec.CommandContext(ctx, "cmd", "/C", entry)
	default:
		cmd = exec.CommandContext(ctx, "sh", entry)
	}
	cmd.Dir = skill.Directory
	cmd.Env = append(os.Environ(),
		"MATEWAY_SKILL_NAME="+skill.Manifest.Name,
		"MATEWAY_SKILL_TYPE="+string(skill.Manifest.Type),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(err),
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (i Invoker) runAPI(ctx context.Context, skill skills.Skill) (Result, error) {
	method := skill.Manifest.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, skill.Manifest.URL, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Stdout:   string(body),
		ExitCode: resp.StatusCode,
	}
	if resp.StatusCode >= 400 {
		result.Stderr = string(body)
		return result, fmt.Errorf("api returned %d", resp.StatusCode)
	}
	return result, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func truncate(value string) string {
	if len(value) <= 2048 {
		return value
	}
	return value[:2048]
}

package cmdresolve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Resolution struct {
	Command        string
	Path           string
	Source         string
	SearchPaths    []string
	ShellPath      string
	ShellCandidate string
}

type Snapshot struct {
	ShellPath   string
	SearchPaths []string
	CollectedAt time.Time
	Err         string
}

type Resolver struct {
	shellPath string
	envPath   string
	runShell  func(string, string) (string, error)

	once         sync.Once
	snapshot     Snapshot
	snapshotErr  error
	cacheMu      sync.RWMutex
	commandCache map[string]Resolution
}

var defaultResolver = NewResolver("", "")

func Default() *Resolver {
	return defaultResolver
}

func NewResolver(shellPath, envPath string) *Resolver {
	return &Resolver{
		shellPath:    strings.TrimSpace(shellPath),
		envPath:      envPath,
		runShell:     invokeShell,
		commandCache: map[string]Resolution{},
	}
}

func (r *Resolver) Resolve(command string) (Resolution, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return Resolution{}, fmt.Errorf("command is required")
	}

	if strings.ContainsRune(command, filepath.Separator) {
		path, err := resolveAbsoluteCommand(command)
		if err != nil {
			return Resolution{}, err
		}
		return Resolution{
			Command:     command,
			Path:        path,
			Source:      "absolute_path",
			SearchPaths: r.SearchPaths(),
			ShellPath:   r.shell(),
		}, nil
	}

	r.cacheMu.RLock()
	if cached, ok := r.commandCache[command]; ok {
		r.cacheMu.RUnlock()
		return cached, nil
	}
	r.cacheMu.RUnlock()

	searchPaths := r.SearchPaths()
	if path, ok := findExecutableInPaths(command, searchPaths); ok {
		resolution := Resolution{
			Command:     command,
			Path:        path,
			Source:      pathSource(path, searchPaths),
			SearchPaths: searchPaths,
			ShellPath:   r.shell(),
		}
		r.cache(command, resolution)
		return resolution, nil
	}

	shellPath := r.shell()
	if shellPath != "" {
		shellCandidate, err := r.commandFromShell(command)
		if err == nil && shellCandidate != "" {
			if filepath.IsAbs(shellCandidate) {
				path, statErr := resolveAbsoluteCommand(shellCandidate)
				if statErr == nil {
					resolution := Resolution{
						Command:        command,
						Path:           path,
						Source:         "login_shell_command_v",
						SearchPaths:    appendUnique(searchPaths, filepath.Dir(path)),
						ShellPath:      shellPath,
						ShellCandidate: shellCandidate,
					}
					r.cache(command, resolution)
					return resolution, nil
				}
			}
			return Resolution{}, &ResolveError{
				Command:        command,
				Kind:           "shell_only",
				ShellCandidate: shellCandidate,
				ShellPath:      shellPath,
				SearchPaths:    searchPaths,
			}
		}
	}

	return Resolution{}, &ResolveError{
		Command:     command,
		Kind:        "not_found",
		ShellPath:   shellPath,
		SearchPaths: searchPaths,
	}
}

func (r *Resolver) SearchPaths() []string {
	snapshot, _ := r.Snapshot()
	paths := splitPathList(firstNonEmpty(r.envPath, os.Getenv("PATH")))
	paths = append(paths, snapshot.SearchPaths...)
	paths = append(paths, defaultSearchPaths()...)
	return uniqueCleanPaths(paths)
}

func (r *Resolver) Snapshot() (Snapshot, error) {
	r.once.Do(func() {
		shellPath := r.shell()
		r.snapshot = Snapshot{
			ShellPath:   shellPath,
			SearchPaths: defaultSearchPaths(),
			CollectedAt: time.Now(),
		}
		if shellPath == "" {
			return
		}
		out, err := r.runShell(shellPath, `printf "__MATEWAY_PATH__%s\n" "$PATH"`)
		if err != nil {
			r.snapshotErr = err
			r.snapshot.Err = err.Error()
			return
		}
		for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
			if strings.HasPrefix(line, "__MATEWAY_PATH__") {
				value := strings.TrimPrefix(line, "__MATEWAY_PATH__")
				r.snapshot.SearchPaths = uniqueCleanPaths(append(splitPathList(value), defaultSearchPaths()...))
			}
		}
	})
	return r.snapshot, r.snapshotErr
}

func (r *Resolver) cache(command string, resolution Resolution) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	r.commandCache[command] = resolution
}

func (r *Resolver) shell() string {
	if r.shellPath != "" {
		return r.shellPath
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell != "" {
		return shell
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	return "/bin/sh"
}

func (r *Resolver) commandFromShell(command string) (string, error) {
	shellPath := r.shell()
	if shellPath == "" {
		return "", nil
	}
	script := fmt.Sprintf(`candidate=$(command -v -- %s 2>/dev/null || true); printf "__MATEWAY_COMMAND__%%s\n" "$candidate"`, shellQuote(command))
	out, err := r.runShell(shellPath, script)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "__MATEWAY_COMMAND__") {
			return strings.TrimSpace(strings.TrimPrefix(line, "__MATEWAY_COMMAND__")), nil
		}
	}
	return "", nil
}

func invokeShell(shellPath, script string) (string, error) {
	args := shellInvocationArgs(shellPath, script)
	cmd := exec.CommandContext(context.Background(), shellPath, args...)
	cmd.Env = append(os.Environ(), "TERM=dumb", "PS1=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func shellInvocationArgs(shellPath, script string) []string {
	base := strings.ToLower(filepath.Base(shellPath))
	switch base {
	case "zsh", "bash":
		return []string{"-lic", script}
	default:
		return []string{"-lc", script}
	}
}

func splitPathList(path string) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return strings.Split(path, string(os.PathListSeparator))
}

func defaultSearchPaths() []string {
	home, _ := os.UserHomeDir()
	paths := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/local/sbin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}
	if strings.TrimSpace(home) != "" {
		paths = append(paths,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
		)
	}
	return uniqueCleanPaths(paths)
}

func uniqueCleanPaths(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		value = filepath.Clean(value)
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func findExecutableInPaths(command string, searchPaths []string) (string, bool) {
	for _, dir := range searchPaths {
		candidate := filepath.Join(dir, command)
		if path, err := resolveAbsoluteCommand(candidate); err == nil {
			return path, true
		}
	}
	return "", false
}

func resolveAbsoluteCommand(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	info, err := os.Stat(command)
	if err != nil {
		if os.IsNotExist(err) {
			return "", exec.ErrNotFound
		}
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", &ResolveError{Command: command, Kind: "not_executable"}
	}
	return filepath.Clean(command), nil
}

func pathSource(path string, searchPaths []string) string {
	dir := filepath.Dir(path)
	for _, item := range splitPathList(os.Getenv("PATH")) {
		if filepath.Clean(strings.TrimSpace(item)) == dir {
			return "process_path"
		}
	}
	for _, item := range searchPaths {
		if filepath.Clean(strings.TrimSpace(item)) == dir {
			switch dir {
			case "/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/local/sbin":
				return "well_known_path"
			default:
				return "search_path"
			}
		}
	}
	return "resolved"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func appendUnique(values []string, value string) []string {
	value = filepath.Clean(strings.TrimSpace(value))
	for _, item := range values {
		if filepath.Clean(strings.TrimSpace(item)) == value {
			return values
		}
	}
	return append(values, value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type ResolveError struct {
	Command        string
	Kind           string
	ShellCandidate string
	ShellPath      string
	SearchPaths    []string
}

func (e *ResolveError) Error() string {
	switch e.Kind {
	case "shell_only":
		return fmt.Sprintf("command %q exists in shell %q but is not an executable path (%s)", e.Command, e.ShellPath, e.ShellCandidate)
	case "not_executable":
		return fmt.Sprintf("command %q is not executable", e.Command)
	default:
		return fmt.Sprintf("command %q not found", e.Command)
	}
}

func (e *ResolveError) Is(target error) bool {
	return errors.Is(target, exec.ErrNotFound) && e.Kind == "not_found"
}

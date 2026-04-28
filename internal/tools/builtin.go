package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dongping/mateway/internal/cmdresolve"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/provisioning"
	"github.com/dongping/mateway/internal/scheduler"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skills"
)

type BuiltinProvider struct {
	Workspace             string
	Sessions              *session.Store
	Memory                memory.Store
	Provisioner           provisioning.WorkspaceProvisioner
	EnforceWorkspacePaths bool
	SkillCatalog          *skills.Catalog
	SkillRuns             SkillRunLookup
}

type SkillRunLookup interface {
	SkillAccessForRun(ctx context.Context, runID string) ([]string, []string, bool)
}

func (p BuiltinProvider) Tools(_ context.Context, _ Scope) ([]Tool, error) {
	return []Tool{
		readFileTool{workspace: p.Workspace, enforceWorkspace: p.EnforceWorkspacePaths},
		readSkillResourceTool{catalog: p.SkillCatalog, runLookup: p.SkillRuns},
		writeFileTool{workspace: p.Workspace, enforceWorkspace: p.EnforceWorkspacePaths},
		listFilesTool{workspace: p.Workspace, enforceWorkspace: p.EnforceWorkspacePaths},
		searchTextTool{workspace: p.Workspace, enforceWorkspace: p.EnforceWorkspacePaths},
		searchHistoryTool{sessions: p.Sessions, memory: p.Memory},
		searchScopedMemoryTool{memory: p.Memory},
		readSessionSummaryTool{memory: p.Memory},
		recallLastTaskTool{memory: p.Memory},
		readMemoryTool{memory: p.Memory},
		writeMemoryNoteTool{memory: p.Memory},
		wikiIngestTool{memory: p.Memory},
		wikiQueryTool{memory: p.Memory},
		wikiLintTool{memory: p.Memory},
		execTool{workspace: p.Workspace, enforceWorkspace: p.EnforceWorkspacePaths},
		sandboxExecTool{workspace: p.Workspace},
		createWorkspaceTool{provisioner: p.Provisioner},
		createAgentTool{provisioner: p.Provisioner},
		scheduleCreateTool{workspace: p.Workspace},
		scheduleUpdateTool{workspace: p.Workspace},
		scheduleListTool{workspace: p.Workspace},
		scheduleGetTool{workspace: p.Workspace},
		scheduleRunsTool{workspace: p.Workspace},
		scheduleToggleTool{workspace: p.Workspace, enabled: true},
		scheduleToggleTool{workspace: p.Workspace, enabled: false},
		scheduleRemoveTool{workspace: p.Workspace},
		waitAgentTool{},
		spawnTool{},
	}, nil
}

type readFileTool struct {
	workspace        string
	enforceWorkspace bool
}

func (t readFileTool) Spec() Spec {
	return Spec{Name: "read_file", Description: "Read a file from the workspace.", Kind: KindBuiltin, ReadOnly: true, Tags: []string{"filesystem"}, InputSchema: schemaObject(prop("path", "string", "Path to read"))}
}
func (t readFileTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	path, err := resolvePath(t.workspace, args.Path, t.enforceWorkspace)
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	return rawResult(string(data)), nil
}

type readSkillResourceTool struct {
	catalog   *skills.Catalog
	runLookup SkillRunLookup
}

func (t readSkillResourceTool) Spec() Spec {
	return Spec{
		Name:        "read_skill_resource",
		Description: "Read a text resource from the selected skill's scripts, references, or assets directory.",
		Kind:        KindBuiltin,
		ReadOnly:    true,
		Tags:        []string{"skill", "resource", "filesystem"},
		InputSchema: schemaObject(
			prop("skill_name", "string", "Selected skill name"),
			prop("path", "string", "Relative path such as references/guide.md or scripts/run.sh"),
		),
	}
}

func (t readSkillResourceTool) Invoke(ctx context.Context, call Call) (Result, error) {
	if t.catalog == nil || t.runLookup == nil {
		return Result{}, fmt.Errorf("skill resource access is not configured")
	}
	var args struct {
		SkillName string `json:"skill_name"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	skillName := strings.TrimSpace(args.SkillName)
	resourcePath, err := normalizeSkillResourcePath(args.Path)
	if err != nil {
		return Result{}, err
	}
	selected, visible, ok := t.runLookup.SkillAccessForRun(ctx, call.RunID)
	if !ok {
		return Result{}, fmt.Errorf("run %q not found for skill resource access", call.RunID)
	}
	allowed := selected
	if len(allowed) == 0 {
		allowed = visible
	}
	if len(allowed) > 0 && !containsString(allowed, skillName) {
		return Result{}, fmt.Errorf("skill %q is not activated for this run", skillName)
	}
	skill, ok := findSkillByName(t.catalog.Snapshot(), skillName)
	if !ok {
		return Result{}, fmt.Errorf("skill %q not found", skillName)
	}
	if !isAllowedSkillResourcePath(resourcePath, skill.Resources.AllowedDirs()) {
		return Result{}, fmt.Errorf("skill resource %q is not under declared skill resource directories", resourcePath)
	}
	fullPath := filepath.Join(skill.Directory, filepath.FromSlash(resourcePath))
	if !isWithinWorkspace(fullPath, skill.Directory) {
		return Result{}, fmt.Errorf("skill resource %q escapes skill directory", resourcePath)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return Result{}, err
	}
	if !utf8.Valid(data) {
		return rawResult(fmt.Sprintf("binary skill resource at %s (%d bytes)", fullPath, len(data))), nil
	}
	return rawResult(string(data)), nil
}

type writeFileTool struct {
	workspace        string
	enforceWorkspace bool
}

func (t writeFileTool) Spec() Spec {
	return Spec{Name: "write_file", Description: "Write content to a file under the workspace.", Kind: KindBuiltin, RiskLevel: "medium", Tags: []string{"filesystem"}, InputSchema: schemaObject(prop("path", "string", "Path to write"), prop("content", "string", "File content"))}
}
func (t writeFileTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	path, err := resolvePath(t.workspace, args.Path, t.enforceWorkspace)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return Result{}, err
	}
	return rawResult(path), nil
}

type listFilesTool struct {
	workspace        string
	enforceWorkspace bool
}

func (t listFilesTool) Spec() Spec {
	return Spec{Name: "list_files", Description: "List files under a workspace directory.", Kind: KindBuiltin, ReadOnly: true, Tags: []string{"filesystem"}, InputSchema: schemaObject(prop("path", "string", "Directory path to list"))}
}
func (t listFilesTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(call.Arguments, &args)
	path, err := resolvePath(t.workspace, firstNonEmpty(args.Path, "."), t.enforceWorkspace)
	if err != nil {
		return Result{}, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return Result{}, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	return rawResult(strings.Join(names, "\n")), nil
}

type searchTextTool struct {
	workspace        string
	enforceWorkspace bool
}

func (t searchTextTool) Spec() Spec {
	return Spec{Name: "search_text", Description: "Search text within workspace files.", Kind: KindBuiltin, ReadOnly: true, Tags: []string{"filesystem", "search"}, InputSchema: schemaObject(prop("query", "string", "Text to search for"), prop("path", "string", "Optional root path"))}
}
func (t searchTextTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Query string `json:"query"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	root, err := resolvePath(t.workspace, firstNonEmpty(args.Path, "."), t.enforceWorkspace)
	if err != nil {
		return Result{}, err
	}
	var matches []string
	needle := strings.ToLower(strings.TrimSpace(args.Query))
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(strings.ToLower(line), needle) {
				matches = append(matches, fmt.Sprintf("%s: %s", path, strings.TrimSpace(line)))
				break
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return rawResult(strings.Join(matches, "\n")), nil
}

type searchHistoryTool struct {
	sessions *session.Store
	memory   memory.Store
}

func (t searchHistoryTool) Spec() Spec {
	return Spec{Name: "search_history", Description: "Search session and memory history.", Kind: KindBuiltin, ReadOnly: true, Tags: []string{"history", "memory"}, InputSchema: schemaObject(prop("query", "string", "Text to search for"), prop("kind", "string", "Optional memory kind"))}
}

type searchScopedMemoryTool struct{ memory memory.Store }

func (t searchScopedMemoryTool) Spec() Spec {
	return Spec{Name: "search_scoped_memory", Description: "Search memory by session, thread, task, or agent dimension.", Kind: KindBuiltin, ReadOnly: true, Tags: []string{"memory", "search"}, InputSchema: schemaObject(
		prop("dimension", "string", "One of session, thread, task, agent"),
		prop("scope", "string", "Scope value for the dimension"),
		prop("query", "string", "Optional query text"),
		prop("kind", "string", "Optional memory kind"),
	)}
}
func (t searchScopedMemoryTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Dimension string `json:"dimension"`
		Scope     string `json:"scope"`
		Query     string `json:"query"`
		Kind      string `json:"kind"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	results, err := t.memory.SearchScoped(ctx, memory.ScopeQuery{
		Kind:       args.Kind,
		Query:      args.Query,
		Dimension:  args.Dimension,
		Scope:      args.Scope,
		SessionKey: call.SessionKey,
		ThreadID:   call.ThreadID,
		AgentName:  call.AgentName,
		Limit:      10,
	})
	if err != nil {
		return Result{}, err
	}
	lines := make([]string, 0, len(results))
	for _, item := range results {
		lines = append(lines, item.Content)
	}
	return rawResult(strings.Join(lines, "\n")), nil
}
func (t searchHistoryTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Query string `json:"query"`
		Kind  string `json:"kind"`
	}
	_ = json.Unmarshal(call.Arguments, &args)
	results, err := t.memory.Search(ctx, firstNonEmpty(args.Kind, "sessions"), args.Query, 10)
	if err != nil {
		return Result{}, err
	}
	lines := make([]string, 0, len(results))
	for _, item := range results {
		lines = append(lines, item.Content)
	}
	if len(lines) == 0 && t.sessions != nil && call.SessionKey != "" {
		items, err := t.sessions.LoadRecent(call.SessionKey, 20)
		if err == nil {
			for _, item := range items {
				if strings.Contains(strings.ToLower(item.Content), strings.ToLower(args.Query)) {
					lines = append(lines, item.Role+": "+item.Content)
				}
			}
		}
	}
	return rawResult(strings.Join(lines, "\n")), nil
}

type readMemoryTool struct{ memory memory.Store }

type readSessionSummaryTool struct{ memory memory.Store }

func (t readSessionSummaryTool) Spec() Spec {
	return Spec{Name: "read_session_summary", Description: "Read the rolling summary for the current session.", Kind: KindBuiltin, ReadOnly: true, Tags: []string{"memory", "summary"}, InputSchema: schemaObject()}
}
func (t readSessionSummaryTool) Invoke(ctx context.Context, call Call) (Result, error) {
	note, ok, err := t.memory.ReadSessionSummary(ctx, call.SessionKey)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return rawResult(""), nil
	}
	return rawResult(note.Content), nil
}

type recallLastTaskTool struct{ memory memory.Store }

func (t recallLastTaskTool) Spec() Spec {
	return Spec{Name: "recall_last_task", Description: "Recall the latest top-level task digest for the current session.", Kind: KindBuiltin, ReadOnly: true, Tags: []string{"memory", "summary", "task"}, InputSchema: schemaObject()}
}
func (t recallLastTaskTool) Invoke(ctx context.Context, call Call) (Result, error) {
	note, ok, err := t.memory.ReadSessionSummary(ctx, call.SessionKey)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return rawResult(""), nil
	}
	if digest := strings.TrimSpace(fmt.Sprint(note.Metadata["latest_task_digest"])); digest != "" {
		return rawResult(digest), nil
	}
	if !ok || strings.TrimSpace(note.Content) == "" {
		return rawResult(""), nil
	}
	for _, line := range strings.Split(note.Content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "最新任务:") {
			return rawResult(strings.TrimSpace(strings.TrimPrefix(line, "最新任务:"))), nil
		}
	}
	return rawResult(note.Content), nil
}

func (t readMemoryTool) Spec() Spec {
	return Spec{Name: "read_memory", Description: "Read structured memory notes.", Kind: KindBuiltin, ReadOnly: true, Tags: []string{"memory"}, InputSchema: schemaObject(prop("kind", "string", "Memory kind"), prop("query", "string", "Optional query"))}
}
func (t readMemoryTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Kind  string `json:"kind"`
		Query string `json:"query"`
	}
	_ = json.Unmarshal(call.Arguments, &args)
	results, err := t.memory.Search(ctx, args.Kind, args.Query, 10)
	if err != nil {
		return Result{}, err
	}
	lines := make([]string, 0, len(results))
	for _, item := range results {
		lines = append(lines, item.Content)
	}
	return rawResult(strings.Join(lines, "\n")), nil
}

type writeMemoryNoteTool struct{ memory memory.Store }

func (t writeMemoryNoteTool) Spec() Spec {
	return Spec{Name: "write_memory_note", Description: "Persist a memory note.", Kind: KindBuiltin, Tags: []string{"memory"}, InputSchema: schemaObject(prop("kind", "string", "Memory kind"), prop("scope", "string", "Memory scope"), prop("content", "string", "Memory content"))}
}
func (t writeMemoryNoteTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Kind    string `json:"kind"`
		Scope   string `json:"scope"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	if err := t.memory.Append(ctx, firstNonEmpty(args.Kind, "knowledge"), args.Scope, args.Content, map[string]any{"tool": "write_memory_note"}); err != nil {
		return Result{}, err
	}
	return rawResult("ok"), nil
}

type wikiIngestTool struct{ memory memory.Store }

func (t wikiIngestTool) Spec() Spec {
	return Spec{
		Name:        "wiki_ingest",
		Description: "Create or update a long-term wiki memory page under workspace/memory/wiki.",
		Kind:        KindBuiltin,
		RiskLevel:   "medium",
		Tags:        []string{"memory", "wiki", "knowledge"},
		InputSchema: schemaObject(
			prop("title", "string", "Wiki page title"),
			prop("category", "string", "Category such as entities, concepts, notes, or sources"),
			prop("slug", "string", "Optional stable slug"),
			prop("summary", "string", "Short page summary"),
			prop("content", "string", "Main markdown content"),
			prop("sources", "array", "Optional list of source references"),
		),
	}
}

func (t wikiIngestTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Title    string   `json:"title"`
		Category string   `json:"category"`
		Slug     string   `json:"slug"`
		Summary  string   `json:"summary"`
		Content  string   `json:"content"`
		Sources  []string `json:"sources"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	path, err := t.memory.UpsertWikiPage(ctx, memory.WikiPage{
		Title:    args.Title,
		Category: args.Category,
		Slug:     args.Slug,
		Summary:  args.Summary,
		Content:  args.Content,
		Sources:  args.Sources,
	})
	if err != nil {
		return Result{}, err
	}
	return rawResult(path), nil
}

type wikiQueryTool struct{ memory memory.Store }

func (t wikiQueryTool) Spec() Spec {
	return Spec{
		Name:        "wiki_query",
		Description: "Search the compiled wiki memory pages before falling back to raw memory or web search.",
		Kind:        KindBuiltin,
		ReadOnly:    true,
		Tags:        []string{"memory", "wiki", "search"},
		InputSchema: schemaObject(
			prop("query", "string", "Search query"),
			prop("limit", "integer", "Optional maximum number of pages to return"),
		),
	}
}

func (t wikiQueryTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	pages, err := t.memory.SearchWiki(ctx, args.Query, args.Limit)
	if err != nil {
		return Result{}, err
	}
	lines := make([]string, 0, len(pages))
	for _, page := range pages {
		lines = append(lines, fmt.Sprintf("- [%s] %s/%s: %s", page.Title, page.Category, page.Slug, firstNonEmpty(page.Summary, trimInline(page.Content, 120))))
	}
	return rawResult(strings.Join(lines, "\n")), nil
}

type wikiLintTool struct{ memory memory.Store }

func (t wikiLintTool) Spec() Spec {
	return Spec{
		Name:        "wiki_lint",
		Description: "Lint the compiled wiki memory and report orphan pages or missing summaries.",
		Kind:        KindBuiltin,
		ReadOnly:    true,
		Tags:        []string{"memory", "wiki", "lint"},
		InputSchema: schemaObject(),
	}
}

func (t wikiLintTool) Invoke(ctx context.Context, call Call) (Result, error) {
	report, err := t.memory.LintWiki(ctx)
	if err != nil {
		return Result{}, err
	}
	return rawResult(report), nil
}

type execTool struct {
	workspace        string
	enforceWorkspace bool
}

func (t execTool) Spec() Spec {
	return Spec{Name: "exec", Description: "Run a command inside the workspace.", Kind: KindBuiltin, RiskLevel: "medium", Tags: []string{"exec"}, InputSchema: schemaObject(prop("command", "string", "Shell command"), prop("working_dir", "string", "Optional working directory"))}
}
func (t execTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Command    string `json:"command"`
		WorkingDir string `json:"working_dir"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{}, fmt.Errorf("command is required")
	}
	if isDangerousCommand(args.Command) {
		return Result{}, fmt.Errorf("dangerous command blocked by runtime safety policy")
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", args.Command)
	dir, err := resolvePath(t.workspace, firstNonEmpty(args.WorkingDir, t.workspace), t.enforceWorkspace)
	if err != nil {
		return Result{}, err
	}
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return rawResult(string(out)), err
	}
	return rawResult(string(out)), nil
}

type sandboxExecTool struct {
	workspace string
}

func (t sandboxExecTool) Spec() Spec {
	return Spec{
		Name:        "sandbox_exec",
		Description: "Run a structured command inside an isolated temporary workspace without shell expansion. Use for safer test execution.",
		Kind:        KindBuiltin,
		RiskLevel:   "medium",
		Tags:        []string{"exec", "sandbox", "testing"},
		InputSchema: schemaObject(
			prop("command", "string", "Executable name, such as go, bash, npm, or pytest"),
			prop("args", "array", "Argument array passed directly to the executable"),
			prop("working_dir", "string", "Optional subdirectory inside the sandbox run directory"),
			prop("timeout_seconds", "integer", "Optional timeout in seconds, default 20"),
			prop("allow_network", "boolean", "Allow common network commands like curl or wget"),
		),
	}
}

func (t sandboxExecTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Command        string   `json:"command"`
		Args           []string `json:"args"`
		WorkingDir     string   `json:"working_dir"`
		TimeoutSeconds int      `json:"timeout_seconds"`
		AllowNetwork   bool     `json:"allow_network"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return Result{}, fmt.Errorf("command is required")
	}
	if isDangerousCommand(command + " " + strings.Join(compactArgs(args.Args), " ")) {
		return Result{}, fmt.Errorf("dangerous command blocked by runtime safety policy")
	}
	if disallowsSandboxShell(command, args.Args) {
		return Result{}, fmt.Errorf("sandbox_exec blocks shell string execution; use command + args or run a script file instead")
	}
	if !args.AllowNetwork && usesNetworkCommand(command) {
		return Result{}, fmt.Errorf("sandbox_exec blocks network-oriented commands by default; set allow_network=true if you really need this")
	}

	sandboxRoot := filepath.Join(t.workspace, "tmp", "sandbox", fmt.Sprintf("run_%d", time.Now().UnixNano()))
	if err := os.MkdirAll(sandboxRoot, 0o755); err != nil {
		return Result{}, err
	}
	workDir, err := resolveSandboxPath(sandboxRoot, args.WorkingDir)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return Result{}, err
	}

	timeout := time.Duration(args.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolution, err := cmdresolve.Default().Resolve(command)
	if err != nil {
		if resolveErr, ok := err.(*cmdresolve.ResolveError); ok {
			similar := sandboxSimilarCommands(command, resolveErr)
			payload, _ := json.Marshal(map[string]any{
				"sandbox_dir":       sandboxRoot,
				"working_dir":       workDir,
				"command":           command,
				"args":              compactArgs(args.Args),
				"output":            "",
				"status":            "command_not_found",
				"resolved_command":  "",
				"resolution_source": "",
				"message":           sandboxResolutionMessage(command, resolveErr, similar),
				"suggestions":       sandboxResolutionSuggestions(command, resolveErr, similar),
				"similar_commands":  similar,
				"search_paths":      resolveErr.SearchPaths,
				"shell_path":        resolveErr.ShellPath,
				"timed_out":         false,
			})
			return Result{Output: payload}, nil
		}
		return Result{}, err
	}

	cmd := exec.CommandContext(runCtx, resolution.Path, compactArgs(args.Args)...)
	cmd.Dir = workDir
	cmd.Env = sandboxEnv(sandboxRoot, resolution.SearchPaths)
	out, err := cmd.CombinedOutput()
	payload, _ := json.Marshal(map[string]any{
		"sandbox_dir":       sandboxRoot,
		"working_dir":       workDir,
		"command":           command,
		"resolved_command":  resolution.Path,
		"resolution_source": resolution.Source,
		"args":              compactArgs(args.Args),
		"output":            strings.TrimSpace(string(out)),
		"timed_out":         runCtx.Err() == context.DeadlineExceeded,
	})
	if err != nil {
		return Result{Output: payload}, fmt.Errorf("sandbox_exec failed: %w", err)
	}
	return Result{Output: payload}, nil
}

type createWorkspaceTool struct {
	provisioner provisioning.WorkspaceProvisioner
}

func (t createWorkspaceTool) Spec() Spec {
	return Spec{Name: "create_workspace", Description: "Create a new workspace scaffold.", Kind: KindBuiltin, RiskLevel: "medium", Tags: []string{"workspace"}, InputSchema: schemaObject(prop("name", "string", "Workspace name"))}
}
func (t createWorkspaceTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	if t.provisioner == nil {
		return Result{}, fmt.Errorf("workspace provisioner is not configured")
	}
	path, err := t.provisioner.CreateWorkspace(args.Name)
	if err != nil {
		return Result{}, err
	}
	return rawResult(path), nil
}

type createAgentTool struct {
	provisioner provisioning.WorkspaceProvisioner
}

func (t createAgentTool) Spec() Spec {
	return Spec{Name: "create_agent", Description: "Create a new agent profile in a workspace.", Kind: KindBuiltin, RiskLevel: "medium", Tags: []string{"workspace", "agent"}, InputSchema: schemaObject(prop("workspace", "string", "Workspace path"), prop("name", "string", "Agent name"), prop("description", "string", "Agent description"))}
}
func (t createAgentTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Workspace   string `json:"workspace"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	if t.provisioner == nil {
		return Result{}, fmt.Errorf("workspace provisioner is not configured")
	}
	path, err := t.provisioner.CreateAgent(args.Workspace, args.Name, args.Description)
	if err != nil {
		return Result{}, err
	}
	return rawResult(path), nil
}

type scheduleCreateTool struct{ workspace string }

func (t scheduleCreateTool) Spec() Spec {
	return Spec{
		Name:        "schedule_create",
		Description: "Create or update a recurring schedule job with interval or cron semantics. Defaults to the current session and current agent unless an explicit target is provided.",
		Kind:        KindBuiltin,
		RiskLevel:   "medium",
		Tags:        []string{"schedule", "cron", "automation"},
		InputSchema: schemaObject(
			prop("name", "string", "Schedule name"),
			prop("prompt", "string", "Prompt for chat schedules"),
			prop("mode", "string", "chat or tool"),
			prop("tool_name", "string", "Tool name for tool-mode schedules"),
			prop("arguments", "object", "Tool arguments for tool-mode schedules"),
			prop("kind", "string", "interval or cron"),
			prop("interval_minutes", "integer", "Interval minutes for interval schedules"),
			prop("expr", "string", "Cron expression for cron schedules"),
			prop("tz", "string", "IANA timezone such as Asia/Shanghai"),
			prop("target_session_mode", "string", "current, explicit, or isolated"),
			prop("target_session_key", "string", "Session key when target_session_mode=explicit"),
			prop("target_agent_mode", "string", "current, explicit, or default"),
			prop("target_agent_name", "string", "Agent name when target_agent_mode=explicit"),
		),
	}
}

func (t scheduleCreateTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Name              string         `json:"name"`
		Prompt            string         `json:"prompt"`
		Mode              string         `json:"mode"`
		ToolName          string         `json:"tool_name"`
		Arguments         map[string]any `json:"arguments"`
		Kind              string         `json:"kind"`
		IntervalMinutes   int            `json:"interval_minutes"`
		Expr              string         `json:"expr"`
		TZ                string         `json:"tz"`
		TargetSessionMode string         `json:"target_session_mode"`
		TargetSessionKey  string         `json:"target_session_key"`
		TargetAgentMode   string         `json:"target_agent_mode"`
		TargetAgentName   string         `json:"target_agent_name"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	store := scheduler.Store{Workspace: t.workspace}
	resolvedSession, resolvedAgent, target, err := scheduler.ResolveTarget(strings.TrimSpace(args.Name), call.SessionKey, call.AgentName, scheduler.Target{
		SessionMode: args.TargetSessionMode,
		SessionKey:  args.TargetSessionKey,
		AgentMode:   args.TargetAgentMode,
		AgentName:   args.TargetAgentName,
	})
	if err != nil {
		return Result{}, err
	}
	var (
		job scheduler.Job
	)
	switch strings.ToLower(strings.TrimSpace(args.Kind)) {
	case "", scheduler.ScheduleKindInterval:
		job, err = scheduler.NewIntervalJob(args.Name, resolvedSession, args.Prompt, args.IntervalMinutes)
	case scheduler.ScheduleKindCron:
		job, err = scheduler.NewCronJob(args.Name, resolvedSession, args.Prompt, args.Expr, args.TZ)
	default:
		return Result{}, fmt.Errorf("unsupported schedule kind %q", args.Kind)
	}
	if err != nil {
		return Result{}, err
	}
	job.Target = target
	job.SessionKey = resolvedSession
	job.AgentName = resolvedAgent
	job.Mode = firstNonEmpty(strings.TrimSpace(args.Mode), "chat")
	job.ToolName = strings.TrimSpace(args.ToolName)
	job.Arguments = args.Arguments
	if strings.EqualFold(job.Mode, "tool") {
		if strings.TrimSpace(job.ToolName) == "" {
			return Result{}, fmt.Errorf("tool_name is required when mode=tool")
		}
		job.Prompt = ""
	} else {
		job.Mode = "chat"
		job.ToolName = ""
		job.Arguments = nil
	}
	job, action, err := store.Upsert(job)
	if err != nil {
		return Result{}, err
	}
	return rawResult(fmt.Sprintf("%s schedule %s (%s)", action, job.Name, job.Description())), nil
}

type scheduleUpdateTool struct{ workspace string }

func (t scheduleUpdateTool) Spec() Spec {
	return Spec{
		Name:        "schedule_update",
		Description: "Update an existing recurring schedule job. Supports changing the name, schedule, prompt, mode, and target.",
		Kind:        KindBuiltin,
		RiskLevel:   "medium",
		Tags:        []string{"schedule", "cron", "automation"},
		InputSchema: schemaObject(
			prop("name", "string", "Existing schedule name or ID"),
			prop("new_name", "string", "New schedule name"),
			prop("prompt", "string", "Prompt for chat schedules"),
			prop("mode", "string", "chat or tool"),
			prop("tool_name", "string", "Tool name for tool-mode schedules"),
			prop("arguments", "object", "Tool arguments for tool-mode schedules"),
			prop("kind", "string", "interval or cron"),
			prop("interval_minutes", "integer", "Interval minutes for interval schedules"),
			prop("expr", "string", "Cron expression for cron schedules"),
			prop("tz", "string", "IANA timezone such as Asia/Shanghai"),
			prop("enabled", "boolean", "Whether the schedule is enabled"),
			prop("target_session_mode", "string", "current, explicit, or isolated"),
			prop("target_session_key", "string", "Session key when target_session_mode=explicit"),
			prop("target_agent_mode", "string", "current, explicit, or default"),
			prop("target_agent_name", "string", "Agent name when target_agent_mode=explicit"),
		),
	}
}

func (t scheduleUpdateTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Name              string         `json:"name"`
		NewName           *string        `json:"new_name"`
		Prompt            *string        `json:"prompt"`
		Mode              *string        `json:"mode"`
		ToolName          *string        `json:"tool_name"`
		Arguments         map[string]any `json:"arguments"`
		Kind              *string        `json:"kind"`
		IntervalMinutes   *int           `json:"interval_minutes"`
		Expr              *string        `json:"expr"`
		TZ                *string        `json:"tz"`
		Enabled           *bool          `json:"enabled"`
		TargetSessionMode *string        `json:"target_session_mode"`
		TargetSessionKey  *string        `json:"target_session_key"`
		TargetAgentMode   *string        `json:"target_agent_mode"`
		TargetAgentName   *string        `json:"target_agent_name"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	store := scheduler.Store{Workspace: t.workspace}
	job, ok, err := store.Get(args.Name)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("schedule %q not found", args.Name)
	}

	if args.NewName != nil && strings.TrimSpace(*args.NewName) != "" {
		job.Name = strings.TrimSpace(*args.NewName)
	}
	if args.Prompt != nil {
		job.Prompt = strings.TrimSpace(*args.Prompt)
	}
	if args.Mode != nil && strings.TrimSpace(*args.Mode) != "" {
		job.Mode = strings.TrimSpace(*args.Mode)
	}
	if args.ToolName != nil {
		job.ToolName = strings.TrimSpace(*args.ToolName)
	}
	if args.Arguments != nil {
		job.Arguments = args.Arguments
	}
	if args.Enabled != nil {
		job.Enabled = *args.Enabled
	}
	if args.Kind != nil && strings.TrimSpace(*args.Kind) != "" {
		job.Schedule.Kind = strings.TrimSpace(*args.Kind)
	}
	if args.IntervalMinutes != nil {
		job.Schedule.IntervalMinutes = *args.IntervalMinutes
	}
	if args.Expr != nil {
		job.Schedule.Expr = strings.TrimSpace(*args.Expr)
	}
	if args.TZ != nil {
		job.Schedule.TZ = strings.TrimSpace(*args.TZ)
	}

	target := job.Target
	if args.TargetSessionMode != nil && strings.TrimSpace(*args.TargetSessionMode) != "" {
		target.SessionMode = strings.TrimSpace(*args.TargetSessionMode)
	}
	if args.TargetSessionKey != nil && strings.TrimSpace(*args.TargetSessionKey) != "" {
		target.SessionKey = strings.TrimSpace(*args.TargetSessionKey)
	}
	if args.TargetAgentMode != nil && strings.TrimSpace(*args.TargetAgentMode) != "" {
		target.AgentMode = strings.TrimSpace(*args.TargetAgentMode)
	}
	if args.TargetAgentName != nil && strings.TrimSpace(*args.TargetAgentName) != "" {
		target.AgentName = strings.TrimSpace(*args.TargetAgentName)
	}
	resolvedSession, resolvedAgent, resolvedTarget, err := scheduler.ResolveTarget(job.Name, job.SessionKey, job.AgentName, target)
	if err != nil {
		return Result{}, err
	}
	job.Target = resolvedTarget
	job.SessionKey = resolvedSession
	job.AgentName = resolvedAgent

	if strings.EqualFold(job.Mode, "tool") {
		if strings.TrimSpace(job.ToolName) == "" {
			return Result{}, fmt.Errorf("tool_name is required when mode=tool")
		}
		job.Prompt = ""
	} else {
		job.Mode = "chat"
		job.ToolName = ""
		job.Arguments = nil
	}

	job, action, err := store.Upsert(job)
	if err != nil {
		return Result{}, err
	}
	return rawResult(fmt.Sprintf("%s schedule %s (%s)", action, job.Name, job.Description())), nil
}

type scheduleListTool struct{ workspace string }

func (t scheduleListTool) Spec() Spec {
	return Spec{
		Name:        "schedule_list",
		Description: "List existing recurring schedule jobs.",
		Kind:        KindBuiltin,
		ReadOnly:    true,
		Tags:        []string{"schedule", "cron", "automation"},
		InputSchema: schemaObject(),
	}
}

func (t scheduleListTool) Invoke(_ context.Context, call Call) (Result, error) {
	store := scheduler.Store{Workspace: t.workspace}
	items, err := store.List()
	if err != nil {
		return Result{}, err
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("%s enabled=%t schedule=%s next=%s status=%s",
			item.Name, item.Enabled, item.Description(), item.State.NextRunAt.Format(time.RFC3339), item.LastStatus()))
	}
	return rawResult(strings.Join(lines, "\n")), nil
}

type scheduleGetTool struct{ workspace string }

func (t scheduleGetTool) Spec() Spec {
	return Spec{
		Name:        "schedule_get",
		Description: "Read one recurring schedule job by name.",
		Kind:        KindBuiltin,
		ReadOnly:    true,
		Tags:        []string{"schedule", "cron", "automation"},
		InputSchema: schemaObject(prop("name", "string", "Schedule name")),
	}
}

func (t scheduleGetTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	store := scheduler.Store{Workspace: t.workspace}
	job, ok, err := store.Get(args.Name)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("schedule %q not found", args.Name)
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return Result{}, err
	}
	return rawResult(string(data)), nil
}

type scheduleRunsTool struct{ workspace string }

func (t scheduleRunsTool) Spec() Spec {
	return Spec{
		Name:        "schedule_runs",
		Description: "Read recent run history for one recurring schedule job.",
		Kind:        KindBuiltin,
		ReadOnly:    true,
		Tags:        []string{"schedule", "cron", "automation", "history"},
		InputSchema: schemaObject(
			prop("name", "string", "Schedule name"),
			prop("limit", "integer", "Optional max number of history lines"),
		),
	}
}

func (t scheduleRunsTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	store := scheduler.Store{Workspace: t.workspace}
	lines, err := store.ReadRuns(args.Name, args.Limit)
	if err != nil {
		return Result{}, err
	}
	return rawResult(strings.Join(lines, "\n")), nil
}

type scheduleToggleTool struct {
	workspace string
	enabled   bool
}

func (t scheduleToggleTool) Spec() Spec {
	name := "schedule_disable"
	description := "Disable a recurring schedule job."
	if t.enabled {
		name = "schedule_enable"
		description = "Enable a recurring schedule job."
	}
	return Spec{
		Name:        name,
		Description: description,
		Kind:        KindBuiltin,
		RiskLevel:   "medium",
		Tags:        []string{"schedule", "cron", "automation"},
		InputSchema: schemaObject(prop("name", "string", "Schedule name")),
	}
}

func (t scheduleToggleTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	store := scheduler.Store{Workspace: t.workspace}
	job, err := store.Enable(args.Name, t.enabled)
	if err != nil {
		return Result{}, err
	}
	state := "disabled"
	if job.Enabled {
		state = "enabled"
	}
	return rawResult(fmt.Sprintf("schedule %s %s", job.Name, state)), nil
}

type scheduleRemoveTool struct{ workspace string }

func (t scheduleRemoveTool) Spec() Spec {
	return Spec{
		Name:        "schedule_remove",
		Description: "Remove a recurring schedule job.",
		Kind:        KindBuiltin,
		RiskLevel:   "medium",
		Tags:        []string{"schedule", "cron", "automation"},
		InputSchema: schemaObject(prop("name", "string", "Schedule name")),
	}
}

func (t scheduleRemoveTool) Invoke(_ context.Context, call Call) (Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	store := scheduler.Store{Workspace: t.workspace}
	if err := store.Remove(args.Name); err != nil {
		return Result{}, err
	}
	return rawResult(fmt.Sprintf("schedule %s removed", args.Name)), nil
}

type spawnTool struct{}

func (t spawnTool) Spec() Spec {
	return Spec{Name: "spawn", Description: "Request a subagent spawn. The default runtime returns a placeholder until async subagents are fully wired.", Kind: KindBuiltin, RiskLevel: "medium", Tags: []string{"agent"}, InputSchema: schemaObject(
		prop("agent_name", "string", "Target child agent"),
		prop("user_text", "string", "Task for the child agent"),
		prop("mode", "string", "chat or tool"),
		prop("tool_name", "string", "Tool name when mode=tool"),
		prop("arguments", "object", "Tool arguments"),
		prop("async", "boolean", "Whether to run asynchronously"),
		prop("collaboration_mode", "string", "coordinator or shared"),
	)}
}
func (t spawnTool) Invoke(_ context.Context, _ Call) (Result, error) {
	return rawResult("spawn is reserved for the harness runtime"), nil
}

type waitAgentTool struct{}

func (t waitAgentTool) Spec() Spec {
	return Spec{Name: "wait_agent", Description: "Wait for a spawned agent. The default runtime returns a placeholder.", Kind: KindBuiltin, ReadOnly: true, Tags: []string{"agent"}, InputSchema: schemaObject(prop("run_id", "string", "Child run id"))}
}
func (t waitAgentTool) Invoke(_ context.Context, _ Call) (Result, error) {
	return rawResult("wait_agent is reserved for the harness runtime"), nil
}

func resolvePath(workspace, path string, enforceWorkspace bool) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", fmt.Errorf("workspace is not configured")
	}
	if path == "" {
		path = "."
	}
	if filepath.IsAbs(path) {
		cleaned := filepath.Clean(path)
		if enforceWorkspace && !isWithinWorkspace(cleaned, workspace) {
			return "", fmt.Errorf("path %s is outside workspace", cleaned)
		}
		return cleaned, nil
	}
	if !enforceWorkspace {
		return filepath.Clean(filepath.Join(workspace, path)), nil
	}
	return filepath.Join(workspace, filepath.Clean(path)), nil
}

func rawResult(value string) Result {
	data, _ := json.Marshal(strings.TrimSpace(value))
	return Result{Output: data}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func schemaObject(properties ...map[string]any) json.RawMessage {
	props := map[string]any{}
	for _, property := range properties {
		if property == nil {
			continue
		}
		name, _ := property["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		cloned := map[string]any{}
		for k, v := range property {
			if k == "name" {
				continue
			}
			cloned[k] = v
		}
		props[name] = cloned
	}
	data, _ := json.Marshal(map[string]any{
		"type":       "object",
		"properties": props,
	})
	return data
}

func prop(name, typ, description string) map[string]any {
	return map[string]any{
		"name":        name,
		"type":        typ,
		"description": description,
	}
}

func isWithinWorkspace(path, workspace string) bool {
	candidate := filepath.Clean(path)
	root := filepath.Clean(workspace)
	if candidate == root {
		return true
	}
	prefix := root + string(filepath.Separator)
	return strings.HasPrefix(candidate, prefix)
}

func normalizeSkillResourcePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("skill resource path is required")
	}
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("skill resource path must be relative")
	}
	cleaned := filepath.ToSlash(filepath.Clean(value))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", fmt.Errorf("skill resource path %q is invalid", value)
	}
	return cleaned, nil
}

func isAllowedSkillResourcePath(path string, dirs []string) bool {
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		prefix := dir + "/"
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func findSkillByName(list []skills.Skill, name string) (skills.Skill, bool) {
	name = strings.TrimSpace(name)
	for _, skill := range list {
		if skill.Manifest.Name == name {
			return skill, true
		}
	}
	return skills.Skill{}, false
}

func containsString(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func isDangerousCommand(command string) bool {
	normalized := strings.ToLower(strings.TrimSpace(command))
	dangerousMarkers := []string{
		"rm -rf /",
		"rm -rf ~",
		"rm -rf *",
		"rm -rf .",
		"rm -rf --no-preserve-root /",
		"sudo rm ",
		"mkfs",
		"dd if=",
		"shutdown ",
		"reboot",
	}
	for _, marker := range dangerousMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func trimInline(value string, max int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if max > 0 && len(value) > max {
		return value[:max]
	}
	return value
}

func disallowsSandboxShell(command string, args []string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	if command != "sh" && command != "bash" && command != "zsh" {
		return false
	}
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "-c", "-lc", "-ic":
			return true
		}
	}
	return false
}

func usesNetworkCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "curl", "wget", "ssh", "scp", "sftp", "nc", "netcat", "telnet", "ping", "dig", "host":
		return true
	default:
		return false
	}
}

func resolveSandboxPath(root, subdir string) (string, error) {
	subdir = strings.TrimSpace(subdir)
	if subdir == "" {
		return root, nil
	}
	if filepath.IsAbs(subdir) {
		return "", fmt.Errorf("sandbox working_dir must be relative")
	}
	path := filepath.Clean(filepath.Join(root, subdir))
	if !isWithinWorkspace(path, root) {
		return "", fmt.Errorf("sandbox working_dir escapes sandbox root")
	}
	return path, nil
}

func sandboxEnv(root string, searchPaths []string) []string {
	path := strings.Join(searchPaths, string(os.PathListSeparator))
	return []string{
		"PATH=" + path,
		"HOME=" + root,
		"TMPDIR=" + root,
		"MATEWAY_SANDBOX=1",
	}
}

func sandboxResolutionMessage(command string, resolveErr *cmdresolve.ResolveError, similar []string) string {
	if resolveErr == nil {
		return fmt.Sprintf("executable %q could not be resolved", command)
	}
	if len(similar) > 0 {
		return fmt.Sprintf("executable %q not found in the current runtime environment; found similarly named executable(s): %s", command, strings.Join(similar, ", "))
	}
	switch resolveErr.Kind {
	case "shell_only":
		return fmt.Sprintf("command %q exists only as a shell alias/function in %s, not as a standalone executable", command, firstNonEmpty(resolveErr.ShellPath, "the login shell"))
	default:
		return fmt.Sprintf("executable %q not found in the current runtime environment", command)
	}
}

func sandboxResolutionSuggestions(command string, resolveErr *cmdresolve.ResolveError, similar []string) []string {
	suggestions := []string{
		"try an absolute path such as /opt/homebrew/bin/" + command,
		"check whether the gateway process PATH includes the CLI install directory",
		"register this CLI as an external provider or skill if it should be a stable capability",
	}
	for i := len(similar) - 1; i >= 0; i-- {
		suggestions = append([]string{
			fmt.Sprintf("try the similarly named executable %q", similar[i]),
		}, suggestions...)
	}
	if resolveErr != nil && resolveErr.Kind == "shell_only" {
		suggestions = append([]string{
			"replace shell alias/function usage with the real executable path before handing it to sandbox_exec",
		}, suggestions...)
	}
	return suggestions
}

func sandboxSimilarCommands(command string, resolveErr *cmdresolve.ResolveError) []string {
	if resolveErr == nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, variant := range commandNameVariants(command) {
		if variant == command || variant == "" || seen[variant] {
			continue
		}
		if !commandExistsInSearchPaths(variant, resolveErr.SearchPaths) {
			continue
		}
		seen[variant] = true
		out = append(out, variant)
	}
	sort.Strings(out)
	return out
}

func commandNameVariants(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	variants := []string{
		strings.ReplaceAll(command, "_", "-"),
		strings.ReplaceAll(command, "-", "_"),
		strings.ReplaceAll(strings.ReplaceAll(command, "-", ""), "_", ""),
	}
	if !strings.ContainsAny(command, "-_") {
		for _, suffix := range []string{"cli", "ctl", "cmd"} {
			if strings.HasSuffix(command, suffix) && len(command) > len(suffix) {
				prefix := command[:len(command)-len(suffix)]
				variants = append(variants, prefix+"-"+suffix, prefix+"_"+suffix)
			}
		}
	}
	out := make([]string, 0, len(variants))
	seen := map[string]bool{}
	for _, variant := range variants {
		variant = strings.TrimSpace(variant)
		if variant == "" || variant == command || seen[variant] {
			continue
		}
		seen[variant] = true
		out = append(out, variant)
	}
	return out
}

func commandExistsInSearchPaths(command string, searchPaths []string) bool {
	for _, dir := range searchPaths {
		path := filepath.Join(strings.TrimSpace(dir), command)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

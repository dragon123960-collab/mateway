package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

type discoveredSkill struct {
	Name        string
	Description string
	Stage       string
	Granularity string
	Priority    string
	Path        string
	Scope       string
}

type skillRelevance struct {
	skill     discoveredSkill
	relevance float64
}

func skillScope(path string) string {
	clean := filepath.ToSlash(path)
	if strings.Contains(clean, "/workspace/agents/") && strings.Contains(clean, "/skills/") {
		return "agent"
	}
	if strings.Contains(clean, "/workspace/skills/") {
		return "shared"
	}
	return ""
}

func discoverSkills(cfg *config.Root, limit int) []discoveredSkill {
	return discoverSkillsForAgent(cfg, "main", limit)
}

func discoverSkillsForAgent(cfg *config.Root, agentID string, limit int) []discoveredSkill {
	if cfg == nil {
		return nil
	}
	workspace := strings.TrimSpace(cfg.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(cfg.App.Home, "workspace")
	}
	roots := skillRoots(workspace, agentID)
	var out []discoveredSkill
	seen := map[string]bool{}
	for _, root := range roots {
		for _, skill := range discoverSkillsInRoot(root) {
			key := strings.ToLower(skill.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, skill)
		}
	}
	sortDiscoveredSkills(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func skillRoots(workspace, agentID string) []string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "main"
	}
	return []string{
		filepath.Join(workspace, "agents", agentID, "skills"),
		filepath.Join(workspace, "skills"),
	}
}

func discoverSkillsInRoot(root string) []discoveredSkill {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []discoveredSkill
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name(), "SKILL.md")
		text := readSkillHeader(path)
		if text == "" {
			continue
		}
		skill := parseSkillHeader(text)
		if skill.Name == "" {
			skill.Name = entry.Name()
		}
		skill.Path = path
		skill.Scope = skillScope(path)
		out = append(out, skill)
	}
	return out
}

func readSkillHeader(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines) && i < 80; i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				return strings.Join(lines[:i+1], "\n")
			}
		}
	}
	if len(lines) > 40 {
		lines = lines[:40]
	}
	return strings.Join(lines, "\n")
}

func parseSkillHeader(text string) discoveredSkill {
	var skill discoveredSkill
	lines := strings.Split(text, "\n")
	inFrontMatter := len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if i == 0 && trimmed == "---" {
			continue
		}
		if inFrontMatter && trimmed == "---" {
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			if !inFrontMatter && strings.HasPrefix(trimmed, "# ") && skill.Name == "" {
				skill.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			}
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			skill.Name = value
		case "description":
			skill.Description = value
		case "stage":
			skill.Stage = value
		case "granularity":
			skill.Granularity = value
		case "priority":
			skill.Priority = value
		}
		if !inFrontMatter && i > 20 {
			break
		}
	}
	return skill
}

func skillsPrompt(skills []discoveredSkill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Discovered skills:\n")
	b.WriteString("- Skills provide specialized instructions for matching tasks.\n")
	b.WriteString("- When a task matches a skill description, read the skill file with file.read before relying on its workflow.\n")
	b.WriteString("- Resolve relative paths in a skill file against the skill directory.\n")
	for _, skill := range skills {
		b.WriteString("- ")
		b.WriteString(skill.Name)
		if skill.Stage != "" || skill.Priority != "" {
			b.WriteString(" (")
			var parts []string
			if skill.Stage != "" {
				parts = append(parts, "stage="+skill.Stage)
			}
			if skill.Priority != "" {
				parts = append(parts, "priority="+skill.Priority)
			}
			b.WriteString(strings.Join(parts, ", "))
			b.WriteString(")")
		}
		if skill.Description != "" {
			b.WriteString(": ")
			b.WriteString(skill.Description)
		}
		b.WriteString("\n")
		b.WriteString("  Location: ")
		b.WriteString(skill.Path)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func skillPriority(skill discoveredSkill) int {
	priority, err := strconv.Atoi(strings.TrimSpace(skill.Priority))
	if err != nil {
		return 0
	}
	return priority
}

func sortDiscoveredSkills(skills []discoveredSkill) {
	if len(skills) <= 1 {
		return
	}
	sort.SliceStable(skills, func(i, j int) bool {
		pi := skillPriority(skills[i])
		pj := skillPriority(skills[j])
		if pi != pj {
			return pi > pj
		}
		return skills[i].Name < skills[j].Name
	})
}

func stripSkillFrontMatter(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return text
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return text
}

func truncateString(text string, limit int) string {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "\n..."
}

func isGuidanceSkillStage(stage string) bool {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "planning", "synthesis", "guidance":
		return true
	}
	return false
}

func executionHint(skill discoveredSkill) string {
	stage := strings.ToLower(strings.TrimSpace(skill.Stage))
	if stage == "cli" {
		return "read SKILL.md with file.read, then execute via terminal.run with the CLI/helper described in the skill"
	}
	if isGuidanceSkillStage(stage) {
		return ""
	}
	return "read SKILL.md with file.read, then follow the skill workflow using existing runtime tools"
}

func readSkillBody(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(stripSkillFrontMatter(string(data)))
	if text == "" {
		return ""
	}
	return text
}

func readSelectedSkillBodies(contract session.TaskContract, skills []discoveredSkill, workspace, agentID string) session.TaskContract {
	if len(contract.RequiredSkills) == 0 || len(skills) == 0 {
		return contract
	}
	byName := map[string]discoveredSkill{}
	for _, s := range skills {
		name := strings.ToLower(strings.TrimSpace(s.Name))
		if name != "" {
			byName[name] = s
		}
	}
	roots := skillRoots(workspace, agentID)
	readCount := 0
	for i := range contract.RequiredSkills {
		name := strings.ToLower(strings.TrimSpace(contract.RequiredSkills[i].Name))
		skill, ok := byName[name]
		if !ok {
			continue
		}
		if isGuidanceSkillStage(skill.Stage) {
			continue
		}
		path := skill.Path
		if path == "" || !validSkillReadPath(path, roots) {
			continue
		}
		body := readSkillBody(path)
		if body == "" {
			continue
		}
		contract.RequiredSkills[i].Body = body
		readCount++
	}
	_ = readCount
	return contract
}

func validSkillReadPath(path string, roots []string) bool {
	clean := filepath.ToSlash(path)
	if strings.Contains(clean, "/.mateway/secrets") {
		return false
	}
	if strings.Contains(clean, "/.mateway/sessions") {
		return false
	}
	if strings.Contains(clean, "/.mateway/trace") {
		return false
	}
	if filepath.Ext(path) != ".md" || !strings.HasSuffix(clean, "/SKILL.md") {
		return false
	}
	if !strings.Contains(clean, "/skills/") {
		return false
	}
	// Resolve symlinks to detect escape attempts.
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	realPath = filepath.Clean(realPath)
	// Verify the resolved path is under one of the configured skill roots.
	for _, root := range roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		realRoot = filepath.Clean(realRoot)
		if strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) || realPath == realRoot {
			return true
		}
	}
	return false
}

func extractCodeBlockCommands(body string) []string {
	var commands []string
	lines := strings.Split(body, "\n")
	inBlock := false
	var buf strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inBlock {
				block := strings.TrimSpace(buf.String())
				if block != "" {
					for _, cmdLine := range strings.Split(block, "\n") {
						cmdLine = strings.TrimSpace(cmdLine)
						if isLikelyCommand(cmdLine) {
							commands = append(commands, cmdLine)
						}
					}
				}
				buf.Reset()
				inBlock = false
			} else {
				inBlock = true
			}
			continue
		}
		if inBlock {
			buf.WriteString(line)
			buf.WriteString("\n")
		}
	}
	return commands
}

func isLikelyCommand(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") ||
		strings.HasPrefix(line, ">") || strings.HasPrefix(line, "<!--") {
		return false
	}
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false
	}
	first := parts[0]
	knownCommands := map[string]bool{
		"npm": true, "npx": true, "node": true, "python": true, "python3": true,
		"pip": true, "pip3": true, "go": true, "cargo": true, "rustc": true,
		"git": true, "docker": true, "kubectl": true, "helm": true,
		"curl": true, "wget": true, "ssh": true, "scp": true, "rsync": true,
		"cat": true, "ls": true, "find": true, "grep": true, "rg": true,
		"sed": true, "awk": true, "echo": true, "mkdir": true, "cp": true,
		"mv": true, "rm": true, "chmod": true, "chown": true, "touch": true,
		"make": true, "cmake": true, "bash": true, "sh": true, "zsh": true,
		"lark-cli": true, "lark": true, "feishu": true, "mateway": true,
		"systemctl": true, "launchctl": true, "brew": true,
	}
	if knownCommands[first] {
		return true
	}
	if strings.Contains(line, " --") || strings.Contains(line, " -") {
		return true
	}
	if strings.HasPrefix(first, "./") || strings.HasPrefix(first, "/") {
		return true
	}
	return false
}

type toolHint struct {
	tool  string
	title string
}

// extractProseToolHints scans every prose segment in the skill body for
// keywords that suggest a specific runtime tool, so steps like
// "Write the document" map to file.write instead of terminal.run, and pure
// prose steps without an adjacent code block (e.g. "Save the output") are
// still recognized. Each blank-line-separated section inside a segment is
// classified independently; the first matching tool wins for that section.
func extractProseToolHints(body string) []toolHint {
	var hints []toolHint
	seen := map[string]bool{}

	// Split by ``` so we can iterate over every prose segment (even indices
	// are prose, odd indices are code blocks). Slice 6C: do not limit the
	// scan to "prose before a code block" — pure prose sections between two
	// code blocks, or at the tail of the body, must still be classified.
	parts := strings.Split(body, "```")
	for i := 0; i < len(parts); i += 2 {
		for _, section := range splitProseSections(parts[i]) {
			hint := classifyProseTool(section)
			if hint == nil || seen[hint.tool] {
				continue
			}
			seen[hint.tool] = true
			hints = append(hints, *hint)
		}
	}
	return hints
}

// splitProseSections splits a prose segment into blank-line-separated
// sections so each step description is classified on its own. Leading and
// trailing blanks are ignored, as are heading-only lines.
func splitProseSections(prose string) []string {
	var sections []string
	var current strings.Builder
	flush := func() {
		text := strings.TrimSpace(current.String())
		if text != "" {
			sections = append(sections, text)
		}
		current.Reset()
	}
	for _, line := range strings.Split(prose, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(trimmed)
	}
	flush()
	return sections
}

func classifyProseTool(prose string) *toolHint {
	lines := strings.Split(strings.TrimSpace(prose), "\n")
	var lastLine string
	for j := len(lines) - 1; j >= 0; j-- {
		line := strings.TrimSpace(lines[j])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lastLine = line
		break
	}
	if lastLine == "" {
		return nil
	}

	lower := strings.ToLower(lastLine)

	// file.edit: editing/updating/patching/replacing/appending/inserting into an existing local file
	editWords := []string{"edit", "update", "patch", "replace", "append", "insert", "modify"}
	for _, w := range editWords {
		if strings.Contains(lower, w) {
			return &toolHint{tool: "file.edit", title: truncateString(lastLine, 50)}
		}
	}

	// file.write: creating/generating/saving/outputting a new local artifact
	writeWords := []string{"write", "save", "generate", "create", "output", "produce"}
	for _, w := range writeWords {
		if strings.Contains(lower, w) {
			return &toolHint{tool: "file.write", title: truncateString(lastLine, 50)}
		}
	}

	// file.read: local read steps (excluding mandatory SKILL.md read)
	readWords := []string{"read ", "load ", "open ", "cat "}
	for _, w := range readWords {
		if strings.Contains(lower, w) {
			return &toolHint{tool: "file.read", title: truncateString(lastLine, 50)}
		}
	}

	// web.search: search/look up/current fact retrieval
	searchWords := []string{"search for", "search ", "look up", "find ", "query "}
	for _, w := range searchWords {
		if strings.Contains(lower, w) {
			return &toolHint{tool: "web.search", title: truncateString(lastLine, 50)}
		}
	}

	// web.fetch: fetching/reading URL/page body
	fetchWords := []string{"fetch", "download", "get url", "read url", "open url"}
	for _, w := range fetchWords {
		if strings.Contains(lower, w) {
			return &toolHint{tool: "web.fetch", title: truncateString(lastLine, 50)}
		}
	}

	// If prose contains "run", "execute", "publish", "deploy" - these are terminal.run hints
	// but we let code blocks handle terminal.run primarily, so return nil here
	// to allow code block analysis to take precedence for terminal.run
	return nil
}

func extractSkillBodyPlanItems(body string, skill discoveredSkill, baseID *int) []session.TaskPlanItem {
	if body == "" || baseID == nil {
		return nil
	}
	var items []session.TaskPlanItem
	toolsSeen := map[string]bool{}

	// Part 1: Extract tool hints from prose preceding code blocks.
	// Prose hints take priority — they reflect the step's real tool intent.
	for _, hint := range extractProseToolHints(body) {
		if toolsSeen[hint.tool] {
			continue
		}
		*baseID++
		items = append(items, session.TaskPlanItem{
			ID:       fmt.Sprintf("plan-%d", *baseID),
			Title:    fmt.Sprintf("%s: %s", skill.Name, hint.title),
			Status:   "pending",
			Tool:     hint.tool,
			Criteria: fmt.Sprintf("execute %s step from skill", skill.Name),
		})
		toolsSeen[hint.tool] = true
	}

	// Part 2a: Code blocks that fetch a URL (curl/wget with a URL) map to
	// web.fetch when no web.fetch plan item has been produced yet. This
	// keeps prose-driven mappings authoritative while still capturing URL
	// fetches that only show up in code form.
	if !toolsSeen["web.fetch"] {
		for _, cmd := range extractCodeBlockCommands(body) {
			url, ok := extractURLFromFetchCommand(cmd)
			if !ok {
				continue
			}
			*baseID++
			items = append(items, session.TaskPlanItem{
				ID:       fmt.Sprintf("plan-%d", *baseID),
				Title:    fmt.Sprintf("%s: %s", skill.Name, truncateString("fetch "+url, 60)),
				Status:   "pending",
				Tool:     "web.fetch",
				Criteria: fmt.Sprintf("fetch %s from skill", skill.Name),
			})
			toolsSeen["web.fetch"] = true
			break
		}
	}

	// Part 2b: Other commands from code blocks map to terminal.run if not
	// already present.
	if !toolsSeen["terminal.run"] {
		for _, cmd := range extractCodeBlockCommands(body) {
			if _, isFetch := extractURLFromFetchCommand(cmd); isFetch {
				continue
			}
			*baseID++
			title := fmt.Sprintf("%s: %s", skill.Name, truncateString(cmd, 60))
			items = append(items, session.TaskPlanItem{
				ID:       fmt.Sprintf("plan-%d", *baseID),
				Title:    title,
				Status:   "pending",
				Tool:     "terminal.run",
				Criteria: fmt.Sprintf("run %s step from skill", skill.Name),
			})
			toolsSeen["terminal.run"] = true
			break // Only one terminal.run from code blocks
		}
	}

	// Part 3: Fallback for CLI skills with no extracted items.
	if len(items) == 0 && strings.ToLower(strings.TrimSpace(skill.Stage)) == "cli" {
		*baseID++
		items = append(items, session.TaskPlanItem{
			ID:       fmt.Sprintf("plan-%d", *baseID),
			Title:    fmt.Sprintf("%s CLI workflow", skill.Name),
			Status:   "pending",
			Tool:     "terminal.run",
			Criteria: fmt.Sprintf("execute %s CLI workflow from SKILL.md", skill.Name),
		})
	}

	return items
}

// extractURLFromFetchCommand returns the first http(s) URL it can find in a
// curl/wget command. Returns ("", false) for other commands or when no URL
// is present.
func extractURLFromFetchCommand(cmd string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return "", false
	}
	head := strings.ToLower(fields[0])
	if head != "curl" && head != "wget" {
		return "", false
	}
	for _, tok := range fields[1:] {
		tok = strings.Trim(tok, `"'`)
		lower := strings.ToLower(tok)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			return tok, true
		}
	}
	return "", false
}

func augmentContractWithSkillPlanItems(contract session.TaskContract, skills []discoveredSkill) session.TaskContract {
	if len(contract.RequiredSkills) == 0 {
		return contract
	}
	byName := map[string]discoveredSkill{}
	for _, s := range skills {
		name := strings.ToLower(strings.TrimSpace(s.Name))
		if name != "" {
			byName[name] = s
		}
	}
	baseID := len(contract.PlanItems)
	toolsInContract := planItemToolSet(contract)
	for _, req := range contract.RequiredSkills {
		if req.Body == "" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(req.Name))
		skill, ok := byName[name]
		if !ok || isGuidanceSkillStage(skill.Stage) {
			continue
		}
		items := extractSkillBodyPlanItems(req.Body, skill, &baseID)
		for _, item := range items {
			tool := strings.TrimSpace(item.Tool)
			if tool == "" {
				continue
			}
			if toolsInContract[tool] {
				existing := planItemForTool(&contract, tool, "pending")
				if existing != nil {
					existing.Criteria = item.Criteria
					if !strings.Contains(existing.Title, skill.Name) {
						existing.Title = skill.Name + ": " + existing.Title
					}
				}
				continue
			}
			contract.PlanItems = append(contract.PlanItems, item)
			toolsInContract[tool] = true
		}
	}

	contract.PlanItems = normalizePlanItems(contract.PlanItems)
	return contract
}

func planItemToolSet(contract session.TaskContract) map[string]bool {
	out := map[string]bool{}
	for _, item := range contract.PlanItems {
		tool := strings.TrimSpace(item.Tool)
		if tool != "" {
			out[tool] = true
		}
	}
	return out
}

package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type ProposalInput struct {
	AgentID    string
	Scope      string
	Type       string
	Title      string
	Body       string
	Sources    []string
	Tags       []string
	Confidence string
	CreatedAt  time.Time
}

type ProposalResult struct {
	ID   string
	Path string
}

type LongMemoryInput struct {
	AgentID    string
	Scope      string
	Type       string
	Title      string
	Body       string
	Sources    []string
	Tags       []string
	Confidence string
	CreatedAt  time.Time
}

type LongMemoryResult struct {
	ID   string
	Path string
}

type CommitInput struct {
	AgentID  string
	Proposal string
	Title    string
	At       time.Time
}

type CommitResult struct {
	SourcePath string
	TargetPath string
	Type       string
}

type MemoryItem struct {
	ID      string
	Path    string
	Status  string
	Title   string
	Kind    string
	Tags    []string
	Sources []string
	Scope   string
	Updated string
}

type ListOptions struct {
	AgentID string
	Status  string
	Area    string
	Kind    string
	Tag     string
}

type ShowResult struct {
	ID   string
	Path string
	Text string
}

type RejectInput struct {
	AgentID  string
	Proposal string
	Reason   string
	At       time.Time
}

type RejectResult struct {
	Path string
}

func (s Store) Propose(in ProposalInput) (ProposalResult, error) {
	if strings.TrimSpace(s.Root) == "" {
		return ProposalResult{}, fmt.Errorf("memory root is required")
	}
	if strings.TrimSpace(in.Title) == "" {
		return ProposalResult{}, fmt.Errorf("memory proposal title is required")
	}
	if strings.TrimSpace(in.Body) == "" {
		return ProposalResult{}, fmt.Errorf("memory proposal body is required")
	}
	if secretLike(in.Title) || secretLike(in.Body) || secretLike(strings.Join(in.Sources, "\n")) {
		return ProposalResult{}, fmt.Errorf("memory proposal appears to contain a secret or credential; refusing to write it")
	}
	agentID := firstNonEmptyMemory(in.AgentID, "main")
	scope := normalizeMemoryScope(in.Scope)
	typ := firstNonEmptyMemory(in.Type, "note")
	confidence := normalizeConfidence(in.Confidence)
	now := in.CreatedAt
	if now.IsZero() {
		now = time.Now()
	}
	id := "memory-proposal-" + slugForMemory(in.Title) + "-" + now.Format("20060102-150405")
	inbox := filepath.Join(s.Root, "agents", agentID, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return ProposalResult{}, err
	}
	path := filepath.Join(inbox, id+".md")
	text := renderMemoryProposal(agentID, scope, typ, in.Title, in.Body, in.Sources, in.Tags, confidence, now)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return ProposalResult{}, err
	}
	if err := s.appendLog(now, fmt.Sprintf("propose memory: %s -> %s", in.Title, path)); err != nil {
		return ProposalResult{}, err
	}
	s.rebuildIndexBestEffort(now)
	return ProposalResult{ID: id, Path: path}, nil
}

func (s Store) WriteLong(in LongMemoryInput) (LongMemoryResult, error) {
	if strings.TrimSpace(s.Root) == "" {
		return LongMemoryResult{}, fmt.Errorf("memory root is required")
	}
	if strings.TrimSpace(in.Title) == "" {
		return LongMemoryResult{}, fmt.Errorf("long memory title is required")
	}
	if strings.TrimSpace(in.Body) == "" {
		return LongMemoryResult{}, fmt.Errorf("long memory body is required")
	}
	if secretLike(in.Title) || secretLike(in.Body) || secretLike(strings.Join(in.Sources, "\n")) {
		return LongMemoryResult{}, fmt.Errorf("long memory appears to contain a secret or credential; refusing to write it")
	}
	agentID := firstNonEmptyMemory(in.AgentID, "main")
	scope := normalizeMemoryScope(in.Scope)
	typ := firstNonEmptyMemory(in.Type, "note")
	confidence := normalizeConfidence(in.Confidence)
	now := in.CreatedAt
	if now.IsZero() {
		now = time.Now()
	}
	longDir := filepath.Join(s.Root, "agents", agentID, "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		return LongMemoryResult{}, err
	}
	filename := slugForMemory(in.Title)
	if typ == "playbook" && !strings.HasPrefix(filename, "playbook-") {
		filename = "playbook-" + filename
	}
	path := filepath.Join(longDir, filename+".md")
	if _, err := os.Stat(path); err != nil && !os.IsNotExist(err) {
		return LongMemoryResult{}, err
	}
	text := renderLongMemory(agentID, scope, typ, in.Title, in.Body, in.Sources, in.Tags, confidence, now)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return LongMemoryResult{}, err
	}
	if err := s.updateAgentIndex(agentID, in.Title, path); err != nil {
		return LongMemoryResult{}, err
	}
	if err := s.appendLog(now, fmt.Sprintf("write long memory: %s -> %s", in.Title, path)); err != nil {
		return LongMemoryResult{}, err
	}
	s.rebuildIndexBestEffort(now)
	return LongMemoryResult{ID: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Path: path}, nil
}

func (s Store) List(opts ListOptions) ([]MemoryItem, error) {
	agentID := firstNonEmptyMemory(opts.AgentID, "main")
	area := strings.ToLower(strings.TrimSpace(opts.Area))
	if area == "" {
		area = "inbox"
	}
	var dir string
	switch area {
	case "inbox", "long":
		dir = filepath.Join(s.Root, "agents", agentID, area)
	default:
		return nil, fmt.Errorf("unsupported memory area %q", opts.Area)
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []MemoryItem
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		item := memoryItemFromText(path, string(data))
		if status := strings.TrimSpace(opts.Status); status != "" && !strings.EqualFold(item.Status, status) {
			continue
		}
		if kind := strings.TrimSpace(opts.Kind); kind != "" && !strings.EqualFold(item.Kind, kind) {
			continue
		}
		if tag := strings.TrimSpace(opts.Tag); tag != "" && !memoryItemHasTag(item, tag) {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s Store) Show(agentID, idOrPath string) (ShowResult, error) {
	agentID = firstNonEmptyMemory(agentID, "main")
	path := s.resolveAnyMemoryPath(agentID, idOrPath)
	if strings.TrimSpace(path) == "" {
		return ShowResult{}, fmt.Errorf("memory id or path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ShowResult{}, err
	}
	return ShowResult{ID: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Path: path, Text: string(data)}, nil
}

func (s Store) Commit(in CommitInput) (CommitResult, error) {
	if strings.TrimSpace(s.Root) == "" {
		return CommitResult{}, fmt.Errorf("memory root is required")
	}
	agentID := firstNonEmptyMemory(in.AgentID, "main")
	sourcePath := s.resolveProposalPath(agentID, in.Proposal)
	if strings.TrimSpace(sourcePath) == "" {
		return CommitResult{}, fmt.Errorf("memory proposal path is required")
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return CommitResult{}, err
	}
	text := string(data)
	if !strings.Contains(text, "type:") || !strings.Contains(text, "status: proposed") {
		return CommitResult{}, fmt.Errorf("memory proposal must have frontmatter with status: proposed")
	}
	if strings.Contains(text, "type: skill_candidate") {
		return CommitResult{}, fmt.Errorf("skill candidates are promoted through the skill workflow, not memory commit")
	}
	title := firstNonEmptyMemory(in.Title, titleFromMarkdown(text), strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath)))
	targetDir := filepath.Join(s.Root, "agents", agentID, "long")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return CommitResult{}, err
	}
	prefix := longMemoryPrefixForProposal(text)
	filename := slugForMemory(title)
	if prefix != "" {
		filename = prefix + "-" + filename
	}
	targetPath := filepath.Join(targetDir, filename+".md")
	if _, err := os.Stat(targetPath); err == nil {
		targetPath = filepath.Join(targetDir, filename+"-"+time.Now().Format("20060102-150405")+".md")
	} else if !os.IsNotExist(err) {
		return CommitResult{}, err
	}
	now := in.At
	if now.IsZero() {
		now = time.Now()
	}
	longText := strings.Replace(text, "status: proposed", "status: active", 1)
	longText = strings.Replace(longText, "# Memory Proposal:", "# Memory:", 1)
	longText = strings.Replace(longText, "updated_at: "+frontmatterDate(text, "updated_at"), "updated_at: "+now.Format("2006-01-02"), 1)
	if err := os.WriteFile(targetPath, []byte(longText), 0o644); err != nil {
		return CommitResult{}, err
	}
	committedProposal := strings.Replace(text, "status: proposed", "status: committed", 1)
	committedProposal = strings.TrimSpace(committedProposal) + "\n\nCommitted to: [[" + filepath.ToSlash(pathWithoutRoot(s.Root, targetPath)) + "]]\n"
	if err := os.WriteFile(sourcePath, []byte(committedProposal), 0o644); err != nil {
		return CommitResult{}, err
	}
	if err := s.updateAgentIndex(agentID, title, targetPath); err != nil {
		return CommitResult{}, err
	}
	if err := s.appendLog(now, fmt.Sprintf("commit memory: %s -> %s", sourcePath, targetPath)); err != nil {
		return CommitResult{}, err
	}
	s.rebuildIndexBestEffort(now)
	return CommitResult{SourcePath: sourcePath, TargetPath: targetPath, Type: strings.ToLower(strings.TrimSpace(memoryType(text)))}, nil
}

func longMemoryPrefixForProposal(text string) string {
	parsed, err := parseMarkdown(text)
	if err != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Frontmatter.Type)) {
	case "decision", "playbook", "preference", "project":
		return strings.ToLower(strings.TrimSpace(parsed.Frontmatter.Type))
	default:
		return ""
	}
}

func (s Store) Reject(in RejectInput) (RejectResult, error) {
	agentID := firstNonEmptyMemory(in.AgentID, "main")
	path := s.resolveProposalPath(agentID, in.Proposal)
	if strings.TrimSpace(path) == "" {
		return RejectResult{}, fmt.Errorf("memory proposal path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RejectResult{}, err
	}
	text := string(data)
	if !strings.Contains(text, "status: proposed") {
		return RejectResult{}, fmt.Errorf("only proposed memory items can be rejected")
	}
	now := in.At
	if now.IsZero() {
		now = time.Now()
	}
	updated := replaceFrontmatterLine(text, "status", "rejected")
	updated = replaceFrontmatterLine(updated, "updated_at", now.Format("2006-01-02"))
	reason := strings.TrimSpace(in.Reason)
	if reason != "" {
		updated = strings.TrimSpace(updated) + "\n\n## Rejection Reason\n\n" + reason + "\n"
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return RejectResult{}, err
	}
	if err := s.appendLog(now, fmt.Sprintf("reject memory: %s", path)); err != nil {
		return RejectResult{}, err
	}
	s.rebuildIndexBestEffort(now)
	return RejectResult{Path: path}, nil
}

func renderMemoryProposal(agentID, scope, typ, title, body string, sources, tags []string, confidence string, now time.Time) string {
	if len(tags) == 0 {
		tags = []string{"memory-proposal"}
	}
	var b strings.Builder
	fmt.Fprintln(&b, "---")
	fmt.Fprintf(&b, "type: %s\n", typ)
	fmt.Fprintf(&b, "scope: %s\n", scope)
	fmt.Fprintf(&b, "owner_agent: %s\n", agentID)
	fmt.Fprintf(&b, "visibility: %s\n", visibilityForScope(scope))
	fmt.Fprintln(&b, "status: proposed")
	fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(cleanList(tags), ", "))
	fmt.Fprintln(&b, "aliases: []")
	fmt.Fprintln(&b, "sources:")
	for _, source := range cleanList(sources) {
		fmt.Fprintf(&b, "  - %s\n", source)
	}
	if len(cleanList(sources)) == 0 {
		fmt.Fprintln(&b, "  - manual")
	}
	fmt.Fprintf(&b, "confidence: %s\n", confidence)
	fmt.Fprintf(&b, "created_at: %s\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "updated_at: %s\n", now.Format("2006-01-02"))
	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "# Memory Proposal: %s\n\n", title)
	fmt.Fprintln(&b, strings.TrimSpace(body))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Review Notes")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "- Confirm this is stable enough to become long memory.")
	fmt.Fprintln(&b, "- Remove accidental private details before committing.")
	return b.String()
}

func renderLongMemory(agentID, scope, typ, title, body string, sources, tags []string, confidence string, now time.Time) string {
	if len(tags) == 0 {
		tags = []string{"auto-memory"}
	}
	var b strings.Builder
	fmt.Fprintln(&b, "---")
	fmt.Fprintf(&b, "type: %s\n", typ)
	fmt.Fprintf(&b, "scope: %s\n", scope)
	fmt.Fprintf(&b, "owner_agent: %s\n", agentID)
	fmt.Fprintf(&b, "visibility: %s\n", visibilityForScope(scope))
	fmt.Fprintln(&b, "status: active")
	fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(cleanList(tags), ", "))
	fmt.Fprintln(&b, "aliases: []")
	fmt.Fprintln(&b, "sources:")
	for _, source := range cleanList(sources) {
		fmt.Fprintf(&b, "  - %s\n", source)
	}
	if len(cleanList(sources)) == 0 {
		fmt.Fprintln(&b, "  - manual")
	}
	fmt.Fprintf(&b, "confidence: %s\n", confidence)
	fmt.Fprintf(&b, "created_at: %s\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "updated_at: %s\n", now.Format("2006-01-02"))
	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "# Memory: %s\n\n", title)
	fmt.Fprintln(&b, strings.TrimSpace(body))
	return b.String()
}

func (s Store) resolveProposalPath(agentID, proposal string) string {
	proposal = strings.TrimSpace(proposal)
	if proposal == "" {
		return ""
	}
	if filepath.IsAbs(proposal) {
		return proposal
	}
	if strings.HasSuffix(proposal, ".md") {
		return filepath.Join(s.Root, "agents", agentID, "inbox", filepath.Base(proposal))
	}
	return filepath.Join(s.Root, "agents", agentID, "inbox", proposal+".md")
}

func (s Store) resolveAnyMemoryPath(agentID, idOrPath string) string {
	idOrPath = strings.TrimSpace(idOrPath)
	if idOrPath == "" {
		return ""
	}
	if filepath.IsAbs(idOrPath) {
		return idOrPath
	}
	name := idOrPath
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	for _, area := range []string{"inbox", "long"} {
		path := filepath.Join(s.Root, "agents", agentID, area, filepath.Base(name))
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join(s.Root, "agents", agentID, "inbox", filepath.Base(name))
}

func (s Store) updateAgentIndex(agentID, title, targetPath string) error {
	indexPath := filepath.Join(s.Root, "agents", agentID, "index.md")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		return err
	}
	entry := "- [[" + filepath.ToSlash(pathWithoutRoot(filepath.Join(s.Root, "agents", agentID), targetPath)) + "|" + title + "]]"
	data, _ := os.ReadFile(indexPath)
	text := strings.TrimSpace(string(data))
	if strings.Contains(text, entry) {
		return nil
	}
	if text == "" {
		text = "# " + agentID + " Agent Memory\n\n## Long-Term"
	}
	return os.WriteFile(indexPath, []byte(strings.TrimSpace(text)+"\n"+entry+"\n"), 0o644)
}

func (s Store) appendLog(at time.Time, line string) error {
	path := filepath.Join(s.Root, "log.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now()
	}
	entry := fmt.Sprintf("- %s %s\n", at.Format(time.RFC3339), strings.TrimSpace(line))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(entry)
	return err
}

func normalizeMemoryScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "user", "org":
		return strings.ToLower(strings.TrimSpace(scope))
	default:
		return "agent"
	}
}

func visibilityForScope(scope string) string {
	switch scope {
	case "user":
		return "shared-user"
	case "org":
		return "shared-org"
	default:
		return "private"
	}
}

func normalizeConfidence(confidence string) string {
	switch strings.ToLower(strings.TrimSpace(confidence)) {
	case "high", "low":
		return strings.ToLower(strings.TrimSpace(confidence))
	default:
		return "medium"
	}
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func slugForMemory(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	var b strings.Builder
	lastDash := false
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ', r == '-', r == '_', r == '.', r == '/', r == ':':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "memory"
	}
	return out
}

func titleFromMarkdown(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "# "), "Memory Proposal:"))
		}
	}
	return ""
}

func frontmatterDate(text, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func frontmatterValue(text, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), `"`)
		}
	}
	return ""
}

func replaceFrontmatterLine(text, key, value string) string {
	prefix := key + ":"
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			lines[i] = key + ": " + value
			return strings.Join(lines, "\n")
		}
	}
	return text
}

func memoryItemFromText(path, text string) MemoryItem {
	parsed, err := parseMarkdown(text)
	if err != nil {
		parsed = ParsedMarkdown{}
	}
	return MemoryItem{
		ID:      strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		Path:    path,
		Status:  parsed.Frontmatter.Status,
		Title:   titleFromMarkdown(text),
		Kind:    parsed.Frontmatter.Type,
		Tags:    cleanList(parsed.Frontmatter.Tags),
		Sources: cleanList(parsed.Frontmatter.Sources),
		Scope:   parsed.Frontmatter.Scope,
		Updated: parsed.Frontmatter.UpdatedAt,
	}
}

func memoryItemHasTag(item MemoryItem, tag string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return true
	}
	for _, itemTag := range item.Tags {
		if strings.EqualFold(strings.TrimSpace(itemTag), tag) {
			return true
		}
	}
	return false
}

func pathWithoutRoot(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return strings.TrimSuffix(filepath.ToSlash(rel), ".md")
}

var secretPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|token|password|passwd|pwd)\s*[:=]\s*\S+`)

func secretLike(text string) bool {
	return secretPattern.MatchString(text)
}

func firstNonEmptyMemory(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type CatalogSource struct {
	Name    string
	BaseURL string
	Search  string
}

type CatalogSearchOptions struct {
	Sources []CatalogSource
	Client  *http.Client
	Limit   int
	Timeout time.Duration
}

type CatalogItem struct {
	Name        string
	Description string
	Source      string
	URL         string
	InstallURL  string
	RepoURL     string
	Installed   bool
	InstallPath string
}

type InstallResult struct {
	Item        CatalogItem
	TargetDir   string
	TargetPath  string
	AlreadyDone bool
}

var defaultCatalogSources = []CatalogSource{
	{Name: "skills.sh", BaseURL: "https://www.skills.sh", Search: "https://www.skills.sh/?q=%s"},
	{Name: "skillhub.cn", BaseURL: "https://skillhub.cn", Search: "https://skillhub.cn/?q=%s"},
	{Name: "clawhub.ai", BaseURL: "https://clawhub.ai", Search: "https://clawhub.ai/skills?q=%s"},
}

var fallbackCatalogSources = []CatalogSource{
	{Name: "web", BaseURL: "https://duckduckgo.com", Search: "https://duckduckgo.com/html/?q=%s"},
}

var (
	hrefPattern        = regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	tagPattern         = regexp.MustCompile(`(?is)<[^>]+>`)
	metaDescPattern    = regexp.MustCompile(`(?is)<meta\s+(?:name|property)=["'](?:description|og:description)["']\s+content=["']([^"']*)["']`)
	titlePattern       = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	githubRepoPattern  = regexp.MustCompile(`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`)
	installCommandRe   = regexp.MustCompile(`npx\s+skills\s+add\s+(https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?:\s+--skill\s+([A-Za-z0-9_.-]+))?`)
	skillMDLinkPattern = regexp.MustCompile(`https://raw\.githubusercontent\.com/[^\s"'<>]+/SKILL\.md|https://github\.com/[^\s"'<>]+/SKILL\.md`)
)

func DefaultCatalogSources() []CatalogSource {
	return append([]CatalogSource(nil), defaultCatalogSources...)
}

func catalogHTTPClient(opts CatalogSearchOptions, fallback time.Duration) *http.Client {
	if opts.Client != nil {
		return opts.Client
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = fallback
	}
	return &http.Client{Timeout: timeout}
}

func SearchCatalog(ctx context.Context, workspace, query string, opts CatalogSearchOptions) ([]CatalogItem, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	client := catalogHTTPClient(opts, 12*time.Second)
	sources := opts.Sources
	if len(sources) == 0 {
		sources = DefaultCatalogSources()
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 8
	}
	var out []CatalogItem
	seen := map[string]bool{}
	for _, source := range sources {
		items, err := searchOneCatalogSource(ctx, client, source, query)
		if err != nil {
			continue
		}
		for _, item := range items {
			key := strings.ToLower(item.Source + "|" + item.URL + "|" + item.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			item.Installed, item.InstallPath = InstalledInWorkspace(workspace, item.Name)
			out = append(out, item)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	if len(out) == 0 {
		out = searchFallbackCatalogs(ctx, client, workspace, query, limit, seen)
		if len(out) == 0 {
			return nil, nil
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return scoreCatalogItem(out[i], query) > scoreCatalogItem(out[j], query)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func searchFallbackCatalogs(ctx context.Context, client *http.Client, workspace, query string, limit int, seen map[string]bool) []CatalogItem {
	var out []CatalogItem
	for _, source := range fallbackCatalogSources {
		items, err := searchOneCatalogSource(ctx, client, source, query+" agent skill SKILL.md")
		if err != nil {
			continue
		}
		for _, item := range items {
			key := strings.ToLower(item.Source + "|" + item.URL + "|" + item.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			item.Installed, item.InstallPath = InstalledInWorkspace(workspace, item.Name)
			out = append(out, item)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func InstallCatalogSkill(ctx context.Context, workspace, ref string, opts CatalogSearchOptions) (InstallResult, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return InstallResult{}, fmt.Errorf("skill name or URL is required")
	}
	client := catalogHTTPClient(opts, 8*time.Second)
	item, err := resolveInstallItem(ctx, client, workspace, ref, opts)
	if err != nil {
		return InstallResult{}, err
	}
	name := normalizeSkillDirName(firstNonEmpty(item.Name, path.Base(strings.TrimRight(item.URL, "/"))))
	if name == "" {
		return InstallResult{}, fmt.Errorf("cannot determine skill name")
	}
	targetDir := filepath.Join(workspace, "skills", name)
	targetPath := filepath.Join(targetDir, "SKILL.md")
	if _, err := os.Stat(targetPath); err == nil {
		item.Installed = true
		item.InstallPath = targetPath
		return InstallResult{Item: item, TargetDir: targetDir, TargetPath: targetPath, AlreadyDone: true}, nil
	}
	body, installURL, err := downloadSkillMarkdown(ctx, client, item)
	if err != nil {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return InstallResult{}, err
	}
	if err := os.WriteFile(targetPath, []byte(body), 0o644); err != nil {
		return InstallResult{}, err
	}
	item.InstallURL = installURL
	item.InstallPath = targetPath
	return InstallResult{Item: item, TargetDir: targetDir, TargetPath: targetPath}, nil
}

func InstalledInWorkspace(workspace, name string) (bool, string) {
	name = normalizeSkillDirName(name)
	if workspace == "" || name == "" {
		return false, ""
	}
	paths := []string{
		filepath.Join(workspace, "skills", name, "SKILL.md"),
		filepath.Join(workspace, "agents", "main", "skills", name, "SKILL.md"),
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true, p
		}
	}
	return false, ""
}

func ListInstalled(workspace string) ([]Definition, error) {
	reg, err := LoadRegistry(workspace, "main")
	if err != nil {
		return nil, err
	}
	return reg.Definitions(), nil
}

func searchOneCatalogSource(ctx context.Context, client *http.Client, source CatalogSource, query string) ([]CatalogItem, error) {
	searchURL := source.Search
	if strings.TrimSpace(searchURL) == "" {
		searchURL = strings.TrimRight(source.BaseURL, "/") + "/?q=%s"
	}
	searchURL = fmt.Sprintf(searchURL, url.QueryEscape(query))
	data, finalURL, err := fetchText(ctx, client, searchURL, 3<<20)
	if err != nil {
		return nil, err
	}
	if items := parseJSONCatalog(source, data); len(items) > 0 {
		return filterCatalogItems(items, query), nil
	}
	items := parseHTMLCatalog(source, finalURL, data, query)
	return filterCatalogItems(items, query), nil
}

func parseJSONCatalog(source CatalogSource, raw string) []CatalogItem {
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil
	}
	var out []CatalogItem
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			name := stringField(x, "name", "title", "slug")
			desc := stringField(x, "description", "summary")
			link := stringField(x, "url", "href", "link", "homepage")
			repo := stringField(x, "repo", "repository", "repository_url", "github")
			if repo != "" && strings.HasPrefix(repo, "github.com/") {
				repo = "https://" + repo
			}
			if link == "" {
				link = repo
			}
			if name != "" && link != "" {
				out = append(out, CatalogItem{Name: name, Description: desc, Source: source.Name, URL: absoluteURL(source.BaseURL, link), RepoURL: repo})
			}
			for _, value := range x {
				walk(value)
			}
		}
	}
	walk(doc)
	return dedupeCatalog(out)
}

func parseHTMLCatalog(source CatalogSource, pageURL, raw, query string) []CatalogItem {
	var out []CatalogItem
	pageRepo := firstRegexGroup(githubRepoPattern, raw)
	var pageInstallRepo, pageInstallSkill string
	if cmd := installCommandRe.FindStringSubmatch(raw); len(cmd) > 0 {
		pageInstallRepo = cmd[1]
		if len(cmd) > 2 {
			pageInstallSkill = cmd[2]
		}
	}
	for _, match := range hrefPattern.FindAllStringSubmatch(raw, -1) {
		href := html.UnescapeString(strings.TrimSpace(match[1]))
		text := cleanHTMLText(match[2])
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") {
			continue
		}
		full := absoluteURL(pageURL, href)
		if !looksLikeSkillLink(source, href, text, query) {
			continue
		}
		name := inferNameFromLink(text, full)
		if name == "" {
			continue
		}
		item := CatalogItem{Name: name, Description: inferDescriptionNear(raw, match[0]), Source: source.Name, URL: full}
		if pageRepo != "" && strings.Contains(full, "/"+name) {
			item.RepoURL = pageRepo
		}
		if pageInstallRepo != "" && (pageInstallSkill == "" || pageInstallSkill == name) {
			item.RepoURL = pageInstallRepo
			item.InstallURL = pageInstallRepo
		}
		out = append(out, item)
	}
	if len(out) == 0 && strings.Contains(strings.ToLower(raw), strings.ToLower(query)) {
		name := cleanHTMLText(firstRegexGroup(titlePattern, raw))
		desc := cleanHTMLText(firstRegexGroup(metaDescPattern, raw))
		if name != "" {
			out = append(out, CatalogItem{Name: name, Description: desc, Source: source.Name, URL: pageURL})
		}
	}
	return dedupeCatalog(out)
}

func resolveInstallItem(ctx context.Context, client *http.Client, workspace, ref string, opts CatalogSearchOptions) (CatalogItem, error) {
	if u, err := url.Parse(ref); err == nil && u.Scheme != "" && u.Host != "" {
		name := path.Base(strings.TrimRight(u.Path, "/"))
		return CatalogItem{Name: name, URL: ref, Source: u.Host}, nil
	}
	items, err := SearchCatalog(ctx, workspace, ref, opts)
	if err != nil {
		return CatalogItem{}, err
	}
	if len(items) == 0 {
		return CatalogItem{}, fmt.Errorf("no matching skill found for %q", ref)
	}
	return items[0], nil
}

func downloadSkillMarkdown(ctx context.Context, client *http.Client, item CatalogItem) (string, string, error) {
	candidates := skillMarkdownCandidates(item)
	for _, candidate := range candidates {
		body, finalURL, err := fetchText(ctx, client, candidate, 4<<20)
		if err == nil && isSkillMarkdown(body) {
			return normalizeSkillMarkdown(body, item.Name), finalURL, nil
		}
	}
	pageURL := firstNonEmpty(item.URL, item.RepoURL)
	if pageURL == "" {
		return "", "", fmt.Errorf("no install URL found")
	}
	page, _, err := fetchText(ctx, client, pageURL, 4<<20)
	if err != nil {
		return "", "", err
	}
	if strings.Contains(strings.TrimSpace(item.URL), "skills.sh/") {
		if enriched := enrichItemFromSkillsPage(page, item); enriched.RepoURL != "" || enriched.InstallURL != "" {
			item = enriched
			for _, candidate := range skillMarkdownCandidates(item) {
				body, finalURL, err := fetchText(ctx, client, candidate, 4<<20)
				if err == nil && isSkillMarkdown(body) {
					return normalizeSkillMarkdown(body, item.Name), finalURL, nil
				}
			}
		}
	}
	for _, raw := range append(skillMDLinkPattern.FindAllString(page, -1), htmlSkillMDLinks(page, pageURL)...) {
		candidate := githubBlobToRaw(html.UnescapeString(raw))
		body, finalURL, err := fetchText(ctx, client, candidate, 4<<20)
		if err == nil && isSkillMarkdown(body) {
			return normalizeSkillMarkdown(body, item.Name), finalURL, nil
		}
	}
	if body, ok := extractSkillMarkdownFromSkillsPage(page, item.Name); ok {
		return body, pageURL, nil
	}
	return "", "", fmt.Errorf("found %s but could not locate SKILL.md", pageURL)
}

func enrichItemFromSkillsPage(page string, item CatalogItem) CatalogItem {
	if cmd := installCommandRe.FindStringSubmatch(page); len(cmd) > 0 {
		item.RepoURL = cmd[1]
		item.InstallURL = cmd[1]
		if len(cmd) > 2 && cmd[2] != "" {
			item.Name = cmd[2]
		}
		return item
	}
	if repo := firstRegexGroup(githubRepoPattern, page); repo != "" {
		item.RepoURL = repo
	}
	return item
}

func htmlSkillMDLinks(raw, pageURL string) []string {
	var out []string
	for _, match := range hrefPattern.FindAllStringSubmatch(raw, -1) {
		if len(match) < 2 {
			continue
		}
		href := html.UnescapeString(strings.TrimSpace(match[1]))
		if strings.HasSuffix(strings.ToLower(href), "skill.md") {
			out = append(out, absoluteURL(pageURL, href))
		}
	}
	return out
}

func skillMarkdownCandidates(item CatalogItem) []string {
	var out []string
	for _, raw := range []string{item.InstallURL, item.URL, item.RepoURL} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "raw.githubusercontent.com") && strings.HasSuffix(raw, "SKILL.md") {
			out = append(out, raw)
		}
		if strings.Contains(raw, "github.com/") {
			raw = strings.TrimSuffix(raw, "/")
			if strings.Contains(raw, "/blob/") && strings.HasSuffix(raw, "SKILL.md") {
				out = append(out, githubBlobToRaw(raw))
				continue
			}
			for _, branch := range []string{"main", "master"} {
				if item.Name != "" {
					out = append(out, githubRepoRawURL(raw, branch, "skills/"+item.Name+"/SKILL.md"))
					out = append(out, githubRepoRawURL(raw, branch, "skill-data/"+item.Name+"/SKILL.md"))
					out = append(out, fmt.Sprintf("%s/raw/%s/skills/%s/SKILL.md", raw, branch, item.Name))
					out = append(out, fmt.Sprintf("%s/raw/%s/skill-data/%s/SKILL.md", raw, branch, item.Name))
				}
				out = append(out, githubRepoRawURL(raw, branch, "SKILL.md"))
				out = append(out, fmt.Sprintf("%s/raw/%s/SKILL.md", raw, branch))
			}
		}
		if strings.HasSuffix(raw, "SKILL.md") {
			out = append(out, raw)
		}
	}
	return dedupeStrings(out)
}

func githubRepoRawURL(repoURL, branch, filePath string) string {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Host != "github.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/" + branch + "/" + strings.TrimLeft(filePath, "/")
}

func extractSkillMarkdownFromSkillsPage(raw, fallbackName string) (string, bool) {
	marker := `"previewHtml":"`
	start := strings.Index(raw, marker)
	if start < 0 {
		return "", false
	}
	start += len(marker)
	end := strings.Index(raw[start:], `","restHtml"`)
	if end < 0 {
		return "", false
	}
	fragment := raw[start : start+end]
	fragment = strings.ReplaceAll(fragment, `\n`, "\n")
	fragment = strings.ReplaceAll(fragment, `\"`, `"`)
	fragment = strings.ReplaceAll(fragment, `\u003c`, "<")
	fragment = strings.ReplaceAll(fragment, `\u003e`, ">")
	fragment = strings.ReplaceAll(fragment, `\u0026`, "&")
	text := cleanHTMLText(fragment)
	if !strings.Contains(strings.ToLower(text), strings.ToLower(firstNonEmpty(fallbackName, "skill"))) {
		return "", false
	}
	return normalizeSkillMarkdown(text, fallbackName), true
}

func normalizeSkillMarkdown(raw, fallbackName string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if strings.HasPrefix(raw, "---\n") {
		return raw + "\n"
	}
	name := normalizeSkillDirName(fallbackName)
	if name == "" {
		name = "installed-skill"
	}
	desc := firstParagraph(raw)
	if desc == "" {
		desc = "Installed skill."
	}
	return fmt.Sprintf("---\nname: %s\ndescription: %s\nstage: planning\n---\n\n%s\n", name, sanitizeYAMLScalar(desc), raw)
}

func isSkillMarkdown(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.Contains(trimmed, "#") && (strings.Contains(trimmed, "description:") || strings.Contains(strings.ToLower(trimmed), "skill"))
}

func fetchText(ctx context.Context, client *http.Client, rawURL string, limit int64) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", rawURL, err
	}
	req.Header.Set("user-agent", "mateway-skill-catalog/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", rawURL, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.Request.URL.String(), fmt.Errorf("GET %s failed with status %d", rawURL, resp.StatusCode)
	}
	return string(data), resp.Request.URL.String(), nil
}

func filterCatalogItems(items []CatalogItem, query string) []CatalogItem {
	terms := queryTerms(query)
	var out []CatalogItem
	for _, item := range items {
		if isCatalogNavigationItem(item) {
			continue
		}
		hay := strings.ToLower(item.Name + " " + item.Description + " " + item.URL + " " + item.RepoURL)
		matched := len(terms) == 0
		misses := 0
		for _, term := range terms {
			if !strings.Contains(hay, term) {
				misses++
			}
		}
		if len(terms) > 0 && misses == 0 {
			matched = true
		}
		if matched {
			out = append(out, item)
		}
	}
	return dedupeCatalog(out)
}

func scoreCatalogItem(item CatalogItem, query string) int {
	q := strings.ToLower(strings.TrimSpace(query))
	name := strings.ToLower(item.Name)
	score := 0
	if name == q {
		score += 100
	}
	if strings.Contains(name, q) {
		score += 50
	}
	if strings.Contains(strings.ToLower(item.Description), q) {
		score += 20
	}
	return score
}

func looksLikeSkillLink(source CatalogSource, href, text, query string) bool {
	lower := strings.ToLower(href + " " + text)
	if strings.Contains(lower, "/_next/") || strings.Contains(lower, ".css") || strings.Contains(lower, ".js") || strings.Contains(lower, ".svg") {
		return false
	}
	if strings.Contains(lower, "/agent/") || strings.Contains(lower, "/topic") || strings.Contains(lower, "/docs") {
		return false
	}
	for _, term := range queryTerms(query) {
		if strings.Contains(lower, term) {
			return true
		}
	}
	if strings.Contains(lower, "github.com") {
		return true
	}
	if strings.Contains(lower, "skill") && !strings.Contains(lower, "/agent/") && !strings.Contains(lower, "/topic") && !strings.Contains(lower, "/docs") {
		return true
	}
	_ = source
	return false
}

func isCatalogNavigationItem(item CatalogItem) bool {
	u, err := url.Parse(item.URL)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	return strings.HasPrefix(p, "/agent/") || strings.HasPrefix(p, "/topic") || strings.HasPrefix(p, "/docs")
}

func inferNameFromLink(text, link string) string {
	text = strings.TrimSpace(text)
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	base := path.Base(strings.TrimRight(u.Path, "/"))
	if sourceName := skillsSHNameFromPath(u.Path); sourceName != "" {
		return sourceName
	}
	if text != "" && len([]rune(text)) <= 80 && !isMostlyNumber(text) {
		return strings.Fields(text)[0]
	}
	return strings.TrimSpace(base)
}

func skillsSHNameFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) >= 3 && parts[0] != "agent" && parts[0] != "topic" {
		return parts[len(parts)-1]
	}
	return ""
}

func isMostlyNumber(text string) bool {
	digits := 0
	letters := 0
	for _, r := range text {
		if r >= '0' && r <= '9' {
			digits++
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			letters++
		}
	}
	return digits > 0 && letters == 0
}

func inferDescriptionNear(raw, anchor string) string {
	idx := strings.Index(raw, anchor)
	if idx < 0 {
		return ""
	}
	end := idx + len(anchor) + 500
	if end > len(raw) {
		end = len(raw)
	}
	return truncateText(cleanHTMLText(raw[idx:end]), 220)
}

func cleanHTMLText(raw string) string {
	raw = tagPattern.ReplaceAllString(raw, " ")
	raw = html.UnescapeString(raw)
	return strings.Join(strings.Fields(raw), " ")
}

func absoluteURL(base, ref string) string {
	u, err := url.Parse(ref)
	if err == nil && u.Scheme != "" {
		return ref
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return ref
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return baseURL.ResolveReference(rel).String()
}

func githubBlobToRaw(raw string) string {
	raw = strings.Replace(raw, "https://github.com/", "https://raw.githubusercontent.com/", 1)
	raw = strings.Replace(raw, "/blob/", "/", 1)
	raw = strings.Replace(raw, "/raw/", "/", 1)
	return raw
}

func normalizeSkillDirName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func firstRegexGroup(re *regexp.Regexp, text string) string {
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

func stringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func queryTerms(query string) []string {
	parts := strings.Fields(strings.ToLower(query))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(part, " \t\r\n,.;:!?()[]{}\"'")
		if part != "" && part != "skill" && part != "skills" {
			out = append(out, part)
		}
	}
	return out
}

func firstParagraph(raw string) string {
	for _, block := range strings.Split(raw, "\n\n") {
		block = strings.TrimSpace(strings.TrimPrefix(block, "#"))
		if block != "" {
			return truncateText(strings.Join(strings.Fields(block), " "), 180)
		}
	}
	return ""
}

func sanitizeYAMLScalar(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, `"`, `'`)
	return `"` + truncateText(value, 240) + `"`
}

func truncateText(text string, limit int) string {
	if len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "..."
}

func dedupeCatalog(items []CatalogItem) []CatalogItem {
	seen := map[string]bool{}
	var out []CatalogItem
	for _, item := range items {
		key := strings.ToLower(item.Source + "|" + item.URL + "|" + item.Name)
		if key == "||" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range in {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

package memory

import (
	"os"
	"path/filepath"
	"strings"
)

type ReportInput struct {
	Home       string
	MemoryRoot string
}

type Report struct {
	MemoryRoot   string
	MemoryFiles  int
	IndexEntries int
	Issues       []Issue
	Proposals    map[string]int
	Observe      map[string]int
}

func BuildReport(input ReportInput) (Report, error) {
	home := strings.TrimSpace(input.Home)
	if home == "" {
		home = ".mateway"
	}
	root := strings.TrimSpace(input.MemoryRoot)
	if root == "" {
		root = filepath.Join(home, "workspace", "memory")
	}
	lint, err := LintRoot(root)
	if err != nil {
		return Report{}, err
	}
	index, issues, err := RebuildIndex(root)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		MemoryRoot:   root,
		MemoryFiles:  lint.Files,
		IndexEntries: len(index.Entries),
		Issues:       append(lint.Issues, issues...),
		Proposals:    map[string]int{},
		Observe:      map[string]int{},
	}
	proposals, err := ProposalStore{Home: home}.List()
	if err != nil {
		return Report{}, err
	}
	for _, proposal := range proposals {
		status := strings.TrimSpace(proposal.Status)
		if status == "" {
			status = "unknown"
		}
		report.Proposals[status]++
	}
	for _, name := range []string{"diary", "reflections", "proposals", "audit"} {
		count, err := countObserveFiles(filepath.Join(home, "observe", name))
		if err != nil {
			return Report{}, err
		}
		report.Observe[name] = count
	}
	return report, nil
}

func countObserveFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			count++
		}
	}
	return count, nil
}

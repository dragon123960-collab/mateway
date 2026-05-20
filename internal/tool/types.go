package tool

import "context"

type Risk string

const (
	RiskSafeRead        Risk = "safe_read"
	RiskGuardedMutation Risk = "guarded_mutation"
	RiskDangerous       Risk = "dangerous_execute"
)

type Definition struct {
	Name        string
	Description string
	ArgsSchema  map[string]string
	Risk        Risk
	Run         func(context.Context, Call) Result
}

type Call struct {
	Name      string
	Args      map[string]string
	Confirmed bool
	Context   Context
}

type Context struct {
	Home          string
	ProjectRoot   string
	Workspace     string
	AllowedRoots  []string
	ConfigSummary string
	Search        SearchConfig
}

type SearchConfig struct {
	TavilyEnabled        bool
	TavilyBaseURL        string
	TavilyAPIKey         string
	TavilyMaxResults     int
	TavilySearchDepth    string
	TavilyTopic          string
	DuckDuckGoEnabled    bool
	DuckDuckGoMaxResults int
	DuckDuckGoRegion     string
}

type Result struct {
	OK              bool
	Output          string
	Evidence        map[string]any
	Error           string
	RequiresConfirm bool
	ConfirmMessage  string
}

func ErrorResult(err string) Result {
	return Result{OK: false, Error: err, Output: err}
}

func ConfirmResult(message string, evidence map[string]any) Result {
	return Result{
		OK:              false,
		Output:          message,
		RequiresConfirm: true,
		ConfirmMessage:  message,
		Evidence:        evidence,
	}
}

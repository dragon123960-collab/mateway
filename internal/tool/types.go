package tool

import "context"

type Risk string

const (
	RiskSafeRead        Risk = "safe_read"
	RiskGuardedMutation Risk = "guarded_mutation"
	RiskDangerous       Risk = "dangerous_execute"
)

type AcceptanceMode string

const (
	AcceptanceCodeOnly AcceptanceMode = "code_only"
	AcceptanceCodeLLM  AcceptanceMode = "code_then_llm"
	AcceptanceLLM      AcceptanceMode = "llm_default"
)

type ParallelMode string

const (
	ParallelForbid       ParallelMode = "forbid"
	ParallelReadOnlyOK   ParallelMode = "read_only_ok"
	ParallelIsolatedOnly ParallelMode = "isolated_only"
)

type ReusePolicy string

const (
	ReuseNever      ReusePolicy = "never"
	ReuseStableRead ReusePolicy = "stable_read"
)

type Metadata struct {
	Purpose            string
	WhenToUse          []string
	WhenNotToUse       []string
	RequiredArgs       []string
	OutputContract     []string
	AcceptanceSpecRef  string
	AcceptanceMode     AcceptanceMode
	SoftFailureSignals []string
	ParallelMode       ParallelMode
	ResourceScope      string
	ReusePolicy        ReusePolicy
	RecoverHints       []string
}

type Definition struct {
	Name        string
	Description string
	ArgsSchema  map[string]string
	Metadata    Metadata
	Risk        Risk
	Hidden      bool
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
	DefaultTool              string
	CacheDir                 string
	CacheEnabled             bool
	CacheTTLHours            int
	FreshCacheTTLHours       int
	ProviderOrder            []string
	TavilyEnabled            bool
	TavilyBaseURL            string
	TavilyAPIKey             string
	TavilyTimeoutSeconds     int
	TavilyMaxResults         int
	TavilyDailyBudget        int
	TavilyMonthlyBudget      int
	TavilySearchDepth        string
	TavilyTopic              string
	DuckDuckGoEnabled        bool
	DuckDuckGoTimeoutSeconds int
	DuckDuckGoMaxResults     int
	DuckDuckGoRegion         string
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

package skills

type Type string

const (
	TypeCLI      Type = "cli"
	TypeAPI      Type = "api"
	TypeWorkflow Type = "workflow"
)

type Manifest struct {
	Name          string         `yaml:"name"`
	Version       string         `yaml:"version,omitempty"`
	Description   string         `yaml:"description"`
	Homepage      string         `yaml:"homepage,omitempty"`
	License       string         `yaml:"license,omitempty"`
	Compatibility string         `yaml:"compatibility,omitempty"`
	AllowedTools  []string       `yaml:"allowed-tools,omitempty"`
	ResourceDirs  []string       `yaml:"resource_dirs,omitempty"`
	Context       string         `yaml:"context,omitempty"`
	Agent         string         `yaml:"agent,omitempty"`
	Model         string         `yaml:"model,omitempty"`
	Metadata      map[string]any `yaml:"metadata,omitempty"`
	Type          Type           `yaml:"-"`
	Entry         string         `yaml:"-"`
	Method        string         `yaml:"-"`
	URL           string         `yaml:"-"`
	Env           []string       `yaml:"-"`
	ReadOnly      bool           `yaml:"-"`
	RiskLevel     string         `yaml:"-"`
	Tags          []string       `yaml:"-"`
}

type MetaFile struct {
	OwnerID     string          `json:"ownerId,omitempty"`
	Slug        string          `json:"slug,omitempty"`
	Version     string          `json:"version,omitempty"`
	PublishedAt int64           `json:"publishedAt,omitempty"`
	Mateway     RuntimeMetadata `json:"mateway,omitempty"`
}

type RuntimeMetadata struct {
	Type      Type     `json:"type,omitempty"`
	Entry     string   `json:"entry,omitempty"`
	Method    string   `json:"method,omitempty"`
	URL       string   `json:"url,omitempty"`
	Env       []string `json:"env,omitempty"`
	ReadOnly  bool     `json:"read_only,omitempty"`
	RiskLevel string   `json:"risk_level,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

type Skill struct {
	Manifest   Manifest
	Directory  string
	SkillPath  string
	MetaPath   string
	Body       string
	Resources  ResourceSet
	Executable bool
}

type ResourceSet struct {
	Scripts    []string
	References []string
	Assets     []string
	Extra      map[string][]string
}

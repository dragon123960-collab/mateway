package skill

type Definition struct {
	Name             string
	Description      string
	Tags             []string
	Priority         int
	Stage            string
	Scope            string
	WhenContains     []string
	WhenResultKinds  []string
	WhenUserLanguage string
	UseFor           []string
	Produces         []string
	AcceptanceMode   string
	ParallelMode     string
	AcceptancePrompt string
	Dir              string
	Instruction      string
}

type Context struct {
	UserText string
	Results  []ResultRef
}

type ResultRef struct {
	Kind string
}

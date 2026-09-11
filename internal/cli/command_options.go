package cli

import "io"

// commandOptions is the typed result of one registered Cobra/pflag parse.
// Business handlers receive exactly these values; they never inspect argv.
type commandOptions struct {
	JSON           bool
	NoContext      bool
	Yes            bool
	DryRun         bool
	From           string
	Overwrite      bool
	Refresh        bool
	All            bool
	Remote         bool
	Apply          bool
	WorkspaceID    string
	DiscardChanges bool
	AssumeStopped  bool
	Project        string
	Convex         bool
	BackendPath    string
	Profile        string
	SiteURLService string
	Update         bool
	Write          bool
	TokenStdin     bool
	NoDescriptions bool
	Shell          string

	Progress io.Writer `json:"-"`
}

func newCommandOptions(stdout io.Writer) *commandOptions {
	return &commandOptions{Profile: "default", Shell: "auto", Progress: stdout}
}

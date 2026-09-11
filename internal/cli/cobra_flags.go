package cli

import "github.com/spf13/cobra"

func boolFlagValueCompletion(command *cobra.Command, name string) {
	_ = command.RegisterFlagCompletionFunc(name, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		var out []string
		for _, value := range []string{"true", "false"} {
			if hasCompletionPrefix(value, toComplete) {
				out = append(out, value)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	})
}

func freeValueCompletion(command *cobra.Command, name, help string) {
	_ = command.RegisterFlagCompletionFunc(name, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if toComplete == "" && help != "" {
			return cobra.AppendActiveHelp(nil, help), cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	})
}

func jsonFlag(command *cobra.Command, options *commandOptions) {
	command.Flags().BoolVar(&options.JSON, "json", false, "emit the versioned JSON result")
	boolFlagValueCompletion(command, "json")
}

func jsonOnlyFlags(command *cobra.Command, options *commandOptions) { jsonFlag(command, options) }

func createFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().BoolVar(&options.Yes, "yes", false, "approve source registration, allocation and this workspace creation")
	boolFlagValueCompletion(command, "yes")
	command.Flags().BoolVar(&options.DryRun, "dry-run", false, "preview this create without state/worktree/provider changes")
	boolFlagValueCompletion(command, "dry-run")
	command.Flags().StringVar(&options.From, "from", "", "existing commit/ref for a new branch")
	localRefCompletion(command)
}

func planFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().StringVar(&options.From, "from", "", "existing commit/ref for a new branch")
	localRefCompletion(command)
}

func resumeFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().BoolVar(&options.DryRun, "dry-run", false, "show the unfinished operation without attempting it")
	boolFlagValueCompletion(command, "dry-run")
}

func statusFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().BoolVar(&options.Refresh, "refresh", false, "include read-only provider identity checks")
	boolFlagValueCompletion(command, "refresh")
}

func syncFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().BoolVar(&options.Overwrite, "overwrite-managed", false, "replace an externally edited EVE-managed value")
	boolFlagValueCompletion(command, "overwrite-managed")
	command.Flags().BoolVar(&options.DryRun, "dry-run", false, "resolve supported changes without journaling or writing")
	boolFlagValueCompletion(command, "dry-run")
}

func listFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().BoolVar(&options.All, "all", false, "list every registered repository")
	boolFlagValueCompletion(command, "all")
}

func doctorFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().BoolVar(&options.Remote, "remote", false, "include read-only provider identity checks")
	boolFlagValueCompletion(command, "remote")
}

func gcFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().BoolVar(&options.Apply, "apply", false, "perform the explicit cleanup operation shown")
	boolFlagValueCompletion(command, "apply")
	command.Flags().StringVar(&options.WorkspaceID, "workspace", "", "exact workspace UUID whose cleanup is selected")
	globalWorkspaceIDCompletion(command)
}

func destroyFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().BoolVar(&options.Yes, "yes", false, "approve removal of the exact workspace")
	boolFlagValueCompletion(command, "yes")
	command.Flags().BoolVar(&options.DryRun, "dry-run", false, "show exact planned effects without mutation")
	boolFlagValueCompletion(command, "dry-run")
	command.Flags().BoolVar(&options.DiscardChanges, "discard-changes", false, "discard reviewed user work in the worktree")
	boolFlagValueCompletion(command, "discard-changes")
	command.Flags().BoolVar(&options.AssumeStopped, "assume-stopped", false, "assert the project launcher has been separately assessed")
	boolFlagValueCompletion(command, "assume-stopped")
}

func initFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().BoolVar(&options.DryRun, "dry-run", false, "show the proposal without mutation")
	boolFlagValueCompletion(command, "dry-run")
	command.Flags().BoolVar(&options.Write, "write", false, "write the reviewed manifest")
	boolFlagValueCompletion(command, "write")
	command.Flags().BoolVar(&options.Yes, "yes", false, "approve this exact write")
	boolFlagValueCompletion(command, "yes")
	command.Flags().StringVar(&options.Project, "project", "", "explicit team:project binding")
	projectCompletion(command)
	command.Flags().BoolVar(&options.Convex, "convex", false, "initialize Convex workflow")
	boolFlagValueCompletion(command, "convex")
	command.Flags().StringVar(&options.BackendPath, "backend-path", "", "explicit discovered Convex package path")
	backendPathCompletion(command)
	command.Flags().StringVar(&options.Profile, "profile", "", "Convex credential profile; empty inherits the default onboarding rule")
	initProfileCompletion(command)
	command.Flags().StringVar(&options.SiteURLService, "site-url-service", "", "service that supplies backend SITE_URL")
	serviceIDCompletion(command)
	command.Flags().BoolVar(&options.Update, "update", false, "update a committed manifest")
	boolFlagValueCompletion(command, "update")
}

func loginFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().StringVar(&options.Project, "project", "", "explicit team:project binding to validate")
	projectCompletion(command)
	command.Flags().StringVar(&options.Profile, "profile", "default", "credential profile name")
	profileCompletion(command, true)
	command.Flags().BoolVar(&options.TokenStdin, "token-stdin", false, "read one token from standard input")
	boolFlagValueCompletion(command, "token-stdin")
}

func profileFlags(command *cobra.Command, options *commandOptions) {
	jsonFlag(command, options)
	command.Flags().StringVar(&options.Profile, "profile", "default", "credential profile name")
	profileCompletion(command, false)
}

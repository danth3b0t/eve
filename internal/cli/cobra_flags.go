package cli

import "github.com/spf13/cobra"

func jsonFlag(command *cobra.Command) {
	command.Flags().Bool("json", false, "emit the versioned JSON result")
}

func jsonOnlyFlags(command *cobra.Command) { jsonFlag(command) }
func createFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().Bool("yes", false, "approve source registration, allocation and this workspace creation")
	localRefCompletion(command)
	command.Flags().String("from", "", "existing commit/ref for a new branch")
}

func planFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().String("from", "", "existing commit/ref for a new branch")
	localRefCompletion(command)
}

func selectorFlags(command *cobra.Command) { jsonFlag(command) }

func statusFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().Bool("refresh", false, "include read-only provider identity checks")
}

func syncFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().Bool("overwrite-managed", false, "replace an externally edited EVE-managed value")
}

func listFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().Bool("all", false, "list every registered repository")
}

func doctorFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().Bool("remote", false, "include read-only provider identity checks")
}

func gcFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().Bool("apply", false, "perform the explicit cleanup operation shown")
	command.Flags().String("workspace", "", "exact workspace UUID whose cleanup is selected")
}

func destroyFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().Bool("yes", false, "approve removal of the exact workspace")
	command.Flags().Bool("discard-changes", false, "discard reviewed user work in the worktree")
	command.Flags().Bool("assume-stopped", false, "assert the project launcher has been separately assessed")
}

func initFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().Bool("dry-run", false, "show the proposal without mutation")
	command.Flags().Bool("write", false, "write the reviewed manifest")
	command.Flags().Bool("yes", false, "approve this exact write")
	command.Flags().String("project", "", "explicit team:project binding")
	command.Flags().Bool("convex", false, "initialize Convex workflow")
	command.Flags().String("backend-path", "", "explicit discovered Convex package path")
	command.Flags().String("profile", "", "Convex credential profile")
	command.Flags().String("site-url-service", "", "service that supplies backend SITE_URL")
	command.Flags().Bool("update", false, "update a committed manifest")
}

func loginFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().String("project", "", "explicit team:project binding")
	command.Flags().String("profile", "", "credential profile name")
	profileCompletion(command)
	command.Flags().Bool("token-stdin", false, "read one token from standard input")
}

func profileFlags(command *cobra.Command) {
	jsonFlag(command)
	command.Flags().String("profile", "", "credential profile name")
	profileCompletion(command)
}

func createSelectorCompletion(command *cobra.Command) {}

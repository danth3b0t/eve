package domain

// GitIdentity is observed filesystem/Git metadata, not deletion authorization by
// itself. The lifecycle must match it to an owned workspace in the registry.
// Inode identities make path replacement/movement distinct from ordinary edits.
type GitIdentity struct {
	Path, PathIdentity        string
	CommonDir, CommonIdentity string
	AdminDir, AdminIdentity   string
}

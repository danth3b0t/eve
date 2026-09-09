// Package lifecycle coordinates domain boundaries. This file implements only
// the Git slice; it is not create/prepared, publication, or full recovery.
package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/git"
	"eve/internal/state"
	"github.com/google/uuid"
)

type GitPlan struct {
	WorkspaceID string
	Path        string
	Repository  state.Repository
	Source      git.Checkout
	Target      git.Target
}

// OpenForGit rejects repository-contained state before state.Open can create
// anything. Read-only CLI inspection will need a separate non-mutating opener.
func OpenForGit(ctx context.Context, g *git.Client, cwd, root string) (*state.Store, error) {
	checkout, err := g.Inspect(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if err := g.Compatible(ctx, checkout); err != nil {
		return nil, err
	}
	canonical, err := g.CheckStateLocation(ctx, checkout.Identity.Path, root)
	if err != nil {
		return nil, err
	}
	return state.Open(ctx, canonical)
}

// RegisterSource is called only after explicit registration consent. Manual
// manifest authoring/discovery and init's UI are separate, not inferred here.
func RegisterSource(ctx context.Context, s *state.Store, g *git.Client, cwd string) (state.Repository, error) {
	checkout, err := g.Inspect(ctx, cwd)
	if err != nil {
		return state.Repository{}, err
	}
	if err := g.Compatible(ctx, checkout); err != nil {
		return state.Repository{}, err
	}
	if err := outsideState(s, checkout); err != nil {
		return state.Repository{}, err
	}
	return s.RegisterRepository(ctx, checkout.Identity.CommonDir, checkout.Identity.Path, filepath.Base(checkout.Identity.Path))
}

// PlanGit is read-only. It resolves the canonical registered source even when
// invoked from a different linked worktree, and uses the target's committed
// manifest. It is not the complete file/provider/loader preflight or CLI plan.
func PlanGit(ctx context.Context, s *state.Store, g *git.Client, cwd, branch, from string) (GitPlan, error) {
	var p GitPlan
	invoking, err := g.Inspect(ctx, cwd)
	if err != nil {
		return p, err
	}
	p.Repository, err = s.RepositoryForCommon(ctx, invoking.Identity.CommonDir, invoking.Identity.CommonIdentity)
	if err != nil {
		return p, err
	}
	p.Source, err = registeredSource(ctx, s, g, p.Repository)
	if err != nil {
		return p, err
	}
	p.Target, err = g.Plan(ctx, p.Source, branch, from)
	if err != nil {
		return p, err
	}
	p.WorkspaceID = uuid.NewString()
	p.Path, err = git.WorkspacePath(p.Source.Identity.Path, p.Repository.Label, p.Repository.ID, branch, p.WorkspaceID)
	return p, err
}

func (p GitPlan) Intent(ports config.UserConfig) state.CreateRequest {
	return state.CreateRequest{RepositoryID: p.Repository.ID, Branch: p.Target.Branch, Path: p.Path, HeadOID: p.Target.HeadOID, Manifest: p.Target.Manifest, Ports: ports, NewBranch: p.Target.NewBranch}
}

func registeredSource(ctx context.Context, s *state.Store, g *git.Client, r state.Repository) (git.Checkout, error) {
	source, err := g.Inspect(ctx, r.SourcePath)
	if err != nil {
		return git.Checkout{}, err
	}
	id := source.Identity
	if id.Path != r.SourcePath || id.PathIdentity != r.SourceIdentity || id.CommonDir != r.CommonDir || id.CommonIdentity != r.CommonIdentity {
		return git.Checkout{}, &domain.Error{Code: "E_SOURCE_IDENTITY", Message: "registered source no longer belongs to the recorded repository"}
	}
	return source, outsideState(s, source)
}

func outsideState(s *state.Store, checkout git.Checkout) error {
	scratch, err := s.ScratchDir()
	if err != nil {
		return err
	}
	root := filepath.Dir(scratch)
	for _, path := range []string{checkout.Identity.Path, checkout.Identity.CommonDir, checkout.Identity.AdminDir} {
		rel, err := filepath.Rel(path, root)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return &domain.Error{Code: "E_STATE_PATH", Message: "EVE state must be outside repository and Git administrative directories", Path: root}
		}
	}
	return nil
}

// PrepareGit expects this store's workspace lock, an approved frozen create
// intent and completed allocation. It holds the repository lock around Git but
// never holds SQL transactions over a subprocess. The caller keeps the workspace
// lock across subsequent file/provider phases. No files are materialized here.
//
// Re-invocation reconciles an exact completed creation receipt or a recorded
// identity. Incomplete Git initialization without such evidence is a diagnostic,
// not a blind retry/reset/adoption. Full resume/destroy orchestration is pending.
func PrepareGit(ctx context.Context, s *state.Store, g *git.Client, locked *state.LockedWorkspace) (domain.GitIdentity, error) {
	step, err := locked.GitStep(ctx)
	if err != nil {
		return domain.GitIdentity{}, err
	}
	r, err := s.Repository(ctx, step.Workspace.RepositoryID)
	if err != nil {
		return domain.GitIdentity{}, err
	}
	repoLock, err := s.LockRepository(r.ID)
	if err != nil {
		return domain.GitIdentity{}, err
	}
	defer repoLock.Close()
	source, err := registeredSource(ctx, s, g, r)
	if err != nil {
		return domain.GitIdentity{}, err
	}
	scratch, err := s.ScratchDir()
	if err != nil {
		return domain.GitIdentity{}, err
	}
	reference, err := git.CreationReference(step.Workspace.OperationID)
	if err != nil {
		return domain.GitIdentity{}, err
	}
	if step.State == "succeeded" {
		if err := g.FinishCreation(ctx, *step.Identity, reference, scratch); err != nil {
			return domain.GitIdentity{}, err
		}
		return *step.Identity, nil
	}
	// The snapshot's committed blob, not today's source/worktree manifest, is
	// authoritative after interruption. No checkout filters participate here.
	manifest, err := g.Manifest(ctx, r.SourcePath, step.Workspace.HeadOID)
	if err != nil {
		return domain.GitIdentity{}, err
	}
	hash := sha256.Sum256(manifest)
	if hex.EncodeToString(hash[:]) != step.Workspace.ManifestSHA256 {
		return domain.GitIdentity{}, &domain.Error{Code: "E_STATE_INTENT", Message: "stored target manifest hash does not match its pinned Git blob"}
	}
	in := git.AddRequest{Source: source.Identity, Target: git.Target{Branch: step.Workspace.Branch, HeadOID: step.Workspace.HeadOID, NewBranch: step.NewBranch, Manifest: manifest}, Path: step.Workspace.Path, Reference: reference, Scratch: scratch}
	var observed git.Checkout
	if step.State == "pending" {
		if err := locked.StartGit(ctx); err != nil {
			return domain.GitIdentity{}, err
		}
		observed, err = g.Add(ctx, in)
	} else {
		observed, err = g.ObserveCreation(ctx, in)
	}
	if err != nil {
		// Journal cancellation too, without performing another Git mutation.
		// SIGKILL still leaves the already durable inflight intent.
		journalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if journalErr := locked.GitUnknown(journalCtx); journalErr != nil {
			return domain.GitIdentity{}, journalErr
		}
		return domain.GitIdentity{}, err
	}
	if err := locked.RecordGit(ctx, observed.Identity); err != nil {
		return domain.GitIdentity{}, err // leave our Git lock/receipt in place
	}
	if err := g.FinishCreation(ctx, observed.Identity, reference, scratch); err != nil {
		return domain.GitIdentity{}, err // identity persisted; unlock can be reconciled
	}
	return observed.Identity, nil
}

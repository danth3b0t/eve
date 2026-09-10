package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"eve/internal/domain"
	"eve/internal/files"
	"eve/internal/git"
	"eve/internal/ports"
	"eve/internal/private"
	"eve/internal/provider/convex"
	"eve/internal/state"
)

type DoctorCheck struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}
type DoctorResult struct {
	Workspace state.Workspace `json:"-"`
	Checks    []DoctorCheck   `json:"checks"`
}

func doctorCheck(id, status, evidence string) DoctorCheck {
	return DoctorCheck{ID: id, Status: status, Evidence: evidence}
}

func DoctorWorkspace(ctx context.Context, s *state.Store, g *git.Client, workspaceID string) (DoctorResult, error) {
	lock, err := s.LockWorkspace(workspaceID)
	if err != nil {
		return DoctorResult{}, err
	}
	defer lock.Close()
	workspace, err := s.Workspace(ctx, workspaceID)
	if err != nil {
		return DoctorResult{}, err
	}
	result := DoctorResult{Workspace: workspace}
	result.Checks = append(result.Checks, doctorCheck("registry", "pass", "workspace and generation are recorded"))
	identity, err := destroyStepIdentity(ctx, lock)
	if err != nil {
		return result, err
	}
	result, err = doctorGit(ctx, s, g, result, identity, workspace)
	if err != nil {
		return result, err
	}
	result, err = doctorFiles(ctx, s, result, identity, workspace, lock)
	if err != nil {
		return result, err
	}
	result, err = doctorEndpoints(ctx, s, result, workspace)
	if err != nil {
		return result, err
	}
	checks, err := doctorResources(ctx, lock, workspace)
	if err != nil {
		return result, err
	}
	result.Checks = append(result.Checks, checks...)
	result.Checks = append(result.Checks, doctorCheck("runtime", "not_checked", "doctor never launches the project"), doctorCheck("loader", "not_checked", "static inspection cannot prove arbitrary launch behavior"), doctorCheck("provider_remote", "not_checked", "--remote was not requested"))
	return result, nil
}
func destroyStepIdentity(ctx context.Context, lock *state.LockedWorkspace) (domain.GitIdentity, error) {
	step, err := lock.DestroyStep(ctx)
	if err != nil {
		return domain.GitIdentity{}, err
	}
	return step.Identity, nil
}
func doctorGit(ctx context.Context, s *state.Store, g *git.Client, result DoctorResult, identity domain.GitIdentity, workspace state.Workspace) (DoctorResult, error) {
	checkout, err := g.Verify(ctx, identity)
	if err != nil {
		result.Checks = append(result.Checks, doctorCheck("git", "fail", "recorded worktree is missing or invalid"))
		return result, nil
	}
	status := "pass"
	evidence := "canonical Git identity and branch match"
	if err := g.Compatible(ctx, checkout); err != nil {
		status = "fail"
		evidence = "unsupported Git repository features"
	}
	if checkout.Branch != workspace.Branch {
		status = "fail"
		evidence = "recorded branch changed"
	}
	raw, manErr := g.Manifest(ctx, identity.Path, checkout.HeadOID)
	if manErr != nil {
		status = "fail"
		evidence = "target manifest is unavailable"
	} else {
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != workspace.ManifestSHA256 {
			if checkout.HeadOID != workspace.HeadOID {
				if status == "pass" {
					status = "warning"
					evidence = "branch points to a newer manifest revision than creation"
				}
			} else {
				status = "fail"
				evidence = "manifest changed at the recorded revision"
			}
		}
	}
	changes, err := g.Changes(ctx, identity.Path)
	if err != nil {
		status = "fail"
		evidence = "Git change inventory is unavailable"
	} else if len(changes) != 0 && status == "pass" {
		status = "warning"
		evidence = "visible Git changes require review before lifecycle operations"
	}
	result.Checks = append(result.Checks, doctorCheck("git", status, evidence))
	return result, nil
}
func doctorFiles(ctx context.Context, s *state.Store, result DoctorResult, identity domain.GitIdentity, workspace state.Workspace, w *state.LockedWorkspace) (DoctorResult, error) {
	filesRows, err := w.ManagedFiles(ctx)
	if err != nil {
		return result, err
	}
	_, key, err := s.HMACKey(ctx)
	if err != nil {
		return result, err
	}
	allExact := true
	for _, file := range filesRows {
		data, _, err := files.ReadDestination(ctx, identity, file.Path)
		if err != nil {
			result.Checks = append(result.Checks, doctorCheck("files:"+file.Path, "fail", "declared destination is missing or unsafe"))
			allExact = false
			continue
		}
		if !private.Equal(key.File(workspace.ID, file.Path, data), file.HMAC) {
			allExact = false
		}
	}
	status := "pass"
	evidence := "current files match the published generation"
	if !allExact {
		status = "warning"
		evidence = "one or more destinations differ from the published generation; unmanaged edits may be permitted"
	}
	result.Checks = append(result.Checks, doctorCheck("files", status, evidence))
	return result, nil
}
func doctorEndpoints(ctx context.Context, s *state.Store, result DoctorResult, workspace state.Workspace) (DoctorResult, error) {
	allocation, err := s.Allocation(ctx, workspace.ID)
	if err != nil {
		return result, err
	}
	if len(allocation.Endpoints) == 0 {
		result.Checks = append(result.Checks, doctorCheck("endpoints", "pass", "no declared listeners"))
		return result, nil
	}
	listening := 0
	for _, endpoint := range allocation.Endpoints {
		err := ports.ProbeTCP(ctx, endpoint.Port)
		if err != nil {
			var d *domain.Error
			if errors.As(err, &d) && d.Code == "E_PORT_OCCUPIED" {
				listening++
			} else {
				return result, err
			}
		}
	}
	result.Checks = append(result.Checks, doctorCheck("endpoints", "warning", "observed listeners are not proof the expected application owns them; stop the project before lifecycle mutations if needed"))
	if listening == 0 {
		result.Checks[len(result.Checks)-1] = doctorCheck("endpoints", "pass", "all declared ports are unbound")
	}
	return result, nil
}
func doctorResources(ctx context.Context, lock *state.LockedWorkspace, workspace state.Workspace) ([]DoctorCheck, error) {
	resources, err := lock.Resources(ctx)
	if err != nil {
		return nil, err
	}
	if len(resources) == 0 {
		return []DoctorCheck{doctorCheck("resources", "pass", "no cloud resources declared")}, nil
	}
	now := time.Now().UnixMilli()
	checks := make([]DoctorCheck, 0, len(resources))
	for _, resource := range resources {
		status := "pass"
		evidence := "recorded provider identity is configured"
		if resource.State != "configured" || resource.ExpiresAtMS <= now {
			status = "warning"
			evidence = "resource is not fully configured or has expired"
		}
		checks = append(checks, doctorCheck("resources:"+resource.ResourceKey, status, evidence))
	}
	return checks, nil
}

// DoctorRemote performs identity checks only. It does not query environment
// values, list secrets, or use deploy credentials.
func DoctorRemote(ctx context.Context, s *state.Store, lock *state.LockedWorkspace, workspace state.Workspace, factory convexFactory) ([]DoctorCheck, error) {
	resources, err := lock.Resources(ctx)
	if err != nil {
		return nil, err
	}
	if factory == nil {
		factory = defaultConvexFactory
	}
	checks := make([]DoctorCheck, 0, len(resources))
	if len(resources) == 0 {
		return []DoctorCheck{doctorCheck("provider_remote", "not_checked", "workspace has no provider resources")}, nil
	}
	for _, resource := range resources {
		_, token, err := managementCredential(ctx, s, resource.Spec.Profile)
		if err != nil {
			checks = append(checks, doctorCheck("provider_remote:"+resource.ResourceKey, "fail", "management credential unavailable"))
			continue
		}
		api, err := factory(token)
		if err != nil {
			checks = append(checks, doctorCheck("provider_remote:"+resource.ResourceKey, "fail", "provider client unavailable"))
			continue
		}
		project, err := api.ValidateProject(ctx, resource.Spec.Project)
		if err != nil {
			checks = append(checks, doctorCheck("provider_remote:"+resource.ResourceKey, "fail", "project identity unavailable"))
			continue
		}
		observed, err := recordedDeployment(resource)
		if err != nil {
			checks = append(checks, doctorCheck("provider_remote:"+resource.ResourceKey, "fail", "recorded identity invalid"))
			continue
		}
		intent := convex.Intent{ProjectID: project.ID, Reference: resource.RemoteReference, Region: resource.Spec.Region, StartMS: workspace.CreatedAtMS, ExpiresMS: resource.ExpiresAtMS}
		deployment, err := api.Inspect(ctx, intent, observed)
		if err != nil {
			status := "fail"
			evidence := "provider identity unavailable"
			var d *domain.Error
			if errors.As(err, &d) && d.Code == "E_PROVIDER_NOT_FOUND" {
				status = "warning"
				evidence = "recorded remote deployment is absent"
			}
			checks = append(checks, doctorCheck("provider_remote:"+resource.ResourceKey, status, evidence))
			continue
		}
		status := "pass"
		evidence := "remote identity matches recorded reference"
		if deployment.Name != resource.RemoteName || deployment.ExpiresAt != resource.ExpiresAtMS {
			status = "fail"
			evidence = "remote identity or expiry changed"
		} else if deployment.ExpiresAt <= time.Now().UnixMilli() {
			status = "warning"
			evidence = "remote deployment has expired"
		}
		checks = append(checks, doctorCheck("provider_remote:"+resource.ResourceKey, status, evidence))
	}
	return checks, nil
}

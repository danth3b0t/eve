package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"eve/internal/domain"
	"eve/internal/provider/convex"
	"eve/internal/state"
)

func defaultConvexFactory(token string) (convexAdapter, error) {
	api, err := convex.New(token, nil)
	if err != nil {
		return nil, err
	}
	return api, nil
}

func recordedDeployment(r state.Resource) (convex.Deployment, error) {
	id, err := strconv.ParseInt(r.RemoteID, 10, 64)
	if err != nil || id <= 0 || r.RemoteName == "" {
		return convex.Deployment{}, &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "recorded remote identity is incomplete"}
	}
	project, err := strconv.ParseInt(r.RemoteProjectID, 10, 64)
	if err != nil || project <= 0 {
		return convex.Deployment{}, &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "recorded project identity is incomplete"}
	}
	return convex.Deployment{ID: id, Name: r.RemoteName, ProjectID: project, Reference: r.RemoteReference}, nil
}

// destroyResources uses exact immutable registry identity after local safety and
// durable destruction intent. Reverse stable resource order matches creation.
// A remote failure leaves the local worktree and its claims intact for retry.
func destroyResources(ctx context.Context, s *state.Store, w *state.LockedWorkspace, workspace state.Workspace, factory convexFactory) error {
	resources, err := w.Resources(ctx)
	if err != nil {
		return err
	}
	for i := len(resources) - 1; i >= 0; i-- {
		r := resources[i]
		if r.State == "deleted" {
			continue
		}
		if r.State == "planned" {
			if err := w.MarkResource(ctx, r.ID, "deleted"); err != nil {
				return err
			}
			continue
		}
		if r.State != "provisioning" && r.State != "unknown" && r.State != "failed" && r.State != "provisioned" && r.State != "configuring" && r.State != "configured" && r.State != "deleting" && r.State != "cleanup_pending" {
			return &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "resource does not have a reconcileable destruction identity"}
		}
		_, token, err := managementCredential(ctx, s, r.Spec.Profile)
		if err != nil {
			return err
		}
		api, err := factory(token)
		if err != nil {
			return err
		}
		project, err := api.ValidateProject(ctx, r.Spec.Project)
		if err != nil {
			return err
		}
		for kind, name := range map[string]string{"dev": project.Dev, "prod": project.Prod} {
			if name != "" {
				if _, err := api.InspectDefault(ctx, name, kind, project.ID); err != nil {
					return err
				}
			}
		}
		if strconv.FormatInt(project.ID, 10) != r.RemoteProjectID {
			return &domain.Error{Code: "E_PROVIDER_IDENTITY", Message: "recorded project identity changed; remote deletion refused"}
		}
		attemptStart := r.AttemptStartedAtMS
		if attemptStart == 0 {
			attemptStart = workspace.CreatedAtMS
		}
		if r.State == "provisioning" || r.State == "unknown" || r.State == "failed" {
			lookup := convex.Intent{ProjectID: project.ID, Reference: r.RemoteReference, Region: r.Spec.Region, StartMS: attemptStart, ExpiresMS: r.IntendedExpiresAtMS}
			deployment, found, err := api.Lookup(ctx, lookup)
			if err != nil {
				return err
			}
			if !found {
				return &domain.Error{Code: "E_PROVIDER_AMBIGUOUS", Message: "resource creation outcome is not proven; remote deletion refused"}
			}
			r.RemoteID = strconv.FormatInt(deployment.ID, 10)
			r.RemoteName = deployment.Name
			r.RemoteProjectID = strconv.FormatInt(deployment.ProjectID, 10)
			r.ExpiresAtMS = deployment.ExpiresAt
			r.State = "provisioned"
			if err := w.RecordResource(ctx, r); err != nil {
				return err
			}
		}
		observed, err := recordedDeployment(r)
		if err != nil {
			return err
		}
		if err := w.MarkResource(ctx, r.ID, "deleting"); err != nil {
			return err
		}
		intent := convex.Intent{ProjectID: project.ID, Reference: r.RemoteReference, Region: r.Spec.Region, StartMS: attemptStart, ExpiresMS: r.IntendedExpiresAtMS}
		if err := api.Delete(ctx, intent, observed); err != nil {
			var d *domain.Error
			if !(errors.As(err, &d) && d.Code == "E_PROVIDER_NOT_FOUND") {
				if stateErr := w.MarkResource(ctx, r.ID, "cleanup_pending"); stateErr != nil {
					return stateErr
				}
				return &domain.Error{Code: "E_CLEANUP_PENDING", Message: fmt.Sprintf("remote deletion for resource %q is not confirmed; local worktree preserved", r.ResourceKey)}
			}
		}
		if err := w.MarkResource(ctx, r.ID, "deleted"); err != nil {
			return err
		}
	}
	return nil
}

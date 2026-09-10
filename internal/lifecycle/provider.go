package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/envfile"
	"eve/internal/provider/convex"
	"eve/internal/resolve"
	"eve/internal/state"
)

type bindingResult struct {
	outputs map[string]resolve.ResourceOutputs
	secrets map[string]map[string]string
}
type ResourcesPublic struct {
	Resources map[string]resolve.ResourceOutputs
}
type convexAdapter interface {
	ValidateProject(context.Context, string) (convex.Project, error)
	InspectDefault(context.Context, string, string, int64) (convex.Deployment, error)
	Inspect(context.Context, convex.Intent, convex.Deployment) (convex.Deployment, error)
	Lookup(context.Context, convex.Intent) (*convex.Deployment, bool, error)
	Create(context.Context, convex.Intent) (*convex.Deployment, error)
	DeployKeyMetadata(context.Context, convex.Deployment) ([]convex.DeployKeyMetadata, error)
	DeleteDeployKey(context.Context, convex.Deployment, string) error
	CreateDeployKey(context.Context, convex.Deployment, string, int64) (*convex.Key, error)
	VerifyDeployKeyMetadata(context.Context, convex.Deployment, convex.Key, int64) (convex.Key, error)
	CanonicalURLs(context.Context, convex.Deployment, string) (string, string, error)
	Env(context.Context, convex.Deployment, string) (map[string]string, error)
	UpdateEnv(context.Context, convex.Deployment, string, map[string]string) error
	Delete(context.Context, convex.Intent, convex.Deployment) error
}
type convexFactory func(string) (convexAdapter, error)

func (b bindingResult) String() string   { return "lifecycle.bindingResult" }
func (b bindingResult) GoString() string { return b.String() }

func deployKeyName(r state.Resource, generation int) string {
	return fmt.Sprintf("eve-%s-%s-g%d", strings.ReplaceAll(r.ID, "-", ""), r.ResourceKey, generation)
}
func convexIntent(w state.Workspace, r state.Resource) convex.Intent {
	return convex.Intent{ProjectID: projectNumber(r.RemoteProjectID), Reference: r.RemoteReference, Region: r.Spec.Region, StartMS: w.CreatedAtMS, ExpiresMS: r.IntendedExpiresAtMS}
}
func projectNumber(value string) int64 { n, _ := strconv.ParseInt(value, 10, 64); return n }

// ProvisionResources verifies project/defaults identity before any write and
// checkpoints completed public resource identity. It never pushes project code.
func ProvisionResources(ctx context.Context, s *state.Store, w *state.LockedWorkspace, manifestWorkspace state.Workspace) (bindingResult, error) {
	return provisionResources(ctx, s, w, manifestWorkspace, func(token string) (convexAdapter, error) {
		api, err := convex.New(token, nil)
		if err != nil {
			return nil, err
		}
		return api, nil
	})
}
func provisionResources(ctx context.Context, s *state.Store, w *state.LockedWorkspace, manifestWorkspace state.Workspace, factory convexFactory) (bindingResult, error) {
	resources, err := w.Resources(ctx)
	if err != nil {
		return bindingResult{}, err
	}
	type preparedResource struct {
		resource state.Resource
		api      convexAdapter
		deploy   convex.Deployment
		key      string
	}
	result := bindingResult{outputs: map[string]resolve.ResourceOutputs{}, secrets: map[string]map[string]string{}}
	prepared := make([]preparedResource, 0, len(resources))

	// Phase one verifies all declared remote identities, creates/joins exact
	// resources, and collects outputs. It never resolves a whole manifest while
	// one of its resource siblings still lacks outputs.
	for i := range resources {
		r := resources[i]
		if r.Provider != "convex" {
			return result, &domain.Error{Code: "E_PROVIDER_INTENT", Message: "unsupported provider"}
		}
		identity, key, err := managementCredential(ctx, s, r.Spec.Profile)
		if err != nil {
			return result, err
		}
		api, err := factory(key)
		if err != nil {
			return result, err
		}
		project, err := api.ValidateProject(ctx, r.Spec.Project)
		if err != nil {
			return result, err
		}
		if identity.TeamID != 0 && identity.TeamID != project.TeamID || identity.TeamSlug != "" && identity.TeamSlug != project.TeamSlug {
			return result, &domain.Error{Code: "E_PROVIDER_IDENTITY", Message: "credential profile no longer belongs to the manifest's team"}
		}
		for kind, name := range map[string]string{"dev": project.Dev, "prod": project.Prod} {
			if name != "" {
				if _, err := api.InspectDefault(ctx, name, kind, project.ID); err != nil {
					return result, err
				}
			}
		}
		r.RemoteProjectID = strconv.FormatInt(project.ID, 10)
		attemptStart := r.AttemptStartedAtMS
		if r.State == "planned" && attemptStart == 0 {
			attemptStart = time.Now().UnixMilli()
			r.AttemptStartedAtMS = attemptStart
		}
		intent := convex.Intent{ProjectID: project.ID, Reference: r.RemoteReference, Region: r.Spec.Region, StartMS: manifestWorkspace.CreatedAtMS, ExpiresMS: r.IntendedExpiresAtMS}
		if attemptStart != 0 {
			intent.StartMS = attemptStart
		}
		d, err := provisionedDeployment(ctx, api, w, r, intent)
		if err != nil {
			markResourceFailure(w, r, err)
			return result, err
		}
		if r.State == "planned" || r.State == "unknown" || r.State == "provisioning" {
			r.RemoteID = strconv.FormatInt(d.ID, 10)
			r.RemoteName = d.Name
			r.RemoteProjectID = strconv.FormatInt(d.ProjectID, 10)
			r.ExpiresAtMS = d.ExpiresAt
			r.State = "provisioned"
			if err := w.RecordResource(ctx, r); err != nil {
				return result, err
			}
		}
		key, err = deployCredential(ctx, s, api, w, r, *d, intent.ExpiresMS)
		if err != nil {
			markResourceFailure(w, r, err)
			return result, err
		}
		currentResources, err := w.Resources(ctx)
		if err != nil {
			return result, err
		}
		found := false
		for _, updated := range currentResources {
			if updated.ID == r.ID {
				r = updated
				found = true
			}
		}
		if !found {
			return result, &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "recorded resource is missing"}
		}
		cloud, site, err := api.CanonicalURLs(ctx, *d, key)
		if err != nil {
			return result, err
		}
		outputs := resolve.ResourceOutputs{URL: cloud, SiteURL: site, Deployment: "dev:" + d.Name, Name: d.Name, Reference: r.RemoteReference}
		result.outputs[r.ResourceKey] = outputs
		r.Outputs = map[string]string{"cloud_url": cloud, "site_url": site, "deployment": "dev:" + d.Name, "name": d.Name, "reference": r.RemoteReference, "expires_at": time.UnixMilli(d.ExpiresAt).UTC().Format(time.RFC3339)}
		if err := w.RecordResource(ctx, r); err != nil {
			return result, err
		}
		secretPath := state.ResourceEnvFile(r)
		result.secrets[secretPath] = map[string]string{"CONVEX_DEPLOYMENT": "dev:" + d.Name, "CONVEX_DEPLOY_KEY": key}
		prepared = append(prepared, preparedResource{resource: r, api: api, deploy: *d, key: key})
	}

	// Phase two resolves relationships only after every sibling output exists.
	heat, err := s.Allocation(ctx, manifestWorkspace.ID)
	if err != nil {
		return result, err
	}
	resolverInput := localInputs(manifestWorkspace, heat)
	resolverInput.Resources = result.outputs
	resolved, err := resolve.Resolve(&manifestWorkspace.Manifest, resolverInput)
	if err != nil {
		return result, err
	}
	for _, item := range prepared {
		if err := configureResourceEnv(ctx, item.api, item.deploy, item.key, resolved.RemoteEnv[item.resource.ResourceKey]); err != nil {
			return result, err
		}
	}
	return result, nil
}
func configureResourceEnv(ctx context.Context, api convexAdapter, d convex.Deployment, key string, desired map[string]string) error {
	existing, err := api.Env(ctx, d, key)
	if err != nil {
		return err
	}
	changes := map[string]string{}
	for name, value := range desired {
		if !envfile.ValidKey(name) || config.ReservedLocalKey(name) {
			return &domain.Error{Code: "E_PROVIDER_INTENT", Message: "remote environment key is reserved or invalid"}
		}
		if existing[name] != value {
			changes[name] = value
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return api.UpdateEnv(ctx, d, key, changes)
}

func managementCredential(ctx context.Context, s *state.Store, profile string) (state.ManagementProfile, string, error) {
	if profile == "default" {
		if token := os.Getenv("EVE_CONVEX_TOKEN"); token != "" {
			return state.ManagementProfile{}, token, nil
		}
	}
	return s.ManagementToken(ctx, "convex", profile)
}
func provisionedDeployment(ctx context.Context, api convexAdapter, w *state.LockedWorkspace, r state.Resource, in convex.Intent) (*convex.Deployment, error) {
	attemptStart := r.AttemptStartedAtMS
	if attemptStart == 0 {
		attemptStart = in.StartMS // compatibility with v1 registries
	}
	if r.State == "provisioned" || r.State == "configuring" || r.State == "configured" {
		if r.RemoteName == "" || r.RemoteID == "" || r.KeyGeneration < 0 || r.ExpiresAtMS != in.ExpiresMS || attemptStart == 0 {
			return nil, &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "recorded resource identity is incomplete"}
		}
		in.StartMS = attemptStart
		id, _ := strconv.ParseInt(r.RemoteID, 10, 64)
		d, err := api.Inspect(ctx, in, convex.Deployment{ID: id, Name: r.RemoteName, ProjectID: in.ProjectID, Reference: r.RemoteReference})
		if err != nil {
			return nil, err
		}
		return &d, nil
	}
	if r.State == "provisioning" || r.State == "unknown" {
		if attemptStart == 0 {
			return nil, &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "resource attempt window is not recorded"}
		}
		in.StartMS = attemptStart
		d, found, err := api.Lookup(ctx, in)
		if err != nil {
			return nil, err
		}
		if !found {
			if err := w.MarkResource(ctx, r.ID, "unknown"); err != nil {
				return nil, err
			}
			return nil, &domain.Error{Code: "E_PROVIDER_AMBIGUOUS", Message: "resource creation outcome remains unknown; reconcile manually rather than creating another reference"}
		}
		return d, nil
	}
	if r.State != "planned" {
		return nil, &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "resource is not in a retryable provisioning state"}
	}
	if r.AttemptStartedAtMS == 0 {
		r.AttemptStartedAtMS = time.Now().UnixMilli()
	}
	r.State = "provisioning"
	if err := w.RecordResource(ctx, r); err != nil {
		return nil, err
	}
	in.StartMS = r.AttemptStartedAtMS
	d, err := api.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	return d, nil
}
func deployCredential(ctx context.Context, s *state.Store, api convexAdapter, w *state.LockedWorkspace, r state.Resource, d convex.Deployment, expires int64) (string, error) {
	if r.CredentialID != "" {
		if current, err := w.DeployKeyCredential(ctx, r); err == nil {
			name := deployKeyName(r, r.KeyGeneration)
			keys, listErr := api.DeployKeyMetadata(ctx, d)
			if listErr != nil {
				return "", listErr
			}
			matches := 0
			for _, entry := range keys {
				if entry.Name == name {
					matches++
					if entry.ID <= 0 || entry.ExpiresAt != expires {
						return "", &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "recorded deploy key metadata changed"}
					}
				}
			}
			if matches != 1 {
				return "", &domain.Error{Code: "E_PROVIDER_RESOURCE", Message: "recorded deployment credential is no longer exactly visible; repair/rotate explicitly"}
			}
			return current, nil
		}
		previous := deployKeyName(r, r.KeyGeneration)
		keys, err := api.DeployKeyMetadata(ctx, d)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range keys {
			if entry.Name == previous {
				found = true
			}
		}
		if found {
			if err := api.DeleteDeployKey(ctx, d, previous); err != nil {
				return "", err
			}
		}
	}
	credential, generation, err := w.StartDeployKey(ctx, r, deployKeyName(r, r.KeyGeneration+1))
	if err != nil {
		return "", err
	}
	key, err := api.CreateDeployKey(ctx, d, deployKeyName(r, generation), expires)
	if err != nil {
		return "", err
	}
	if _, err := api.VerifyDeployKeyMetadata(ctx, d, *key, expires); err != nil {
		return "", err
	}
	if err := s.StoreDeploymentKey(ctx, credential, key.Value()); err != nil {
		return "", err
	}
	r.CredentialID = credential
	r.KeyGeneration = generation
	r.State = "configured"
	if err := w.RecordResource(ctx, r); err != nil {
		return "", err
	}
	return key.Value(), nil
}
func markResourceFailure(w *state.LockedWorkspace, r state.Resource, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		_ = w.MarkResource(context.Background(), r.ID, "unknown")
		return
	}
	var d *domain.Error
	if errors.As(err, &d) && (d.Code == "E_PROVIDER_AMBIGUOUS" || d.Code == "E_PROVIDER_CONTRACT") {
		_ = w.MarkResource(context.Background(), r.ID, "unknown")
	}
}

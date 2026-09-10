package lifecycle

import (
	"context"
	"os"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/envfile"
	"eve/internal/files"
	"eve/internal/state"
)

var ambientSelectorKeys = []string{
	"CONVEX_DEPLOYMENT",
	"CONVEX_DEPLOY_KEY",
	"CONVEX_DEPLOYMENT_TOKEN",
	"CONVEX_SELF_HOSTED_URL",
	"CONVEX_SELF_HOSTED_ADMIN_KEY",
	"CONVEX_OVERRIDE_ACCESS_TOKEN",
}

// AuditConvexSelectors rejects known conflicting Convex routing inputs before a
// provider write. Values are never compared or exposed; presence is evidence.
func AuditConvexSelectors(ctx context.Context, s *state.Store, lock *state.LockedWorkspace, workspace state.Workspace) error {
	for _, key := range ambientSelectorKeys {
		if os.Getenv(key) != "" {
			return &domain.Error{Code: "E_PROVIDER_SELECTOR_CONFLICT", Message: "inherited Convex selector " + key + " conflicts with the EVE-owned deployment binding; unset it and rerun"}
		}
	}
	repo, err := s.Repository(ctx, workspace.RepositoryID)
	if err != nil {
		return err
	}
	source := domain.GitIdentity{Path: repo.SourcePath, PathIdentity: repo.SourceIdentity, CommonDir: repo.CommonDir, CommonIdentity: repo.CommonIdentity}
	target, targetErr := lock.GitIdentityReceipt(ctx)
	manifest := workspace.AppliedManifest
	if manifest.Version == 0 {
		manifest = workspace.Manifest
	}
	destinations := map[string]bool{}
	resourceFiles := map[string]bool{}
	for _, service := range manifest.Services {
		name, err := config.RelativePath(service.Path, service.EnvFile)
		if err != nil {
			return err
		}
		destinations[name] = true
	}
	for _, resource := range manifest.Resources {
		name, err := config.RelativePath(resource.Path, resource.EnvFile)
		if err != nil {
			return err
		}
		destinations[name] = true
		resourceFiles[name] = true
	}
	for name := range destinations {
		resource := resourceFiles[name]
		if err := auditSelectorFile(ctx, source, name, resource); err != nil {
			return err
		}
		if targetErr == nil {
			data, _, err := files.ReadDestination(ctx, target, name)
			if err != nil {
				return err
			}
			extras, err := selectorViolations(data, resource, name)
			if err != nil || extras != "" {
				return errOrSelectorError(err, "destination", name, extras)
			}
		}
	}
	return nil
}

func auditSelectorFile(ctx context.Context, root domain.GitIdentity, name string, resource bool) error {
	data, _, err := files.ReadDestination(ctx, root, name)
	if err != nil {
		return err
	}
	extras, err := selectorViolations(data, resource, name)
	return errOrSelectorError(err, "source destination", name, extras)
}

func selectorViolations(data []byte, resource bool, name string) (string, error) {
	doc, err := envfile.Parse(data)
	if err != nil {
		return "", &domain.Error{Code: "E_PROVIDER_SELECTOR_CONFLICT", Message: "declared destination cannot be audited for reserved selectors", Path: name}
	}
	for _, key := range doc.Keys() {
		if !selectorAllowed(key, resource) {
			return key, nil
		}
	}
	return "", nil
}

func errOrSelectorError(err error, scope, name, key string) error {
	if err != nil {
		return err
	}
	if key == "" {
		return nil
	}
	return &domain.Error{Code: "E_PROVIDER_SELECTOR_CONFLICT", Message: scope + " contains stale Convex selector " + key, Path: name}
}

func selectorAllowed(key string, resource bool) bool {
	if resource && (key == "CONVEX_DEPLOYMENT" || key == "CONVEX_DEPLOY_KEY") {
		return true
	}
	return !config.ReservedLocalKey(key)
}

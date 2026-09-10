package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"eve/internal/config"
	"eve/internal/files"
	"eve/internal/git"
	"maps"
	"slices"
)

type PlanEndpoint struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	Env     string `json:"env"`
	Host    string `json:"host"`
	Scheme  string `json:"scheme"`
}
type PlanResource struct {
	Key      string `json:"key"`
	Provider string `json:"provider"`
	Project  string `json:"project"`
	Path     string `json:"path"`
	EnvFile  string `json:"env_file"`
	Profile  string `json:"profile"`
	TTL      string `json:"ttl"`
	Region   string `json:"region,omitempty"`
}
type PlanPreview struct {
	Branch         string         `json:"branch"`
	From           string         `json:"from,omitempty"`
	Source         string         `json:"source"`
	HeadOID        string         `json:"head_oid"`
	ManifestSHA256 string         `json:"manifest_sha256"`
	Endpoints      []PlanEndpoint `json:"endpoints"`
	Resources      []PlanResource `json:"resources"`
	Files          files.Report   `json:"files"`
}

func (p PlanPreview) String() string   { data, _ := json.Marshal(p); return string(data) }
func (p PlanPreview) GoString() string { return p.String() }

// PlanPreview is read-only and requires no registered EVE workspace/state. It
// inspects the canonical committed target manifest; prospective state identity
// and port values are deliberately absent. This does not establish loaders or credentials.
func PlanPreviewForBranch(ctx context.Context, g *git.Client, source, branch, from string) (PlanPreview, error) {
	checkout, err := g.Inspect(ctx, source)
	if err != nil {
		return PlanPreview{}, err
	}
	if err := g.Compatible(ctx, checkout); err != nil {
		return PlanPreview{}, err
	}
	target, err := g.Plan(ctx, checkout, branch, from)
	if err != nil {
		return PlanPreview{}, err
	}
	filePlan, err := files.Snapshot(ctx, g, checkout.Identity, target)
	if err != nil {
		return PlanPreview{}, err
	}
	manifest, err := config.Parse(target.Manifest)
	if err != nil {
		return PlanPreview{}, err
	}
	preview := PlanPreview{Branch: branch, From: from, Source: checkout.Identity.Path, HeadOID: target.HeadOID, Files: filePlan.Report()}
	digest := sha256.Sum256(target.Manifest)
	preview.ManifestSHA256 = hex.EncodeToString(digest[:])
	for _, endpoint := range manifest.Endpoints() {
		service := manifest.Services[endpoint.Service]
		preview.Endpoints = append(preview.Endpoints, PlanEndpoint{Service: endpoint.Service, Name: endpoint.Name, Env: endpoint.Env, Host: service.Host, Scheme: service.Scheme})
	}
	for _, id := range slices.Sorted(maps.Keys(manifest.Resources)) {
		resource := manifest.Resources[id]
		preview.Resources = append(preview.Resources, PlanResource{Key: id, Provider: resource.Provider, Project: resource.Project, Path: resource.Path, EnvFile: resource.EnvFile, Profile: resource.CredentialProfile, TTL: resource.TTL, Region: resource.Region})
	}
	return preview, nil
}

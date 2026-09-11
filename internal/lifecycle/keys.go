package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"eve/internal/config"
	"eve/internal/git"
	"maps"
)

type InterpolationVariable struct {
	Variable    string `json:"variable"`
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

type InterpolationPreview struct {
	Source         string                  `json:"source"`
	HeadOID        string                  `json:"head_oid"`
	ManifestSHA256 string                  `json:"manifest_sha256"`
	Keys           []InterpolationVariable `json:"keys"`
}

func scopedInterpolationVariable(source, kind, variable, description string) InterpolationVariable {
	return InterpolationVariable{Source: source, Kind: kind, Variable: "${" + variable + "}", Description: description}
}

// InterpolationPreviewForCheckout exposes only committed manifest-supported
// interpolation keys. It never opens EVE state, contact providers, or reads
// destination dotenv contents.
func InterpolationPreviewForCheckout(ctx context.Context, g *git.Client, root string) (InterpolationPreview, error) {
	checkout, err := g.Inspect(ctx, root)
	if err != nil {
		return InterpolationPreview{}, err
	}
	if err := g.Compatible(ctx, checkout); err != nil {
		return InterpolationPreview{}, err
	}
	data, err := g.Manifest(ctx, checkout.Identity.Path, checkout.HeadOID)
	if err != nil {
		return InterpolationPreview{}, err
	}
	manifest, err := config.Parse(data)
	if err != nil {
		return InterpolationPreview{}, err
	}

	preview := InterpolationPreview{Source: checkout.Identity.Path, HeadOID: checkout.HeadOID}
	digest := sha256.Sum256(data)
	preview.ManifestSHA256 = hex.EncodeToString(digest[:])

	base := []InterpolationVariable{
		scopedInterpolationVariable("workspace", "public", "workspace.id", "immutable EVE workspace UUID"),
		scopedInterpolationVariable("workspace", "public", "workspace.slug", "path-derived workspace slug"),
		scopedInterpolationVariable("workspace", "public", "workspace.branch", "reviewed target branch"),
	}
	preview.Keys = append(preview.Keys, base...)
	if len(manifest.Endpoints()) != 0 {
		preview.Keys = append(preview.Keys, scopedInterpolationVariable("workspace", "allocation", "workspace.port_base", "first allocated endpoint port in this workspace block"))
	}
	for _, id := range slices.Sorted(maps.Keys(manifest.Services)) {
		service := manifest.Services[id]
		source := "services." + id
		if service.Port != "" {
			preview.Keys = append(preview.Keys,
				scopedInterpolationVariable(source, "allocation", "services."+id+".port", "allocated primary endpoint port"),
				scopedInterpolationVariable(source, "allocation", "services."+id+".url", "scheme/host/allocated primary endpoint URL"),
			)
		}
		for _, endpoint := range slices.Sorted(maps.Keys(service.Ports)) {
			preview.Keys = append(preview.Keys, scopedInterpolationVariable(source, "allocation", fmt.Sprintf("services.%s.ports.%s.port", id, endpoint), "allocated additional endpoint port"))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(manifest.Resources)) {
		resource := manifest.Resources[id]
		if resource.Provider != "convex" {
			continue
		}
		source := "resources." + id
		preview.Keys = append(preview.Keys,
			scopedInterpolationVariable(source, "provider-output", "resources."+id+".url", "Convex deployment client URL"),
			scopedInterpolationVariable(source, "provider-output", "resources."+id+".site_url", "Convex deployment HTTP Actions URL"),
			scopedInterpolationVariable(source, "provider-output", "resources."+id+".deployment", "Convex deployment selector; use only where the application requires it"),
			scopedInterpolationVariable(source, "provider-output", "resources."+id+".name", "Convex deployment resource name"),
			scopedInterpolationVariable(source, "provider-output", "resources."+id+".reference", "EVE ownership reference for this created deployment"),
		)
	}
	return preview, nil
}

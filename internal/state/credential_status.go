package state

import (
	"context"
)

// ManagementProfiles returns nonsecret credential metadata only. Token objects
// and checksums are outside this read model.
func (s *Store) ManagementProfiles(ctx context.Context, provider string) ([]ManagementProfile, error) {
	if provider != "convex" {
		return nil, failure("E_CREDENTIAL_PROFILE", "unsupported credential provider")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider,name,team_id,team_slug,credential_id,last_validated_at_ms FROM credential_profiles WHERE provider=? ORDER BY name`, provider)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var profiles []ManagementProfile
	for rows.Next() {
		var profile ManagementProfile
		if err := rows.Scan(&profile.Provider, &profile.Name, &profile.TeamID, &profile.TeamSlug, &profile.CredentialID, &profile.LastValidatedAtMS); err != nil {
			return nil, dbError(err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	if len(profiles) == 0 {
		return nil, failure("E_PROVIDER_AUTH", "management credential profile is not configured")
	}
	return profiles, nil
}

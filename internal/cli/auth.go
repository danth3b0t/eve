package cli

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"eve/internal/domain"
	"eve/internal/provider/convex"
)

var profileName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

type authProjectValidator func(context.Context, string, string) (convex.Project, error)

func defaultAuthValidator(ctx context.Context, token, project string) (convex.Project, error) {
	api, err := convex.New(token, nil)
	if err != nil {
		return convex.Project{}, err
	}
	return api.ValidateProject(ctx, project)
}

// authLogin receives typed, parsed options from the Cobra adapter. The profile
// default is registered once in cobra_flags.go and is never reparsed here.
func authLogin(ctx context.Context, opts *commandOptions, args []string, validate authProjectValidator) (*output, error) {
	if len(args) != 0 || opts.Project == "" || !profileName.MatchString(opts.Profile) {
		return nil, &domain.Error{Code: "E_USAGE", Message: "auth login requires --project and a valid profile name"}
	}
	token, err := readTeamToken(ctx, opts.TokenStdin)
	if err != nil {
		return nil, err
	}
	identity, err := validate(ctx, token, opts.Project)
	if err != nil {
		return nil, err
	}
	_, s, err := globalStore(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	returned, err := s.StoreManagementProfile(ctx, "convex", opts.Profile, identity.TeamSlug, identity.TeamID, token, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if returned.TeamSlug != identity.TeamSlug || returned.TeamID != identity.TeamID {
		return nil, &domain.Error{Code: "E_CREDENTIAL_PROFILE", Message: "recorded credential profile identity changed"}
	}
	response := &output{SchemaVersion: 1, Command: "auth convex login", OK: true, Auth: map[string]string{"provider": "convex", "profile": opts.Profile, "team_slug": identity.TeamSlug, "team_id": strconv.FormatInt(identity.TeamID, 10), "validated_at": time.UnixMilli(returned.LastValidatedAtMS).UTC().Format(time.RFC3339)}}
	response.Warnings = []string{"Credential metadata is stored as an owner-only plaintext object; filesystem permissions are not encryption and do not protect against this OS user or root."}
	response.Human = fmt.Sprintf("stored Convex profile %s for team %s\nproject %s validated; default dev deployment exists\nwarning: owner-only plaintext object; not encrypted\n", strconv.Quote(opts.Profile), strconv.Quote(identity.TeamSlug), strconv.Quote(opts.Project))
	return response, nil
}

func authStatus(ctx context.Context, opts *commandOptions, args []string) (*output, error) {
	if len(args) != 0 || !profileName.MatchString(opts.Profile) {
		return nil, &domain.Error{Code: "E_USAGE", Message: "authorization status requires a valid profile name"}
	}
	_, s, err := globalStore(ctx, false)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	profiles, err := s.ManagementProfiles(ctx, "convex")
	if err != nil {
		return nil, err
	}
	for _, current := range profiles {
		if current.Name != opts.Profile {
			continue
		}
		response := &output{SchemaVersion: 1, Command: "auth convex status", OK: true, Auth: map[string]string{"provider": current.Provider, "profile": current.Name, "team_slug": current.TeamSlug, "team_id": strconv.FormatInt(current.TeamID, 10), "validated_at": time.UnixMilli(current.LastValidatedAtMS).UTC().Format(time.RFC3339)}}
		response.Warnings = []string{"Credential metadata only is shown; token values and provider liveness are not inspected."}
		response.Human = fmt.Sprintf("Convex profile %s: team %s; validated %s\n", strconv.Quote(current.Name), strconv.Quote(current.TeamSlug), time.UnixMilli(current.LastValidatedAtMS).UTC().Format(time.RFC3339))
		return response, nil
	}
	return nil, &domain.Error{Code: "E_PROVIDER_AUTH", Message: "management credential profile is not configured"}
}

func authLogout(ctx context.Context, opts *commandOptions, args []string) (*output, error) {
	if len(args) != 0 || !profileName.MatchString(opts.Profile) {
		return nil, &domain.Error{Code: "E_USAGE", Message: "authorization logout requires a valid profile name"}
	}
	_, s, err := globalStore(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	removed, err := s.DeleteManagementProfile(ctx, "convex", opts.Profile)
	if err != nil {
		return nil, err
	}
	response := &output{SchemaVersion: 1, Command: "auth convex logout", OK: true, Auth: map[string]string{"provider": "convex", "profile": opts.Profile}}
	if removed.References != 0 {
		response.Warnings = []string{strconv.Itoa(removed.References) + " live resource record(s) referenced this credential profile; remote resources were not deleted, review them with list/status/doctor before deleting EVE state"}
	} else {
		response.Warnings = []string{"No live resource records reference this profile locally; remote identity was not changed."}
	}
	response.Human = fmt.Sprintf("removed Convex profile %s\nremote resources were not deleted\n", strconv.Quote(opts.Profile))
	return response, nil
}

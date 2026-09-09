package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"eve/internal/domain"
	"eve/internal/provider/convex"
)

var profileName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

func auth(ctx context.Context, args []string) (*output, error) {
	if len(args) != 2 || args[0] != "convex" || args[1] != "login" {
		return nil, &domain.Error{Code: "E_USAGE", Message: "usage: eve auth convex login --project team:slug --token-stdin [--profile name]"}
	}
	fs, _ := newFlags("auth")
	profile := fs.String("profile", "default", "credential profile name")
	stdin := fs.Bool("token-stdin", false, "read exactly one team token only from standard input")
	project := fs.String("project", "", "explicit team:project binding to validate")
	if err := fs.Parse(args[2:]); err != nil {
		return nil, &domain.Error{Code: "E_USAGE", Message: "invalid auth options"}
	}
	if fs.NArg() != 0 || !*stdin || !profileName.MatchString(*profile) || *project == "" {
		return nil, &domain.Error{Code: "E_USAGE", Message: "auth login requires --project, --token-stdin, and a valid profile name"}
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<10+1))
	if err != nil {
		return nil, &domain.Error{Code: "E_CREDENTIAL_PROFILE", Message: "credential input could not be read safely"}
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return nil, &domain.Error{Code: "E_PROVIDER_AUTH", Message: "team token input is missing or invalid"}
	}
	api, err := convex.New(token, nil)
	if err != nil {
		return nil, err
	}
	identity, err := api.ValidateProject(ctx, *project)
	if err != nil {
		return nil, err
	}
	_, s, err := storeFor(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	returned, err := s.StoreManagementProfile(ctx, "convex", *profile, identity.TeamSlug, identity.TeamID, token, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if returned.TeamSlug != identity.TeamSlug || returned.TeamID != identity.TeamID {
		return nil, &domain.Error{Code: "E_CREDENTIAL_PROFILE", Message: "recorded credential profile identity changed"}
	}
	response := &output{SchemaVersion: 1, Command: "auth convex login", OK: true, Auth: map[string]string{"provider": "convex", "profile": *profile, "team_slug": identity.TeamSlug, "team_id": strconv.FormatInt(identity.TeamID, 10), "validated_at": time.UnixMilli(returned.LastValidatedAtMS).UTC().Format(time.RFC3339)}}
	response.Warnings = []string{"Credential metadata is stored as an owner-only plaintext object; filesystem permissions are not encryption and do not protect against this OS user or root."}
	response.Human = fmt.Sprintf("stored Convex profile %s for team %s\nproject %s validated; default dev deployment exists\nwarning: owner-only plaintext object; not encrypted\n", strconv.Quote(*profile), strconv.Quote(identity.TeamSlug), strconv.Quote(*project))
	return response, nil
}

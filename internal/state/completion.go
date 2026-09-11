package state

import (
	"context"
	"strings"
)

// CompletionWorkspace is one bounded, nonsecret registry row used by shell
// candidate selection. It intentionally omits manifests and credential data.
type CompletionWorkspace struct {
	ID, RepositoryID, Branch, Path, State, Phase string
	RepositoryLabel                              string
	Generation                                   int
	OperationCommand                             string
}

func completionLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value + "%"
}

// CompletionWorkspaces loads at most limit rows after SQL-side prefix filtering.
// global=true is reserved for GC's exact-UUID selection and never widens other
// lifecycle resolver semantics.
func (s *Store) CompletionWorkspaces(ctx context.Context, repositoryID string, prefix string, global bool, limit int) ([]CompletionWorkspace, error) {
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	where := `w.state <> 'destroyed'`
	args := []any{}
	if !global {
		where += ` AND w.repository_id=?`
		args = append(args, repositoryID)
	}
	if prefix != "" {
		where += ` AND (w.branch LIKE ? ESCAPE '\' OR w.id LIKE ? ESCAPE '\' OR w.path LIKE ? ESCAPE '\')`
		like := completionLike(prefix)
		args = append(args, like, like, like)
	}
	limitArg := limit + 1
	args = append(args, limitArg)
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.repository_id,w.branch,w.path,w.state,w.phase,w.generation,r.label,coalesce((SELECT o.command FROM operations o WHERE o.workspace_id=w.id AND o.state NOT IN ('succeeded','cancelled') ORDER BY o.updated_at_ms DESC LIMIT 1),'') FROM workspaces w JOIN repositories r ON r.id=w.repository_id WHERE `+where+` ORDER BY w.branch,w.id LIMIT ?`, args...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := make([]CompletionWorkspace, 0, limit)
	for rows.Next() {
		var row CompletionWorkspace
		if err := rows.Scan(&row.ID, &row.RepositoryID, &row.Branch, &row.Path, &row.State, &row.Phase, &row.Generation, &row.RepositoryLabel, &row.OperationCommand); err != nil {
			return nil, dbError(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	return out, nil
}

// CompletionProjects returns bounded recorded public project bindings only.
func (s *Store) CompletionProjects(ctx context.Context, limit int) ([]string, error) {
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT json_extract(spec_json,'$.project') FROM resources WHERE json_extract(spec_json,'$.project')<>'' ORDER BY 1 LIMIT ?`, limit+1)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			return nil, dbError(err)
		}
		out = append(out, project)
	}
	return out, dbError(rows.Err())
}

package state

import "context"

// Repositories lists registered canonical sources. Listing alone never proves
// current Git ownership; lifecycle operations recheck identities again.
func (s *Store) Repositories(ctx context.Context) ([]Repository, error) {
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,common_dir,source_path,common_identity,source_identity,label FROM repositories ORDER BY label,created_at_ms`)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var out []Repository
	for rows.Next() {
		var r Repository
		if err := rows.Scan(&r.ID, &r.CommonDir, &r.SourcePath, &r.CommonIdentity, &r.SourceIdentity, &r.Label); err != nil {
			return nil, dbError(err)
		}
		out = append(out, r)
	}
	return out, dbError(rows.Err())
}
func (s *Store) ListWorkspaces(ctx context.Context, repositoryID string, includeDestroyed bool) ([]Workspace, error) {
	if err := s.checkStorage(); err != nil {
		return nil, err
	}
	condition := `repository_id=?`
	if !includeDestroyed {
		condition += ` AND state<>'destroyed'`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM workspaces WHERE `+condition+` ORDER BY created_at_ms,branch`, repositoryID)
	if err != nil {
		return nil, dbError(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	out := make([]Workspace, 0, len(ids))
	for _, id := range ids {
		workspace, err := s.Workspace(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, workspace)
	}
	return out, nil
}

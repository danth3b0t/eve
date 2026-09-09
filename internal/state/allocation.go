package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type Endpoint struct {
	Service, Name, Env, Host, Scheme string
	Slot, Port                       int
}

type Allocation struct {
	Base, Size int
	Endpoints  []Endpoint
	Ready      bool // reservation accepted; NOT workspace preparation or a socket lease
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readAllocation(ctx context.Context, q queryer, id string) (Allocation, error) {
	var a Allocation
	// One statement gives readers a consistent snapshot during candidate
	// replacement/finalization and distinguishes no listeners from no workspace.
	rows, err := q.QueryContext(ctx, `SELECT coalesce(b.base,0),coalesce(b.size,0),
		EXISTS(SELECT 1 FROM operations o JOIN operation_steps s ON s.operation_id=o.id WHERE o.workspace_id=w.id AND o.command='create' AND s.action='reserve_ports' AND s.state='succeeded'),
		e.service_id,e.name,e.env_key,e.host,e.scheme,e.slot,e.port
		FROM workspaces w LEFT JOIN port_blocks b ON b.workspace_id=w.id LEFT JOIN endpoints e ON e.workspace_id=w.id WHERE w.id=? ORDER BY e.slot`, id)
	if err != nil {
		return a, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		found = true
		var service, name, env, host, scheme sql.NullString
		var slot, port sql.NullInt64
		if err := rows.Scan(&a.Base, &a.Size, &a.Ready, &service, &name, &env, &host, &scheme, &slot, &port); err != nil {
			return a, err
		}
		if service.Valid {
			a.Endpoints = append(a.Endpoints, Endpoint{service.String, name.String, env.String, host.String, scheme.String, int(slot.Int64), int(port.Int64)})
		}
	}
	if err := rows.Err(); err != nil {
		return a, err
	}
	if !found {
		return a, failure("E_WORKSPACE_NOT_FOUND", "workspace is not recorded in this registry")
	}
	return a, nil
}

func (s *Store) Allocation(ctx context.Context, id string) (Allocation, error) {
	if err := s.checkStorage(); err != nil {
		return Allocation{}, err
	}
	a, err := readAllocation(ctx, s.db, id)
	return a, dbError(err)
}

// reservationIntent only permits allocation changes before worktree creation.
// There is intentionally no generic "release ports on failure" method.
func reservationIntent(ctx context.Context, tx *sql.Tx, id string) (Workspace, createIntent, error) {
	w, err := scanWorkspace(tx.QueryRowContext(ctx, workspaceQuery, id))
	var p createIntent
	if err != nil {
		return w, p, err
	}
	if w.State != "creating" || w.Phase != "reserve" {
		return w, p, failure("E_ALLOCATION_FROZEN", "port claims cannot be changed after the reservation phase")
	}
	var intent string
	var untouched bool
	err = tx.QueryRowContext(ctx, `SELECT o.intent_json,w.git_admin_dir IS NULL FROM operations o JOIN workspaces w ON w.id=o.workspace_id WHERE o.id=? AND o.command='create' AND o.phase='reserve' AND o.state IN ('pending','inflight')`, w.OperationID).Scan(&intent, &untouched)
	if ctx.Err() != nil {
		return w, p, ctx.Err()
	}
	if err != nil || !untouched {
		return w, p, failure("E_ALLOCATION_FROZEN", "no untouched creation intent authorizes reservation changes")
	}
	if json.Unmarshal([]byte(intent), &p) != nil || p.Min < 1 || p.Max > 65535 || p.Min > p.Max || p.Size < 1 || p.Size > 1000 || p.Size != w.Manifest.Workspace.PortBlockSize || len(w.Manifest.Endpoints()) > p.Size {
		return w, p, failure("E_STATE_INTENT", "stored allocation intent is invalid")
	}
	return w, p, nil
}

// Candidate reserves every port transactionally before probing. An interrupted
// candidate is returned unchanged for re-probing, never adopted as ready.
// minimum only advances this attempt's search; user range/size come from the
// frozen intent, not mutable caller config. Zero starts at the recorded minimum.
func (w *LockedWorkspace) Candidate(ctx context.Context, minimum int) (Allocation, error) {
	var a Allocation
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			var err error
			a, err = readAllocation(ctx, tx, w.id)
			if err != nil || a.Ready {
				return err
			}
			workspace, p, err := reservationIntent(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if a.Size != 0 || len(workspace.Manifest.Endpoints()) == 0 {
				return nil
			}
			base := max(minimum, p.Min)
			rows, err := tx.QueryContext(ctx, `SELECT port FROM port_claims WHERE port>=? AND port<=? ORDER BY port`, base, p.Max)
			if err != nil {
				return err
			}
			for rows.Next() {
				var port int
				if err := rows.Scan(&port); err != nil {
					rows.Close()
					return err
				}
				if port >= base+p.Size {
					break
				}
				base = port + 1
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if base > p.Max-p.Size+1 {
				return failure("E_PORT_EXHAUSTED", "no unclaimed block fits the configured user port range")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO port_blocks(workspace_id,base,size) VALUES(?,?,?)`, w.id, base, p.Size); err != nil {
				return err
			}
			now := time.Now().UnixMilli()
			stmt, err := tx.PrepareContext(ctx, `INSERT INTO port_claims(port,workspace_id,claimed_at_ms) VALUES(?,?,?)`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for port := base; port < base+p.Size; port++ {
				if _, err := stmt.ExecContext(ctx, port, w.id, now); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operations SET state='inflight',updated_at_ms=? WHERE id=?`, now, workspace.OperationID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='inflight',started_at_ms=coalesce(started_at_ms,?) WHERE operation_id=? AND action='reserve_ports'`, now, workspace.OperationID); err != nil {
				return err
			}
			a.Base, a.Size = base, p.Size
			return nil
		})
	})
	if err != nil {
		return Allocation{}, err
	}
	return a, nil
}

// RejectCandidate is only for an observed bind conflict before any endpoint is
// finalized. Unexpected errors/cancellation leave claims available for resume.
func (w *LockedWorkspace) RejectCandidate(ctx context.Context, expectedBase int) error {
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			if _, _, err := reservationIntent(ctx, tx, w.id); err != nil {
				return err
			}
			a, err := readAllocation(ctx, tx, w.id)
			if err != nil {
				return err
			}
			if a.Ready || len(a.Endpoints) != 0 || a.Base != expectedBase || a.Size == 0 {
				return failure("E_ALLOCATION_FROZEN", "candidate identity changed or endpoints were already finalized")
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM port_claims WHERE workspace_id=?`, w.id); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM port_blocks WHERE workspace_id=?`, w.id)
			return err
		})
	})
}

// AcceptCandidate is called only after every port was exclusively probed. The
// accepted block/slots and next phase commit together. The probe itself must
// NOT run inside this transaction. A zero base accepts a no-listener manifest.
func (w *LockedWorkspace) AcceptCandidate(ctx context.Context, expectedBase int) (Allocation, error) {
	var a Allocation
	err := w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			workspace, p, err := reservationIntent(ctx, tx, w.id)
			if err != nil {
				return err
			}
			a, err = readAllocation(ctx, tx, w.id)
			if err != nil {
				return err
			}
			eps := workspace.Manifest.Endpoints()
			var claims int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM port_claims WHERE workspace_id=?`, w.id).Scan(&claims); err != nil {
				return err
			}
			if a.Ready || len(a.Endpoints) != 0 || a.Base != expectedBase || claims != a.Size || (len(eps) == 0 && a.Size != 0) || (len(eps) != 0 && (a.Size != p.Size || a.Base < p.Min || a.Base > p.Max-p.Size+1)) {
				return failure("E_ALLOCATION_FROZEN", "candidate does not match the frozen allocation intent")
			}
			for slot, ep := range eps {
				service := workspace.Manifest.Services[ep.Service]
				e := Endpoint{Service: ep.Service, Name: ep.Name, Env: ep.Env, Host: service.Host, Scheme: service.Scheme, Slot: slot, Port: a.Base + slot}
				if _, err := tx.ExecContext(ctx, `INSERT INTO endpoints(workspace_id,service_id,name,slot,port,env_key,host,scheme) VALUES(?,?,?,?,?,?,?,?)`, w.id, e.Service, e.Name, e.Slot, e.Port, e.Env, e.Host, e.Scheme); err != nil {
					return err
				}
				a.Endpoints = append(a.Endpoints, e)
			}
			now := time.Now().UnixMilli()
			a.Ready = true
			metadata, _ := json.Marshal(a) // public allocation metadata only
			if _, err := tx.ExecContext(ctx, `UPDATE operation_steps SET state='succeeded',outcome_metadata_json=?,finished_at_ms=? WHERE operation_id=? AND action='reserve_ports'`, string(metadata), now, workspace.OperationID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE operations SET phase='worktree',state='inflight',updated_at_ms=? WHERE id=?`, now, workspace.OperationID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET phase='worktree',updated_at_ms=? WHERE id=?`, now, w.id)
			return err
		})
	})
	if err != nil {
		return Allocation{}, err
	}
	return a, nil
}

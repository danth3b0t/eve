package state

import (
	"context"
	"database/sql"

	"eve/internal/config"
	"eve/internal/envfile"
)

// AppendSyncEndpoints persists a newly appended endpoint atomically. The whole
// block was already claimed at creation; lifecycle probes its port before this.
func (w *LockedWorkspace) AppendSyncEndpoints(ctx context.Context, endpoints []Endpoint) error {
	if len(endpoints) == 0 {
		return failure("E_ALLOCATION", "no endpoint additions supplied")
	}
	return w.withLock(func() error {
		return w.store.transaction(ctx, func(tx *sql.Tx) error {
			var base, size, generation int
			var state string
			if err := tx.QueryRowContext(ctx, `SELECT coalesce(b.base,0),coalesce(b.size,0),w.state,w.generation FROM workspaces w LEFT JOIN port_blocks b ON b.workspace_id=w.id WHERE w.id=?`, w.id).Scan(&base, &size, &state, &generation); err != nil {
				return err
			}
			if state != "prepared" || generation < 1 {
				return failure("E_SYNC_STATE", "endpoint additions require a prepared workspace")
			}
			for _, endpoint := range endpoints {
				if endpoint.Service == "" || endpoint.Name == "" || endpoint.Name == "primary" || !envfile.ValidKey(endpoint.Env) || config.ReservedLocalKey(endpoint.Env) || endpoint.Slot < 0 || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Port <= 0 || endpoint.Port > 65535 {
					return failure("E_SYNC_ENDPOINT", "invalid additive endpoint metadata")
				}
				if base == 0 || endpoint.Slot < 0 || endpoint.Slot >= size || endpoint.Port != base+endpoint.Slot {
					return failure("E_PORT_EXHAUSTED", "endpoint addition exceeds the frozen allocation block")
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO endpoints(workspace_id,service_id,name,slot,port,env_key,host,scheme) VALUES(?,?,?,?,?,?,?,?)`, w.id, endpoint.Service, endpoint.Name, endpoint.Slot, endpoint.Port, endpoint.Env, endpoint.Host, endpoint.Scheme); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

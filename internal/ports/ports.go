// Package ports coordinates durable per-user claims with transient TCP probes.
// Reservations are not socket leases and never establish runtime health.
package ports

import (
	"context"
	"errors"

	"eve/internal/domain"
	"eve/internal/state"
)

type Prober func(context.Context, int) error

// Reserve uses the workspace's frozen intent. Keep the workspace lock for the
// surrounding lifecycle operation. Only definitive occupied-port results discard
// a pre-Git candidate; cancellation/other errors retain intent and claims. A
// resumed incomplete candidate is re-probed; finalized allocations never move.
// A nil prober selects the real exclusive IPv4/IPv6 TCP probe.
func Reserve(ctx context.Context, w *state.LockedWorkspace, probe Prober) (state.Allocation, error) {
	if probe == nil {
		probe = ProbeTCP
	}
	minimum := 0
	for {
		if err := ctx.Err(); err != nil {
			return state.Allocation{}, err
		}
		a, err := w.Candidate(ctx, minimum)
		if err != nil || a.Ready {
			return a, err
		}
		occupied := 0
		for port := a.Base; port < a.Base+a.Size; port++ {
			if err := ctx.Err(); err != nil {
				return state.Allocation{}, err
			}
			if err := probe(ctx, port); err != nil {
				var d *domain.Error
				if errors.As(err, &d) && d.Code == "E_PORT_OCCUPIED" {
					occupied = port
					break
				}
				return state.Allocation{}, err
			}
		}
		if occupied == 0 {
			return w.AcceptCandidate(ctx, a.Base)
		}
		if err := w.RejectCandidate(ctx, a.Base); err != nil {
			return state.Allocation{}, err
		}
		minimum = occupied + 1
	}
}

// CheckEndpoints is required immediately before final configuration publication.
// Conflicts are reported, not fixed by reallocating or killing a listener.
func CheckEndpoints(ctx context.Context, a state.Allocation, probe Prober) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !a.Ready {
		return &domain.Error{Code: "E_PORT_ALLOCATION", Message: "allocation has not completed its initial probes"}
	}
	if probe == nil {
		probe = ProbeTCP
	}
	for _, e := range a.Endpoints {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := probe(ctx, e.Port); err != nil {
			return err
		}
	}
	return nil
}

// CheckStopped observes every declared endpoint before destruction. A port
// listener is not identified as the project's process; it only blocks release.
func CheckStopped(ctx context.Context, a state.Allocation, probe Prober, assumeStopped bool) error {
	if probe == nil {
		probe = ProbeTCP
	}
	for _, e := range a.Endpoints {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := probe(ctx, e.Port)
		if err == nil {
			continue
		}
		var d *domain.Error
		if errors.As(err, &d) && d.Code == "E_PORT_OCCUPIED" && !assumeStopped {
			return &domain.Error{Code: "E_POSSIBLY_RUNNING", Message: "an allocated endpoint still has a listener; stop the ordinary project launcher or confirm that you assessed it", Port: e.Port}
		}
		if err != nil && !(assumeStopped && errors.As(err, &d) && d.Code == "E_PORT_OCCUPIED") {
			return err
		}
	}
	return nil
}

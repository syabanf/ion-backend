// Package network adapts the network bounded context's coverage capability
// to the CRM port.CoverageGateway. Today this is an in-process call; when
// network ships as its own service, replace this with an HTTP client adapter
// — the port stays the same.
package network

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	networkdomain "github.com/ion-core/backend/internal/network/domain"
	networkport "github.com/ion-core/backend/internal/network/port"
)

// NetworkService is the subset of network.usecase.Service we need.
// Declared as an interface here so we don't import the concrete usecase.
type NetworkService interface {
	CheckCoverage(ctx context.Context, in networkport.CoverageCheckInput) (*networkdomain.CoverageResult, error)
	GetNode(ctx context.Context, id uuid.UUID) (*networkport.NodeListItem, error)
}

type CoverageGateway struct {
	svc NetworkService
}

func NewCoverageGateway(svc NetworkService) *CoverageGateway {
	return &CoverageGateway{svc: svc}
}

var _ port.CoverageGateway = (*CoverageGateway)(nil)

// Check runs a coverage decision and projects it down to what CRM needs.
// On covered/excess we additionally fetch the best-candidate node to pick
// up its branch_id — the CRM service uses that to scope the lead.
func (g *CoverageGateway) Check(ctx context.Context, lat, lng float64) (*port.CoverageDecision, error) {
	res, err := g.svc.CheckCoverage(ctx, networkport.CoverageCheckInput{
		Lat: lat, Lng: lng,
		OnlyAvailable: false,
		MaxCandidates: 3,
	})
	if err != nil {
		return nil, err
	}

	// Persist the full coverage result as opaque jsonb. We don't try to keep
	// this in sync with the wire format used by /api/network/coverage/check —
	// it's a snapshot for audit, not for the UI to re-render.
	snapshot, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("marshal coverage snapshot: %w", err)
	}

	out := &port.CoverageDecision{
		Verdict:  domain.CoverageVerdict(string(res.Verdict)),
		Snapshot: snapshot,
	}

	if res.BestCandidate != nil {
		nid := res.BestCandidate.NodeID
		out.NearestNodeID = &nid

		cable := res.BestCandidate.CableDistanceM
		out.CableDistanceM = &cable
		excess := res.BestCandidate.ExcessCharge
		out.ExcessCharge = &excess

		// Try to enrich with branch_id from the node. Failure here is non-fatal —
		// if the node lookup fails (network split, race), the lead still gets a
		// verdict and snapshot; the branch is just unset.
		if node, err := g.svc.GetNode(ctx, nid); err == nil && node != nil && node.Node.BranchID != nil {
			b := *node.Node.BranchID
			out.BranchID = &b
		}
	}

	return out, nil
}

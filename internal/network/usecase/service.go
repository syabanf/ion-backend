// Package usecase implements network application services.
//
// Same conventions as internal/identity/usecase: each method is one use
// case, orchestrates domain calls + driven ports, never touches HTTP/SQL.
package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/network/domain"
	"github.com/ion-core/backend/internal/network/port"
	derrors "github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/platformconfig"
)

type Service struct {
	nodeTypes port.NodeTypeRepository
	nodes     port.NodeRepository
	ports     port.PortRepository
	coverage  port.CoverageRepository
	impact    port.ImpactRepository
	radius    port.RadiusClient // available for the onboarding flow once CRM lands
	config    *platformconfig.Reader
	log       *slog.Logger
}

func NewService(
	nodeTypes port.NodeTypeRepository,
	nodes port.NodeRepository,
	ports port.PortRepository,
	coverage port.CoverageRepository,
	impact port.ImpactRepository,
	radius port.RadiusClient,
	config *platformconfig.Reader,
	log *slog.Logger,
) *Service {
	return &Service{
		nodeTypes: nodeTypes,
		nodes:     nodes,
		ports:     ports,
		coverage:  coverage,
		impact:    impact,
		radius:    radius,
		config:    config,
		log:       log,
	}
}

var _ port.UseCase = (*Service)(nil)

// =====================================================================
// Node types
// =====================================================================

func (s *Service) ListNodeTypes(ctx context.Context, includeInactive bool) ([]domain.NodeType, error) {
	return s.nodeTypes.List(ctx, includeInactive)
}

func (s *Service) GetNodeType(ctx context.Context, id uuid.UUID) (*domain.NodeType, error) {
	return s.nodeTypes.FindByID(ctx, id)
}

func (s *Service) CreateNodeType(ctx context.Context, in port.CreateNodeTypeInput) (*domain.NodeType, error) {
	if existing, err := s.nodeTypes.FindByKey(ctx, in.TypeKey); err == nil && existing != nil {
		return nil, derrors.Conflict("node_type.key_taken", "type_key already in use")
	}
	t, err := domain.NewNodeType(in.TypeKey, in.Label)
	if err != nil {
		return nil, err
	}
	t.Description = in.Description
	t.IconOnline = in.IconOnline
	t.IconOffline = in.IconOffline
	t.IconTrouble = in.IconTrouble
	t.SortOrder = in.SortOrder
	t.HasCoverageArea = in.HasCoverageArea
	if err := s.nodeTypes.Create(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Service) UpdateNodeType(ctx context.Context, in port.UpdateNodeTypeInput) (*domain.NodeType, error) {
	t, err := s.nodeTypes.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if in.Label != nil {
		t.Label = *in.Label
	}
	if in.Description != nil {
		t.Description = *in.Description
	}
	if in.IconOnline != nil {
		t.IconOnline = *in.IconOnline
	}
	if in.IconOffline != nil {
		t.IconOffline = *in.IconOffline
	}
	if in.IconTrouble != nil {
		t.IconTrouble = *in.IconTrouble
	}
	if in.SortOrder != nil {
		t.SortOrder = *in.SortOrder
	}
	if in.Active != nil {
		t.Active = *in.Active
	}
	if in.HasCoverageArea != nil {
		t.HasCoverageArea = *in.HasCoverageArea
	}
	if err := s.nodeTypes.Update(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// =====================================================================
// Nodes
// =====================================================================

func (s *Service) ListNodes(ctx context.Context, f port.NodeListFilter) ([]port.NodeListItem, int, error) {
	return s.nodes.List(ctx, f)
}

func (s *Service) GetNode(ctx context.Context, id uuid.UUID) (*port.NodeListItem, error) {
	return s.nodes.FindByID(ctx, id)
}

// CreateNode builds the entity, optionally seeds the node's ports, and
// commits everything in one repo transaction. TotalPorts > 0 auto-creates
// that many ports numbered 1..N with the supplied PortRole (default generic).
func (s *Service) CreateNode(ctx context.Context, in port.CreateNodeInput) (*domain.Node, error) {
	// Verify the node type exists and is active.
	t, err := s.nodeTypes.FindByID(ctx, in.NodeTypeID)
	if err != nil {
		return nil, err
	}
	if !t.Active {
		return nil, derrors.Validation("node.type_inactive",
			"selected node type is inactive")
	}

	n, err := domain.NewNode(in.NodeTypeID, in.Name, in.Code)
	if err != nil {
		return nil, err
	}
	n.ParentID = in.ParentID
	n.UpstreamPortID = in.UpstreamPortID
	n.BranchID = in.BranchID
	n.Address = in.Address
	if in.GPSLat != nil && in.GPSLng != nil {
		if err := n.SetGPS(*in.GPSLat, *in.GPSLng); err != nil {
			return nil, err
		}
	} else if in.GPSLat != nil || in.GPSLng != nil {
		return nil, derrors.Validation("node.gps_pair_required",
			"both gps_lat and gps_lng must be provided together")
	}
	n.CoverageRadiusM = in.CoverageRadiusM
	n.TotalPorts = in.TotalPorts
	if in.Metadata != nil {
		n.Metadata = in.Metadata
	}

	autoPorts := 0
	if in.TotalPorts != nil && *in.TotalPorts > 0 {
		autoPorts = *in.TotalPorts
	}
	role := in.PortRole
	if role == "" {
		role = domain.PortRoleGeneric
	}
	if autoPorts > 0 && !role.Valid() {
		return nil, derrors.Validation("port.role_invalid", "invalid port_role")
	}

	if err := s.nodes.Create(ctx, n, autoPorts, role); err != nil {
		return nil, err
	}
	return n, nil
}

func (s *Service) UpdateNode(ctx context.Context, in port.UpdateNodeInput) (*domain.Node, error) {
	if in.Status != nil && !in.Status.Valid() {
		return nil, derrors.Validation("node.status_invalid", "invalid status")
	}
	if (in.GPSLat == nil) != (in.GPSLng == nil) && !in.ClearGPS {
		return nil, derrors.Validation("node.gps_pair_required",
			"both gps_lat and gps_lng must be provided together")
	}
	if in.GPSLat != nil {
		if *in.GPSLat < -90 || *in.GPSLat > 90 {
			return nil, derrors.Validation("node.lat_invalid", "lat must be in [-90,90]")
		}
	}
	if in.GPSLng != nil {
		if *in.GPSLng < -180 || *in.GPSLng > 180 {
			return nil, derrors.Validation("node.lng_invalid", "lng must be in [-180,180]")
		}
	}
	if in.ParentID != nil && *in.ParentID == in.ID {
		return nil, derrors.Validation("node.parent_self", "node cannot be its own parent")
	}
	return s.nodes.Update(ctx, in)
}

// =====================================================================
// Ports
// =====================================================================

func (s *Service) ListPortsForNode(ctx context.Context, nodeID uuid.UUID) ([]domain.Port, error) {
	return s.ports.ListForNode(ctx, nodeID)
}

// ReservePort holds a port for a customer for `holdSeconds`. Once the hold
// expires, a background sweep (future work) releases it back to Available.
// For Phase 1 we accept the hold and trust callers to activate or release.
func (s *Service) ReservePort(ctx context.Context, portID, customerID uuid.UUID, holdSeconds int) (*domain.Port, error) {
	if holdSeconds <= 0 {
		holdSeconds = 24 * 3600 // default 24h
	}
	p, err := s.ports.FindByID(ctx, portID)
	if err != nil {
		return nil, err
	}
	if err := p.Reserve(customerID, time.Now().UTC().Add(time.Duration(holdSeconds)*time.Second)); err != nil {
		return nil, err
	}
	if err := s.ports.Save(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) ActivatePort(ctx context.Context, portID, customerID uuid.UUID) (*domain.Port, error) {
	p, err := s.ports.FindByID(ctx, portID)
	if err != nil {
		return nil, err
	}
	if err := p.Activate(customerID); err != nil {
		return nil, err
	}
	if err := s.ports.Save(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) ReleasePort(ctx context.Context, portID uuid.UUID) (*domain.Port, error) {
	p, err := s.ports.FindByID(ctx, portID)
	if err != nil {
		return nil, err
	}
	p.Release()
	if err := s.ports.Save(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// =====================================================================
// Coverage check (PRD §5.1 — find_nearest_available_odp)
// =====================================================================

func (s *Service) CheckCoverage(ctx context.Context, in port.CoverageCheckInput) (*domain.CoverageResult, error) {
	if in.Lat < -90 || in.Lat > 90 {
		return nil, derrors.Validation("coverage.lat_invalid", "lat must be in [-90,90]")
	}
	if in.Lng < -180 || in.Lng > 180 {
		return nil, derrors.Validation("coverage.lng_invalid", "lng must be in [-180,180]")
	}
	if in.MaxCandidates <= 0 {
		in.MaxCandidates = 5
	}

	// Policy values from platform_config (cacheable, 60s TTL).
	maxRun := s.config.Int(ctx, "cable_max_run_meters", 210)
	routeFactor := s.config.Float(ctx, "cable_route_factor", 1.3)
	excessPrice := s.config.Float(ctx, "cable_excess_price_per_meter", 0)

	out := &domain.CoverageResult{
		Verdict:          domain.VerdictUncovered,
		MaxCableRunM:     maxRun,
		CableRouteFactor: routeFactor,
		ExcessPricePerM:  excessPrice,
	}

	// 1. Polygon containment — if the address falls inside an ODP's coverage
	//    polygon, that's the definitive answer (covered).
	containing, err := s.coverage.FindContaining(ctx, in.Lat, in.Lng, in.OnlyAvailable)
	if err != nil {
		return nil, err
	}
	if len(containing) > 0 {
		best := decorate(containing[0], in.Lat, in.Lng, routeFactor, maxRun, excessPrice)
		out.Verdict = domain.VerdictCovered
		out.BestCandidate = &best
		// Also return some nearest candidates by distance for UX context.
		nearby, _ := s.coverage.FindNearestODPs(ctx, in.Lat, in.Lng, 5000, in.MaxCandidates, in.OnlyAvailable)
		for _, c := range nearby {
			if c.NodeID == best.NodeID {
				continue
			}
			out.OtherCandidates = append(out.OtherCandidates, decorate(c, in.Lat, in.Lng, routeFactor, maxRun, excessPrice))
		}
		return out, nil
	}

	// 2. No polygon match — fall back to distance.
	near, err := s.coverage.FindNearestODPs(ctx, in.Lat, in.Lng, 5000, in.MaxCandidates+1, in.OnlyAvailable)
	if err != nil {
		return nil, err
	}
	if len(near) == 0 {
		return out, nil // VerdictUncovered, no candidates
	}

	best := decorate(near[0], in.Lat, in.Lng, routeFactor, maxRun, excessPrice)
	out.BestCandidate = &best
	if best.CableDistanceM <= float64(maxRun) {
		out.Verdict = domain.VerdictCovered
	} else {
		out.Verdict = domain.VerdictExcess
	}
	for _, c := range near[1:] {
		out.OtherCandidates = append(out.OtherCandidates, decorate(c, in.Lat, in.Lng, routeFactor, maxRun, excessPrice))
	}
	return out, nil
}

// decorate converts a repo row into the user-facing candidate, applying the
// route factor and computing excess cost vs the standard cable limit.
func decorate(row port.CoverageCandidateRow, qLat, qLng, factor float64, maxRun int, price float64) domain.CoverageCandidate {
	straight := row.StraightLineM
	if straight == 0 && row.GPSLat != nil && row.GPSLng != nil {
		straight = domain.HaversineMeters(qLat, qLng, *row.GPSLat, *row.GPSLng)
	}
	cable := straight * factor
	excess := 0.0
	excessCharge := 0.0
	if cable > float64(maxRun) {
		excess = cable - float64(maxRun)
		excessCharge = excess * price
	}
	return domain.CoverageCandidate{
		NodeID:         row.NodeID,
		NodeName:       row.NodeName,
		NodeCode:       row.NodeCode,
		StraightLineM:  straight,
		CableDistanceM: cable,
		ExcessM:        excess,
		ExcessCharge:   excessCharge,
		AvailablePorts: row.AvailablePorts,
		GPSLat:         row.GPSLat,
		GPSLng:         row.GPSLng,
		InPolygon:      row.InPolygon,
	}
}

// =====================================================================
// Polygons
// =====================================================================

func (s *Service) GetNodePolygon(ctx context.Context, nodeID uuid.UUID) (*domain.GeoJSONPolygon, error) {
	if _, err := s.nodes.FindByID(ctx, nodeID); err != nil {
		return nil, err
	}
	return s.coverage.GetPolygon(ctx, nodeID)
}

func (s *Service) SaveNodePolygon(ctx context.Context, in port.SaveCoveragePolygonInput) error {
	if !in.Polygon.IsValid() {
		return derrors.Validation("polygon.invalid_shape", "polygon must have a closed outer ring with ≥3 points")
	}
	return s.coverage.SavePolygon(ctx, in.NodeID, in.Polygon)
}

func (s *Service) ClearNodePolygon(ctx context.Context, nodeID uuid.UUID) error {
	return s.coverage.ClearPolygon(ctx, nodeID)
}

// =====================================================================
// KMZ / KML import
// =====================================================================
//
// Two-step flow: preview (parse, return the placemarks) → apply (write
// the polygons to the chosen nodes). The FE shows the preview to the user
// so they can map placemark name → existing node before committing.

func (s *Service) PreviewKMZ(ctx context.Context, body []byte) (*port.KMZImportPreview, error) {
	placemarks, err := domain.ParseKMZ(body)
	if err != nil {
		// Fallback: maybe the upload was raw KML (not zipped).
		if pms, kerr := domain.ParseKML(body); kerr == nil {
			placemarks = pms
		} else {
			return nil, derrors.Validation("kmz.parse", err.Error())
		}
	}
	return &port.KMZImportPreview{Placemarks: placemarks}, nil
}

func (s *Service) ApplyKMZ(ctx context.Context, in port.KMZImportApply) (int, error) {
	if len(in.Assignments) == 0 {
		return 0, nil
	}
	// Build a name → polygon lookup.
	byName := make(map[string]domain.GeoJSONPolygon, len(in.Polygons))
	for _, pm := range in.Polygons {
		byName[pm.Name] = pm.Polygon
	}
	applied := 0
	for name, nodeID := range in.Assignments {
		poly, ok := byName[name]
		if !ok {
			continue
		}
		if err := s.coverage.SavePolygon(ctx, nodeID, poly); err != nil {
			s.log.Warn("kmz apply failed", "name", name, "node_id", nodeID, "err", err)
			continue
		}
		applied++
	}
	return applied, nil
}

// =====================================================================
// Downstream impact
// =====================================================================

func (s *Service) DownstreamImpact(ctx context.Context, nodeID uuid.UUID) (*port.ImpactResult, error) {
	root, err := s.nodes.FindByID(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	rows, customers, err := s.impact.Downstream(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return &port.ImpactResult{
		RootID:       nodeID,
		RootName:     root.Node.Name,
		Nodes:        rows,
		CustomersHit: customers,
	}, nil
}

// Suppress "imported and not used" if time isn't referenced elsewhere — but
// it is, in port reservation. Kept as a no-op anchor against accidental
// import pruning.
var _ = time.Second

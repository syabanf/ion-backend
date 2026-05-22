// Package port defines the contracts between the network usecase layer and
// the world outside it. Same hexagonal pattern as internal/identity.
package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/network/domain"
)

// =====================================================================
// Driving ports
// =====================================================================

// NodeListFilter narrows /nodes list queries.
type NodeListFilter struct {
	Search     string // matches name or code (ILIKE)
	NodeTypeID *uuid.UUID
	BranchID   *uuid.UUID
	ParentID   *uuid.UUID // for "children of"
	Status     string
	Active     *bool
	Limit      int
	Offset     int
}

// NodeListItem is the projection returned by ListNodes — node + a few
// joined display fields so the table renders without N+1 lookups.
type NodeListItem struct {
	Node          domain.Node
	NodeTypeKey   string
	NodeTypeLabel string
	BranchName    string  // empty when unassigned
	BranchCode    string  // empty when unassigned
	ParentName    string  // empty for roots
	PortsTotal    int
	PortsUsed     int
}

// CreateNodeInput — required: type, name, code; everything else optional.
type CreateNodeInput struct {
	NodeTypeID      uuid.UUID
	Name            string
	Code            string
	ParentID        *uuid.UUID
	UpstreamPortID  *uuid.UUID
	BranchID        *uuid.UUID
	Address         string
	GPSLat          *float64
	GPSLng          *float64
	CoverageRadiusM *int
	TotalPorts      *int // when > 0 we auto-create empty ports
	PortRole        domain.PortRole // role for auto-created ports; default 'generic'
	Metadata        map[string]any
}

// UpdateNodeInput — partial; nil means leave alone, Clear* flags NULL the field.
type UpdateNodeInput struct {
	ID              uuid.UUID
	Name            *string
	ParentID        *uuid.UUID
	ClearParent     bool
	UpstreamPortID  *uuid.UUID
	ClearUpstream   bool
	BranchID        *uuid.UUID
	ClearBranch     bool
	Address         *string
	GPSLat          *float64
	GPSLng          *float64
	ClearGPS        bool
	CoverageRadiusM *int
	ClearCoverage   bool
	Status          *domain.NodeStatus
	Active          *bool
	Metadata        map[string]any
}

// CreateNodeTypeInput / UpdateNodeTypeInput — admin-managed catalog.
type CreateNodeTypeInput struct {
	TypeKey         string
	Label           string
	Description     string
	IconOnline      string
	IconOffline     string
	IconTrouble     string
	SortOrder       int
	HasCoverageArea bool
}

type UpdateNodeTypeInput struct {
	ID              uuid.UUID
	Label           *string
	Description     *string
	IconOnline      *string
	IconOffline     *string
	IconTrouble     *string
	SortOrder       *int
	Active          *bool
	HasCoverageArea *bool
}

// CoverageCheckInput — a single (lat, lng) query against the ODP catalog.
type CoverageCheckInput struct {
	Lat             float64
	Lng             float64
	OnlyAvailable   bool // when true, candidates must have ≥1 available port
	MaxCandidates   int  // how many additional nearest ODPs to return
}

// ImpactRow is one downstream node found by traversing parent_id from a
// given root. Each row carries its depth from the root (root itself = 0).
type ImpactRow struct {
	NodeID        uuid.UUID
	Name          string
	Code          string
	NodeTypeKey   string
	NodeTypeLabel string
	Depth         int
	ParentID      *uuid.UUID
	ParentName    string
}

// ImpactResult — what GET /nodes/:id/impact returns.
type ImpactResult struct {
	RootID         uuid.UUID
	RootName       string
	Nodes          []ImpactRow
	CustomersHit   int // count of distinct active customers attached to downstream ODP ports
}

// SaveCoveragePolygonInput — store or replace a node's coverage polygon.
type SaveCoveragePolygonInput struct {
	NodeID  uuid.UUID
	Polygon domain.GeoJSONPolygon
}

// KMZImportPreview — what the importer returns after parsing the KMZ.
// We return a preview so the FE can show "X polygons found, here are the
// names; pick which to apply to which node" before committing writes.
type KMZImportPreview struct {
	Placemarks []domain.KMLPlacemark
}

// KMZImportApply — commit polygons to nodes by name match or explicit mapping.
type KMZImportApply struct {
	// Map of placemark name → node UUID. Names not in the map are skipped.
	Assignments map[string]uuid.UUID
	Polygons    []domain.KMLPlacemark
}

// UseCase is what the HTTP handler depends on.
type UseCase interface {
	// Node types
	ListNodeTypes(ctx context.Context, includeInactive bool) ([]domain.NodeType, error)
	GetNodeType(ctx context.Context, id uuid.UUID) (*domain.NodeType, error)
	CreateNodeType(ctx context.Context, in CreateNodeTypeInput) (*domain.NodeType, error)
	UpdateNodeType(ctx context.Context, in UpdateNodeTypeInput) (*domain.NodeType, error)

	// Nodes
	ListNodes(ctx context.Context, f NodeListFilter) ([]NodeListItem, int, error)
	GetNode(ctx context.Context, id uuid.UUID) (*NodeListItem, error)
	CreateNode(ctx context.Context, in CreateNodeInput) (*domain.Node, error)
	UpdateNode(ctx context.Context, in UpdateNodeInput) (*domain.Node, error)

	// Ports
	ListPortsForNode(ctx context.Context, nodeID uuid.UUID) ([]domain.Port, error)
	ReservePort(ctx context.Context, portID, customerID uuid.UUID, holdSeconds int) (*domain.Port, error)
	ActivatePort(ctx context.Context, portID, customerID uuid.UUID) (*domain.Port, error)
	ReleasePort(ctx context.Context, portID uuid.UUID) (*domain.Port, error)

	// Coverage
	CheckCoverage(ctx context.Context, in CoverageCheckInput) (*domain.CoverageResult, error)
	GetNodePolygon(ctx context.Context, nodeID uuid.UUID) (*domain.GeoJSONPolygon, error)
	SaveNodePolygon(ctx context.Context, in SaveCoveragePolygonInput) error
	ClearNodePolygon(ctx context.Context, nodeID uuid.UUID) error

	// KMZ / KML import
	PreviewKMZ(ctx context.Context, body []byte) (*KMZImportPreview, error)
	ApplyKMZ(ctx context.Context, in KMZImportApply) (int, error) // returns applied count

	// Impact
	DownstreamImpact(ctx context.Context, nodeID uuid.UUID) (*ImpactResult, error)
}

// =====================================================================
// Driven ports (repositories)
// =====================================================================

type NodeTypeRepository interface {
	List(ctx context.Context, includeInactive bool) ([]domain.NodeType, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.NodeType, error)
	FindByKey(ctx context.Context, typeKey string) (*domain.NodeType, error)
	Create(ctx context.Context, t *domain.NodeType) error
	Update(ctx context.Context, t *domain.NodeType) error
}

type NodeRepository interface {
	List(ctx context.Context, f NodeListFilter) ([]NodeListItem, int, error)
	FindByID(ctx context.Context, id uuid.UUID) (*NodeListItem, error)
	Create(ctx context.Context, n *domain.Node, autoCreatePorts int, defaultPortRole domain.PortRole) error
	Update(ctx context.Context, in UpdateNodeInput) (*domain.Node, error)
}

type PortRepository interface {
	ListForNode(ctx context.Context, nodeID uuid.UUID) ([]domain.Port, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Port, error)
	Save(ctx context.Context, p *domain.Port) error
}

// CoverageRepository — spatial reads/writes against PostGIS columns.
//
// FindNearestODPs uses an index-friendly two-step:
//   1. ST_DWithin on geography to filter to a max search radius (cheap, indexed).
//   2. Order by ST_Distance ascending; take top N.
//
// FindContaining uses ST_Contains on the indexed coverage_polygon —
// effectively constant-time even with thousands of polygons.
type CoverageRepository interface {
	// FindContaining returns ODP nodes whose coverage polygon contains the point.
	// May be empty; that's the "no polygon coverage" case.
	FindContaining(ctx context.Context, lat, lng float64, onlyAvailable bool) ([]CoverageCandidateRow, error)

	// FindNearestODPs returns the N nearest ODP nodes within searchRadiusM,
	// ordered by straight-line distance ascending.
	FindNearestODPs(ctx context.Context, lat, lng float64, searchRadiusM int, limit int, onlyAvailable bool) ([]CoverageCandidateRow, error)

	GetPolygon(ctx context.Context, nodeID uuid.UUID) (*domain.GeoJSONPolygon, error)
	SavePolygon(ctx context.Context, nodeID uuid.UUID, polygon domain.GeoJSONPolygon) error
	ClearPolygon(ctx context.Context, nodeID uuid.UUID) error
}

// CoverageCandidateRow — what the repo returns. The usecase decorates it
// with route-factor cable distance + excess cost (those are policy fields,
// not facts about the geometry).
type CoverageCandidateRow struct {
	NodeID         uuid.UUID
	NodeName       string
	NodeCode       string
	GPSLat         *float64
	GPSLng         *float64
	StraightLineM  float64 // 0 when polygon match only
	AvailablePorts int
	InPolygon      bool
}

// ImpactRepository — recursive CTE traversal for fault impact.
type ImpactRepository interface {
	Downstream(ctx context.Context, rootID uuid.UUID) ([]ImpactRow, int, error) // rows + active customer count
}

// =====================================================================
// RADIUS adapter (PRD §13)
// =====================================================================
//
// RadiusClient is the contract for talking to ION Radius / FreeRADIUS.
//
// The first implementation is LocalRadiusClient — a DB-backed stub that
// persists state transitions in network.radius_accounts but doesn't open
// a connection to a real RADIUS server. When the real FreeRADIUS adapter
// lands, it implements this same interface; nothing else changes.

// RadiusClient handles credential lifecycle for a customer's network access.
type RadiusClient interface {
	// Provision creates a new RADIUS account in TEMPORARY state at WO creation.
	Provision(ctx context.Context, in domain.ProvisionInput) (*domain.RadiusAccount, error)
	// PromoteToPermanent — NOC approved BAST + invoices paid.
	PromoteToPermanent(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error)
	// Suspend — suspension schema fired.
	Suspend(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error)
	// Restore — payment confirmed.
	Restore(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error)
	// Deactivate — termination.
	Deactivate(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error)
	// Find returns nil if no account exists for this customer.
	Find(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error)
}

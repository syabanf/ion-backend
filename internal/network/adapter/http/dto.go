// Package http — DTOs for the network adapter.
//
// All HTTP-layer request/response shapes for network live in this
// file (node types, nodes, ports, coverage, polygons, KMZ import,
// downstream impact). Conversion helpers `toXxxDTO` sit next to
// their target type so a change to the wire shape touches one file
// instead of three.
//
// Why DTOs are an adapter concern (not a domain concern):
//   - Domain types should stay framework-free; they shouldn't know
//     about JSON tags or HTTP versioning.
//   - The wire format can drift (rename, add field, deprecate) without
//     touching usecase or domain code.
//   - One file per bounded context keeps the surface easy to grep when
//     a contract question arises ("what does /api/network/nodes
//     return?").
package http

import (
	"time"

	"github.com/ion-core/backend/internal/network/domain"
	"github.com/ion-core/backend/internal/network/port"
)

// =====================================================================
// Node types
// =====================================================================

type nodeTypeDTO struct {
	ID              string `json:"id"`
	TypeKey         string `json:"type_key"`
	Label           string `json:"label"`
	Description     string `json:"description"`
	IconOnline      string `json:"icon_online"`
	IconOffline     string `json:"icon_offline"`
	IconTrouble     string `json:"icon_trouble"`
	SortOrder       int    `json:"sort_order"`
	Active          bool   `json:"active"`
	HasCoverageArea bool   `json:"has_coverage_area"`
}

func toNodeTypeDTO(t domain.NodeType) nodeTypeDTO {
	return nodeTypeDTO{
		ID: t.ID.String(), TypeKey: t.TypeKey, Label: t.Label, Description: t.Description,
		IconOnline: t.IconOnline, IconOffline: t.IconOffline, IconTrouble: t.IconTrouble,
		SortOrder: t.SortOrder, Active: t.Active, HasCoverageArea: t.HasCoverageArea,
	}
}

type createNodeTypeRequest struct {
	TypeKey         string `json:"type_key"`
	Label           string `json:"label"`
	Description     string `json:"description"`
	IconOnline      string `json:"icon_online"`
	IconOffline     string `json:"icon_offline"`
	IconTrouble     string `json:"icon_trouble"`
	SortOrder       int    `json:"sort_order"`
	HasCoverageArea bool   `json:"has_coverage_area"`
}

type updateNodeTypeRequest struct {
	Label           *string `json:"label,omitempty"`
	Description     *string `json:"description,omitempty"`
	IconOnline      *string `json:"icon_online,omitempty"`
	IconOffline     *string `json:"icon_offline,omitempty"`
	IconTrouble     *string `json:"icon_trouble,omitempty"`
	SortOrder       *int    `json:"sort_order,omitempty"`
	Active          *bool   `json:"active,omitempty"`
	HasCoverageArea *bool   `json:"has_coverage_area,omitempty"`
}

// =====================================================================
// Nodes
// =====================================================================

type nodeDTO struct {
	ID              string         `json:"id"`
	NodeTypeID      string         `json:"node_type_id"`
	NodeTypeKey     string         `json:"node_type_key,omitempty"`
	NodeTypeLabel   string         `json:"node_type_label,omitempty"`
	Name            string         `json:"name"`
	Code            string         `json:"code"`
	ParentID        *string        `json:"parent_id,omitempty"`
	ParentName      string         `json:"parent_name,omitempty"`
	UpstreamPortID  *string        `json:"upstream_port_id,omitempty"`
	BranchID        *string        `json:"branch_id,omitempty"`
	BranchName      string         `json:"branch_name,omitempty"`
	BranchCode      string         `json:"branch_code,omitempty"`
	Address         string         `json:"address,omitempty"`
	GPSLat          *float64       `json:"gps_lat,omitempty"`
	GPSLng          *float64       `json:"gps_lng,omitempty"`
	CoverageRadiusM *int           `json:"coverage_radius_m,omitempty"`
	TotalPorts      *int           `json:"total_ports,omitempty"`
	PortsTotal      int            `json:"ports_total"`
	PortsUsed       int            `json:"ports_used"`
	Status          string         `json:"status"`
	Metadata        map[string]any `json:"metadata"`
	Active          bool           `json:"active"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
}

func toNodeDTO(it port.NodeListItem) nodeDTO {
	n := it.Node
	d := nodeDTO{
		ID:              n.ID.String(),
		NodeTypeID:      n.NodeTypeID.String(),
		NodeTypeKey:     it.NodeTypeKey,
		NodeTypeLabel:   it.NodeTypeLabel,
		Name:            n.Name,
		Code:            n.Code,
		BranchName:      it.BranchName,
		BranchCode:      it.BranchCode,
		ParentName:      it.ParentName,
		Address:         n.Address,
		GPSLat:          n.GPSLat,
		GPSLng:          n.GPSLng,
		CoverageRadiusM: n.CoverageRadiusM,
		TotalPorts:      n.TotalPorts,
		PortsTotal:      it.PortsTotal,
		PortsUsed:       it.PortsUsed,
		Status:          string(n.Status),
		Metadata:        n.Metadata,
		Active:          n.Active,
		CreatedAt:       n.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       n.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if n.ParentID != nil {
		s := n.ParentID.String()
		d.ParentID = &s
	}
	if n.UpstreamPortID != nil {
		s := n.UpstreamPortID.String()
		d.UpstreamPortID = &s
	}
	if n.BranchID != nil {
		s := n.BranchID.String()
		d.BranchID = &s
	}
	return d
}

// toBareNodeDTO renders a domain.Node without the joined-display fields.
// Used as the response to PATCH /nodes/{id} which doesn't refetch the joins.
func toBareNodeDTO(n domain.Node) nodeDTO {
	d := nodeDTO{
		ID:              n.ID.String(),
		NodeTypeID:      n.NodeTypeID.String(),
		Name:            n.Name,
		Code:            n.Code,
		Address:         n.Address,
		GPSLat:          n.GPSLat,
		GPSLng:          n.GPSLng,
		CoverageRadiusM: n.CoverageRadiusM,
		TotalPorts:      n.TotalPorts,
		Status:          string(n.Status),
		Metadata:        n.Metadata,
		Active:          n.Active,
		CreatedAt:       n.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       n.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if n.ParentID != nil {
		s := n.ParentID.String()
		d.ParentID = &s
	}
	if n.UpstreamPortID != nil {
		s := n.UpstreamPortID.String()
		d.UpstreamPortID = &s
	}
	if n.BranchID != nil {
		s := n.BranchID.String()
		d.BranchID = &s
	}
	return d
}

type createNodeRequest struct {
	NodeTypeID      string         `json:"node_type_id"`
	Name            string         `json:"name"`
	Code            string         `json:"code"`
	ParentID        *string        `json:"parent_id,omitempty"`
	UpstreamPortID  *string        `json:"upstream_port_id,omitempty"`
	BranchID        *string        `json:"branch_id,omitempty"`
	Address         string         `json:"address"`
	GPSLat          *float64       `json:"gps_lat,omitempty"`
	GPSLng          *float64       `json:"gps_lng,omitempty"`
	CoverageRadiusM *int           `json:"coverage_radius_m,omitempty"`
	TotalPorts      *int           `json:"total_ports,omitempty"`
	PortRole        string         `json:"port_role,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

type updateNodeRequest struct {
	Name            *string        `json:"name,omitempty"`
	ParentID        *string        `json:"parent_id,omitempty"`
	ClearParent     bool           `json:"clear_parent,omitempty"`
	UpstreamPortID  *string        `json:"upstream_port_id,omitempty"`
	ClearUpstream   bool           `json:"clear_upstream,omitempty"`
	BranchID        *string        `json:"branch_id,omitempty"`
	ClearBranch     bool           `json:"clear_branch,omitempty"`
	Address         *string        `json:"address,omitempty"`
	GPSLat          *float64       `json:"gps_lat,omitempty"`
	GPSLng          *float64       `json:"gps_lng,omitempty"`
	ClearGPS        bool           `json:"clear_gps,omitempty"`
	CoverageRadiusM *int           `json:"coverage_radius_m,omitempty"`
	ClearCoverage   bool           `json:"clear_coverage,omitempty"`
	Status          *string        `json:"status,omitempty"`
	Active          *bool          `json:"active,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// =====================================================================
// Ports
// =====================================================================

type portDTO struct {
	ID                string  `json:"id"`
	NodeID            string  `json:"node_id"`
	PortNumber        int     `json:"port_number"`
	Role              string  `json:"role"`
	MaxCapacity       int     `json:"max_capacity"`
	ActiveConnections int     `json:"active_connections"`
	Status            string  `json:"status"`
	CustomerID        *string `json:"customer_id,omitempty"`
	ReservedFor       *string `json:"reserved_for,omitempty"`
	ReservedUntil     *string `json:"reserved_until,omitempty"`
	ActivatedAt       *string `json:"activated_at,omitempty"`
}

func toPortDTO(p domain.Port) portDTO {
	d := portDTO{
		ID: p.ID.String(), NodeID: p.NodeID.String(), PortNumber: p.PortNumber,
		Role: string(p.Role), MaxCapacity: p.MaxCapacity,
		ActiveConnections: p.ActiveConnections, Status: string(p.Status),
	}
	if p.CustomerID != nil {
		s := p.CustomerID.String()
		d.CustomerID = &s
	}
	if p.ReservedFor != nil {
		s := p.ReservedFor.String()
		d.ReservedFor = &s
	}
	if p.ReservedUntil != nil {
		s := p.ReservedUntil.UTC().Format(time.RFC3339)
		d.ReservedUntil = &s
	}
	if p.ActivatedAt != nil {
		s := p.ActivatedAt.UTC().Format(time.RFC3339)
		d.ActivatedAt = &s
	}
	return d
}

type reservePortRequest struct {
	CustomerID  string `json:"customer_id"`
	HoldSeconds int    `json:"hold_seconds,omitempty"`
}

type activatePortRequest struct {
	CustomerID string `json:"customer_id"`
}

// =====================================================================
// Coverage
// =====================================================================

type coverageCheckRequest struct {
	Lat           float64 `json:"lat"`
	Lng           float64 `json:"lng"`
	OnlyAvailable bool    `json:"only_available,omitempty"`
	MaxCandidates int     `json:"max_candidates,omitempty"`
}

type coverageCandidateDTO struct {
	NodeID         string   `json:"node_id"`
	NodeName       string   `json:"node_name"`
	NodeCode       string   `json:"node_code"`
	StraightLineM  float64  `json:"straight_line_m"`
	CableDistanceM float64  `json:"cable_distance_m"`
	ExcessM        float64  `json:"excess_m"`
	ExcessCharge   float64  `json:"excess_charge"`
	AvailablePorts int      `json:"available_ports"`
	GPSLat         *float64 `json:"gps_lat,omitempty"`
	GPSLng         *float64 `json:"gps_lng,omitempty"`
	InPolygon      bool     `json:"in_polygon"`
}

func toCandidateDTO(c domain.CoverageCandidate) coverageCandidateDTO {
	return coverageCandidateDTO{
		NodeID:         c.NodeID.String(),
		NodeName:       c.NodeName,
		NodeCode:       c.NodeCode,
		StraightLineM:  c.StraightLineM,
		CableDistanceM: c.CableDistanceM,
		ExcessM:        c.ExcessM,
		ExcessCharge:   c.ExcessCharge,
		AvailablePorts: c.AvailablePorts,
		GPSLat:         c.GPSLat,
		GPSLng:         c.GPSLng,
		InPolygon:      c.InPolygon,
	}
}

type coverageResultDTO struct {
	Verdict          string                 `json:"verdict"`
	BestCandidate    *coverageCandidateDTO  `json:"best_candidate,omitempty"`
	OtherCandidates  []coverageCandidateDTO `json:"other_candidates"`
	MaxCableRunM     int                    `json:"max_cable_run_m"`
	CableRouteFactor float64                `json:"cable_route_factor"`
	ExcessPricePerM  float64                `json:"excess_price_per_meter"`
}

// =====================================================================
// Polygons
// =====================================================================

type savePolygonRequest struct {
	Polygon domain.GeoJSONPolygon `json:"polygon"`
}

// =====================================================================
// KMZ
// =====================================================================

type kmzApplyRequest struct {
	// Placemarks the FE got back from /preview — echoed back so the BE
	// doesn't have to keep state between the two calls.
	Placemarks []domain.KMLPlacemark `json:"placemarks"`
	// Mapping placemark Name → node UUID. Names not in the map are skipped.
	Assignments map[string]string `json:"assignments"`
}

type kmzApplyResponse struct {
	Applied int `json:"applied"`
}

// =====================================================================
// Impact
// =====================================================================

type impactRowDTO struct {
	NodeID        string  `json:"node_id"`
	Name          string  `json:"name"`
	Code          string  `json:"code"`
	NodeTypeKey   string  `json:"node_type_key"`
	NodeTypeLabel string  `json:"node_type_label"`
	Depth         int     `json:"depth"`
	ParentID      *string `json:"parent_id,omitempty"`
	ParentName    string  `json:"parent_name,omitempty"`
}

type impactResponse struct {
	RootID       string         `json:"root_id"`
	RootName     string         `json:"root_name"`
	CustomersHit int            `json:"customers_hit"`
	Nodes        []impactRowDTO `json:"nodes"`
}

package domain

import (
	"time"

	"github.com/google/uuid"
)

// Role is a named bundle of permissions, per PRD Administration §7.
// Roles are seeded at deployment (Sales Rep, NOC, Finance Manager, etc.)
// and can be added/edited by Super Admin.
type Role struct {
	ID          uuid.UUID
	Name        string // canonical machine name, e.g. "sales_rep", "noc_manager"
	Description string
	CreatedAt   time.Time
}

// Permission is module + action (e.g. module="crm", action="lead.create").
// The HTTP layer reads role permissions at request time and checks the
// requested operation against the authenticated user's grants.
type Permission struct {
	ID          uuid.UUID
	Module      string
	Action      string
	Description string
}

// Key returns the canonical "module.action" string used in middleware checks
// and on the wire. Stable; do not rename without coordinating with the FE.
func (p Permission) Key() string { return p.Module + "." + p.Action }

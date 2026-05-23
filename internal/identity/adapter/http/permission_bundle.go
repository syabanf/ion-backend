// Wave 118 — Permission bundles endpoint (TC-RBAC-* regression edge).
//
// The dashboard's /admin/roles page (frontend) lets admins build composite
// roles, but lacks a "starter pack" UX — every new role has to be built
// permission-by-permission. This endpoint surfaces six pre-built bundles
// the FE can offer as one-click role creation:
//
//   - sales_starter / sales_advanced
//   - ops_starter / ops_advanced
//   - finance_starter / finance_advanced
//
// Read-only — the FE picks a bundle, then POSTs to /roles + /roles/{id}/permissions
// via the existing endpoints. The bundle data lives here (not the DB) because
// it's a UX convenience, not a domain entity; future schema-driven config
// can promote it to a table.

package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ion-core/backend/pkg/httpserver"
)

// PermissionBundle is a named collection of permission keys.
type PermissionBundle struct {
	Code        string   `json:"code"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Persona     string   `json:"persona"`
	Permissions []string `json:"permissions"`
}

// CommonPermissionBundles returns the six standard bundles. Exported so
// tests + future programmatic callers can consume the same list.
func CommonPermissionBundles() []PermissionBundle {
	return []PermissionBundle{
		{
			Code:        "sales_starter",
			DisplayName: "Sales Starter",
			Description: "Entry-level sales rep: create leads, view own pipeline, KTP OCR upload.",
			Persona:     "sales",
			Permissions: []string{
				"crm.lead.create",
				"crm.lead.read",
				"crm.lead.update",
				"crm.ktp.upload",
				"identity.user.read",
			},
		},
		{
			Code:        "sales_advanced",
			DisplayName: "Sales Advanced",
			Description: "Senior sales rep: full pipeline, plan changes, commission visibility.",
			Persona:     "sales",
			Permissions: []string{
				"crm.lead.create",
				"crm.lead.read",
				"crm.lead.update",
				"crm.lead.assign",
				"crm.ktp.upload",
				"crm.plan_change.request",
				"billing.commission.read.self",
				"identity.user.read",
			},
		},
		{
			Code:        "ops_starter",
			DisplayName: "Operations Starter",
			Description: "Field operations: WO read, BAST submit, check-in.",
			Persona:     "operations",
			Permissions: []string{
				"field.wo.read",
				"field.wo.checkin",
				"field.bast.submit",
				"warehouse.serialized.scan",
				"warehouse.cable.cut",
				"warehouse.consumable.consume",
			},
		},
		{
			Code:        "ops_advanced",
			DisplayName: "Operations Advanced",
			Description: "Team lead: WO assign, schedule, BAST verify, sub-warehouse manage.",
			Persona:     "operations",
			Permissions: []string{
				"field.wo.read",
				"field.wo.assign",
				"field.wo.schedule",
				"field.wo.checkin",
				"field.bast.submit",
				"field.bast.verify",
				"warehouse.serialized.scan",
				"warehouse.cable.cut",
				"warehouse.consumable.consume",
				"warehouse.sub_warehouse.manage",
				"identity.user.read",
			},
		},
		{
			Code:        "finance_starter",
			DisplayName: "Finance Starter",
			Description: "AR clerk: invoice read, payment confirm, reminder dispatch.",
			Persona:     "finance",
			Permissions: []string{
				"billing.invoice.read",
				"billing.payment.confirm",
				"billing.reminder.dispatch",
				"billing.commission.read",
				"hris.employee.read",
			},
		},
		{
			Code:        "finance_advanced",
			DisplayName: "Finance Advanced",
			Description: "Finance manager: invoice issue, refund, tax, commission approval, HRIS read.",
			Persona:     "finance",
			Permissions: []string{
				"billing.invoice.read",
				"billing.invoice.issue",
				"billing.invoice.cancel",
				"billing.payment.confirm",
				"billing.payment.refund",
				"billing.commission.read",
				"billing.commission.approve",
				"billing.reminder.dispatch",
				"tax.faktur.generate",
				"hris.employee.read",
				"hris.event.read",
			},
		},
	}
}

// mountPermissionBundles registers the GET endpoint on the given router.
// Wave 118 — call this from the existing Handler.Mount after the other
// permissioned routes. Read access is gated by identity.permission.read
// (same as /permissions catalog).
func (h *Handler) mountPermissionBundles(r chi.Router) {
	r.With(httpserver.RequirePermission("identity.permission.read")).
		Get("/permission-bundles", h.listPermissionBundles)
}

func (h *Handler) listPermissionBundles(w http.ResponseWriter, _ *http.Request) {
	bundles := CommonPermissionBundles()
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": bundles,
		"total": len(bundles),
	})
}

// Package usecase implements the CRM bounded context's business rules.
//
// The service wires repositories + the coverage gateway. The convert flow
// is the load-bearing piece — it enforces the document gate and creates
// customer + order in one transaction-shaped sequence (best-effort; the
// repositories aren't wrapped in a single tx in round 1, same trade-off
// the warehouse service makes).
package usecase

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type Service struct {
	products  port.ProductRepository
	leads     port.LeadRepository
	docs      port.DocumentRepository
	customers port.CustomerRepository
	orders    port.OrderRepository
	coverage  port.CoverageGateway
	billing   port.BillingGateway // optional; nil = no auto-OTC

	// M4 r2 — optional, nil-safe.
	schemas   port.OnboardingSchemaRepository
	salesUser port.SalesUserGateway

	// Wave 80b (TC-SCH-011/015/023/026, TC-PRD-025) — optional schema
	// resolver gateway. When wired, ConvertLead snapshots the resolved
	// version of each of the 5 schema kinds onto the new customer row
	// so subsequent reads stay pinned to order-time behavior.
	schemaResolver port.SchemaResolverGateway

	// Wave 81 (TC-PRD-013/028) — audit writer for product mutations.
	// Defaults to Nop; CreateProduct + UpdateProduct fire SafeWrite so
	// the admin audit viewer can show price/slot changes over time.
	auditW audit.Writer
}

func NewService(
	products port.ProductRepository,
	leads port.LeadRepository,
	docs port.DocumentRepository,
	customers port.CustomerRepository,
	orders port.OrderRepository,
	coverage port.CoverageGateway,
) *Service {
	return &Service{
		products:  products,
		leads:     leads,
		docs:      docs,
		customers: customers,
		orders:    orders,
		coverage:  coverage,
		auditW:    audit.Nop{},
	}
}

// WithAudit wires the append-only audit writer. Wave 81 — CreateProduct
// and UpdateProduct emit a row through this writer so the admin
// product-history view can render diffs without a separate audit table.
// Defaults to audit.Nop{}.
func (s *Service) WithAudit(w audit.Writer) *Service {
	if w != nil {
		s.auditW = w
	}
	return s
}

// WithBilling attaches the billing gateway so converting a lead also
// auto-creates an OTC invoice. Optional — the M4 round-1 wiring left
// this nil, and the M6 wiring (crm-svc embedding billing usecase)
// passes a real impl.
func (s *Service) WithBilling(b port.BillingGateway) *Service {
	s.billing = b
	return s
}

// WithR2 attaches M4 r2 dependencies: the onboarding schema repo (used
// at lead creation in place of the hardcoded DefaultBroadbandDocs) and
// the sales-user gateway (used for sales_type enforcement). Both are
// optional; when nil, behaviour falls back to r1 (hardcoded + no
// type check), so existing callers keep working.
func (s *Service) WithR2(schemas port.OnboardingSchemaRepository, salesUser port.SalesUserGateway) *Service {
	s.schemas = schemas
	s.salesUser = salesUser
	return s
}

// WithSchemaResolver attaches the Wave 80b schema-resolver gateway.
// When wired, ConvertLead snapshots the 5 resolved schema versions
// onto the new customer record. Nil-safe — convert still works
// without it; customers just fall through to the existing
// FindLatestPublished resolver path on subsequent reads.
func (s *Service) WithSchemaResolver(r port.SchemaResolverGateway) *Service {
	s.schemaResolver = r
	return s
}

var _ port.UseCase = (*Service)(nil)

// =====================================================================
// Products
// =====================================================================

func (s *Service) ListProducts(ctx context.Context, f port.ProductListFilter) ([]domain.Product, error) {
	return s.products.List(ctx, f)
}

// GetProduct returns a single product by id. Wave 77.
func (s *Service) GetProduct(ctx context.Context, id uuid.UUID) (*domain.Product, error) {
	return s.products.FindByID(ctx, id)
}

func (s *Service) CreateProduct(ctx context.Context, in port.CreateProductInput) (*domain.Product, error) {
	p, err := domain.NewProduct(in.Code, in.Name, in.SpeedMbps, in.MonthlyPrice, in.OTCPrice)
	if err != nil {
		return nil, err
	}
	// Wave 77 (TC-PRD-014/016/018/022): copy optional schema slots.
	p.OnboardingSchemaID = in.OnboardingSchemaID
	p.BillingSchemaID = in.BillingSchemaID
	p.ServiceSchemaID = in.ServiceSchemaID
	p.CommissionSchemaID = in.CommissionSchemaID
	p.SuspensionSchemaID = in.SuspensionSchemaID
	if err := s.products.Create(ctx, p); err != nil {
		return nil, err
	}
	// Wave 81 (TC-PRD-013) — record provisioning. We log code + name
	// + monthly price as the canonical identifier triple so the audit
	// viewer doesn't have to join back to the product row.
	audit.SafeWrite(ctx, s.auditW, audit.Entry{
		Module:     "crm",
		RecordType: "product",
		RecordID:   p.ID.String(),
		After:      p.Code + " " + p.Name + " monthly=" + ftoa(p.MonthlyPrice),
		Reason:     "product_created",
	})
	return p, nil
}

// UpdateProduct applies a partial patch including schema slot
// (re)assignment. Wave 77 (TC-PRD-014/016/018/022/024).
//
// Mutation rules:
//   - Non-nil pointer field → apply.
//   - Clear*Schema=true → null the corresponding slot.
//   - Both set is ambiguous; ClearX wins so the caller gets predictable
//     behavior when serializing partial updates from a UI.
//
// The resolver (`internal/platform/usecase.ResolveForCustomer`) treats
// a null slot as "use customer-type default" — clearing a slot is
// equivalent to "let the global default re-emerge".
func (s *Service) UpdateProduct(ctx context.Context, in port.UpdateProductInput) (*domain.Product, error) {
	p, err := s.products.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.SpeedMbps != nil {
		p.SpeedMbps = *in.SpeedMbps
	}
	if in.MonthlyPrice != nil {
		p.MonthlyPrice = *in.MonthlyPrice
	}
	if in.OTCPrice != nil {
		p.OTCPrice = *in.OTCPrice
	}
	if in.TempWindowHrs != nil {
		p.TempActivationWindowHrs = *in.TempWindowHrs
	}
	if in.Active != nil {
		p.Active = *in.Active
	}
	applySlot := func(clear bool, ptr *uuid.UUID, current **uuid.UUID) {
		if clear {
			*current = nil
			return
		}
		if ptr != nil {
			id := *ptr
			*current = &id
		}
	}
	applySlot(in.ClearOnboarding, in.OnboardingSchemaID, &p.OnboardingSchemaID)
	applySlot(in.ClearBilling, in.BillingSchemaID, &p.BillingSchemaID)
	applySlot(in.ClearService, in.ServiceSchemaID, &p.ServiceSchemaID)
	applySlot(in.ClearCommission, in.CommissionSchemaID, &p.CommissionSchemaID)
	applySlot(in.ClearSuspension, in.SuspensionSchemaID, &p.SuspensionSchemaID)
	if err := s.products.Update(ctx, p); err != nil {
		return nil, err
	}
	// Wave 81 (TC-PRD-028) — record the patch shape. Like identity's
	// summarizeUpdate, we don't have pre-edit values handy here, so we
	// emit which fields the caller touched.
	audit.SafeWrite(ctx, s.auditW, audit.Entry{
		Module:     "crm",
		RecordType: "product",
		RecordID:   p.ID.String(),
		After:      summarizeProductUpdate(in),
		Reason:     "product_updated",
	})
	return p, nil
}

// summarizeProductUpdate produces a compact one-line description of
// non-nil fields in an UpdateProductInput. Used by Wave 81 audit
// emission — the admin viewer renders this as a concise change tag
// without us having to JSON-marshal the entire patch struct.
func summarizeProductUpdate(in port.UpdateProductInput) string {
	parts := []string{}
	if in.Name != nil {
		parts = append(parts, "name")
	}
	if in.SpeedMbps != nil {
		parts = append(parts, "speed_mbps")
	}
	if in.MonthlyPrice != nil {
		parts = append(parts, "monthly_price="+ftoa(*in.MonthlyPrice))
	}
	if in.OTCPrice != nil {
		parts = append(parts, "otc_price="+ftoa(*in.OTCPrice))
	}
	if in.TempWindowHrs != nil {
		parts = append(parts, "temp_window_hrs")
	}
	if in.Active != nil {
		parts = append(parts, "active="+strconv.FormatBool(*in.Active))
	}
	if in.OnboardingSchemaID != nil || in.ClearOnboarding {
		parts = append(parts, "onboarding_schema")
	}
	if in.BillingSchemaID != nil || in.ClearBilling {
		parts = append(parts, "billing_schema")
	}
	if in.ServiceSchemaID != nil || in.ClearService {
		parts = append(parts, "service_schema")
	}
	if in.CommissionSchemaID != nil || in.ClearCommission {
		parts = append(parts, "commission_schema")
	}
	if in.SuspensionSchemaID != nil || in.ClearSuspension {
		parts = append(parts, "suspension_schema")
	}
	if len(parts) == 0 {
		return "no-op"
	}
	return "fields=" + strings.Join(parts, ",")
}

// ftoa renders a price float for audit emission. Two-decimal fixed —
// the same precision the billing surface uses for invoice totals, so
// the audit row matches what the admin sees in the UI.
func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'f', 2, 64)
}

// =====================================================================
// Leads
// =====================================================================

// CreateLead constructs a lead, runs coverage (when GPS provided), seeds
// the document checklist from the active onboarding schema (or the r1
// hardcoded fallback when no schema repo is wired), enforces the
// sales-rep type when a sales_id is supplied, and writes everything in
// one repository call. The coverage call is best-effort: failures
// degrade gracefully to a lead with no verdict so sales can still triage.
func (s *Service) CreateLead(ctx context.Context, in port.CreateLeadInput) (*port.LeadWithDocs, error) {
	l, err := domain.NewLead(in.FullName, in.Phone, in.Address)
	if err != nil {
		return nil, err
	}
	l.LeadNumber = domain.GenerateLeadNumber(time.Now())
	l.Email = in.Email
	l.NIK = in.NIK
	l.GPSLat = in.GPSLat
	l.GPSLng = in.GPSLng
	l.ProductID = in.ProductID
	l.SalesID = in.SalesID
	l.Notes = in.Notes
	l.CreatedBy = in.CreatedBy
	// Wave 76 (TC-CRM-002): capture lead_type. Default broadband per
	// NewLead constructor; only override if caller explicitly set it.
	if in.LeadType != "" {
		lt := domain.LeadType(in.LeadType)
		if lt != domain.LeadTypeBroadband && lt != domain.LeadTypeEnterprise {
			return nil, derrors.Validation("lead.lead_type_invalid",
				"lead_type must be 'broadband' or 'enterprise'")
		}
		l.LeadType = lt
	}
	if in.Source != "" {
		src := domain.LeadSource(in.Source)
		if !domain.IsValidLeadSource(src) {
			return nil, derrors.Validation("lead.source_invalid",
				"source '"+in.Source+"' is not a valid lead source")
		}
		l.Source = src
	}
	// Wave 76 (TC-CRM-007/008): when source=referral, referrer must
	// point to an active customer. Reject suspended, terminated,
	// pending_install, archived, etc. — anyone whose status isn't
	// 'active' was the prior-QA landmine.
	if l.Source == domain.LeadSourceReferral {
		if in.ReferrerCustomerID == nil {
			return nil, derrors.Validation("lead.referrer_required",
				"referrer_customer_id is required when source=referral")
		}
		if s.customers != nil {
			ref, err := s.customers.FindByID(ctx, *in.ReferrerCustomerID)
			if err != nil {
				return nil, derrors.Validation("lead.referrer_not_found",
					"referrer customer not found")
			}
			if ref.Status != "active" {
				return nil, derrors.Validation("lead.referrer_inactive",
					"referrer customer must be active — current status is "+string(ref.Status))
			}
		}
		l.ReferrerCustomerID = in.ReferrerCustomerID
	} else if in.ReferrerCustomerID != nil {
		// Tolerate but don't error — useful for cs_referral flows that
		// also want to record an existing-customer link.
		l.ReferrerCustomerID = in.ReferrerCustomerID
	}
	l.AcceptExcessCable = in.AcceptExcessCable

	// M4 r2 — sales rep type enforcement.
	// Round 1 only supports broadband leads, so when a sales rep is
	// assigned, their sales_type must be 'broadband' or 'both'.
	// 'enterprise'-only sales reps are rejected. When the gateway
	// isn't wired (r1 callers) we skip the check.
	if in.SalesID != nil && s.salesUser != nil {
		stype, err := s.salesUser.SalesTypeFor(ctx, *in.SalesID)
		if err != nil {
			return nil, err
		}
		if !salesTypeMatchesBroadband(stype) {
			return nil, derrors.Validation("lead.sales_type_mismatch",
				"sales rep type '"+stype+"' is not allowed on broadband leads")
		}
		l.SalesTypeAtCreate = stype
	}

	// Coverage check: only when GPS is provided.
	if l.GPSLat != nil && l.GPSLng != nil {
		dec, err := s.coverage.Check(ctx, *l.GPSLat, *l.GPSLng)
		if err != nil {
			// Don't fail the lead. Log via wrap (caller can surface if needed).
			// Keep lead with no verdict.
			_ = err
		} else if dec != nil {
			l.ApplyCoverage(
				dec.Verdict, dec.Snapshot,
				dec.NearestNodeID, dec.CableDistanceM, dec.ExcessCharge,
				dec.BranchID, in.AcceptExcessCable,
			)
		}
	}

	// Wave 81 (TC-CRM-011) — territory-based auto-assign. When the
	// caller didn't pick a sales rep AND coverage resolved a branch,
	// ask the gateway for the least-loaded eligible rep in that
	// territory. nil result is a soft fallback: the lead lands in the
	// unassigned queue for a sales manager to triage. We only auto-
	// assign when SalesID is nil so explicit "assign to X" calls are
	// never overridden.
	if l.SalesID == nil && l.BranchID != nil && s.salesUser != nil {
		picked, err := s.salesUser.FindForTerritory(ctx, *l.BranchID, string(l.LeadType))
		if err == nil && picked != nil {
			l.SalesID = picked
			// Capture the rep's sales_type for audit symmetry with
			// the explicit-assign branch above.
			if stype, terr := s.salesUser.SalesTypeFor(ctx, *picked); terr == nil {
				l.SalesTypeAtCreate = stype
			}
		}
	}

	// Seed the checklist. M4 r2 reads from the active onboarding schema;
	// when the schema repo isn't wired (r1 callers) we fall back to the
	// hardcoded default. The lead records which schema was used.
	var blueprints []domain.DocBlueprint
	if s.schemas != nil {
		schema, err := s.schemas.FindActive(ctx, "broadband", "standard")
		if err == nil && schema != nil {
			blueprints = schema.Content.BlueprintsFor(l.AcceptExcessCable)
			schemaID := schema.ID
			l.OnboardingSchemaID = &schemaID
		}
	}
	if blueprints == nil {
		blueprints = domain.DefaultBroadbandDocs(l.AcceptExcessCable)
	}
	rows := make([]domain.OrderDocument, 0, len(blueprints))
	for _, b := range blueprints {
		rows = append(rows, *domain.NewOrderDocument(l.ID, b))
	}

	if err := s.leads.Create(ctx, l, rows); err != nil {
		return nil, err
	}
	return s.leads.FindByID(ctx, l.ID)
}

// UpdateLead patches a lead in-flight. Mutating GPS or accept_excess does
// NOT re-run coverage automatically in round 1 — sales has to explicitly
// re-issue the lead, which is the same flow as creating again with new
// coords. This keeps the rules predictable: the coverage_snapshot you see
// is the snapshot you captured.
func (s *Service) UpdateLead(ctx context.Context, in port.UpdateLeadInput) (*port.LeadWithDocs, error) {
	lw, err := s.leads.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	l := &lw.Lead
	if in.FullName != nil {
		l.FullName = *in.FullName
	}
	if in.Phone != nil {
		l.Phone = *in.Phone
	}
	if in.Email != nil {
		l.Email = *in.Email
	}
	if in.NIK != nil {
		l.NIK = *in.NIK
	}
	if in.Address != nil {
		l.Address = *in.Address
	}
	if in.ClearGPS {
		l.GPSLat = nil
		l.GPSLng = nil
	} else {
		if in.GPSLat != nil {
			l.GPSLat = in.GPSLat
		}
		if in.GPSLng != nil {
			l.GPSLng = in.GPSLng
		}
	}
	if in.ClearProduct {
		l.ProductID = nil
	} else if in.ProductID != nil {
		l.ProductID = in.ProductID
	}
	if in.ClearSales {
		l.SalesID = nil
	} else if in.SalesID != nil {
		l.SalesID = in.SalesID
	}
	if in.Notes != nil {
		l.Notes = *in.Notes
	}
	if in.AcceptExcessCable != nil {
		l.AcceptExcessCable = *in.AcceptExcessCable
		// Wave 75 (QA TC-CRM-013): we no longer mutate Status from
		// AcceptExcessCable changes. Status is rep-driven; the flag
		// is captured for downstream invoice/order generation.
	}
	if in.Status != nil {
		// PRD §6.3 line 1448 ("Lost/closed reason tagging"): when a
		// lead is moved to `lost`, the operator must record why. QA
		// flagged this against TC-CRM-016 — the old flow accepted
		// status=lost with no reason. We treat `Notes` as the reason
		// field: it must be non-empty on this transition, either from
		// the same PATCH payload (`in.Notes`) or already on the lead
		// (e.g. the rep filled it earlier and is only flipping status
		// now). Trim guards against whitespace-only reasons.
		if *in.Status == domain.LeadStatusLost {
			reason := l.Notes
			if in.Notes != nil {
				reason = *in.Notes
			}
			if strings.TrimSpace(reason) == "" {
				return nil, derrors.Validation(
					"lead.lost_reason_required",
					"a reason is required when marking a lead as lost — fill the notes field",
				)
			}
		}
		// Wave 75 (QA TC-CRM-013/023): enforce forward-only pipeline.
		// The old code accepted any status mutation — hot→new and
		// even converted→new both passed, which QA correctly flagged
		// as regression-prone (you could "un-convert" a customer).
		// Now every transition is gated through `CanTransitionTo`.
		if err := l.CanTransitionTo(*in.Status); err != nil {
			return nil, err
		}
		l.Status = *in.Status
	}
	if err := s.leads.Update(ctx, l); err != nil {
		return nil, err
	}
	return s.leads.FindByID(ctx, in.ID)
}

func (s *Service) ListLeads(ctx context.Context, f port.LeadListFilter) ([]port.LeadWithDocs, int, error) {
	return s.leads.List(ctx, f)
}

func (s *Service) GetLead(ctx context.Context, id uuid.UUID) (*port.LeadWithDocs, error) {
	return s.leads.FindByID(ctx, id)
}

// =====================================================================
// Documents
// =====================================================================

func (s *Service) UpdateDocument(ctx context.Context, in port.UpdateDocumentInput) (*domain.OrderDocument, error) {
	return s.docs.Update(ctx, in.ID, in)
}

// =====================================================================
// Convert (the gate)
// =====================================================================

// ConvertLead is the conversion gate. It runs:
//
//  1. Status check — only qualified/potential leads can convert.
//  2. Product must be picked.
//  3. Every required document must be `submitted=true`.
//  4. Create customer (broadband, pending_install) from the lead's identity.
//  5. Create order with prices snapshot from the chosen product.
//  6. Stamp the lead 'converted' and link customer+order back.
//
// This is intentionally not transactional across all writes — same trade-off
// as Warehouse intake. If a later step fails, we surface the partial state
// (customer exists, order missing). The convert button is idempotent on
// retry only when the lead is still in qualified/potential — once converted,
// re-clicking returns the existing customer+order via CanConvert's check.
func (s *Service) ConvertLead(ctx context.Context, in port.ConvertLeadInput) (*port.ConvertLeadOutput, error) {
	lw, err := s.leads.FindByID(ctx, in.LeadID)
	if err != nil {
		return nil, err
	}
	l := lw.Lead

	if err := l.CanConvert(); err != nil {
		return nil, err
	}
	if l.ProductID == nil {
		return nil, derrors.Validation("lead.product_required",
			"pick a product before converting")
	}

	// Document gate.
	for _, d := range lw.Documents {
		if d.Required && !d.Submitted {
			return nil, derrors.Validation("lead.docs_incomplete",
				"required document "+d.Label+" is not submitted")
		}
	}

	prod, err := s.products.FindByID(ctx, *l.ProductID)
	if err != nil {
		return nil, err
	}

	cust, err := domain.NewBroadbandCustomer(l.FullName, l.Phone, l.Address)
	if err != nil {
		return nil, err
	}
	cust.Email = l.Email
	cust.NIK = l.NIK
	cust.GPSLat = l.GPSLat
	cust.GPSLng = l.GPSLng
	cust.BranchID = l.BranchID
	cust.InstallationNodeID = l.NearestNodeID

	if err := s.customers.Create(ctx, cust); err != nil {
		return nil, err
	}

	// Wave 80b (TC-SCH-011/015/023/026, TC-PRD-025): snapshot the
	// resolved schema version for each of the 5 kinds onto the new
	// customer row. The resolver honors product slot + customer
	// override; the returned schema_definitions.id pins this customer
	// to that exact version for all subsequent reads (dunning ticks,
	// commission calcs, suspension scheduler), so publishing a newer
	// schema version doesn't silently retro-rate them.
	//
	// Nil-safe: when the resolver gateway isn't wired (tests, legacy
	// callers), customers fall through to FindLatestPublished. When
	// wired but a kind fails to resolve (e.g. transient DB error),
	// we log and continue — convert is the wrong place to block on
	// a downstream concern.
	if s.schemaResolver != nil {
		locks := port.LockedSchemaVersions{}
		// One pointer per kind, populated by the resolver result.
		resolve := func(kind string, productSlot *uuid.UUID, target **uuid.UUID) {
			v, err := s.schemaResolver.ResolveVersionForCustomer(
				ctx, cust.ID, kind, productSlot,
			)
			if err == nil && v != nil {
				*target = v
			}
		}
		resolve("onboarding", prod.OnboardingSchemaID, &locks.Onboarding)
		resolve("billing", prod.BillingSchemaID, &locks.Billing)
		resolve("service", prod.ServiceSchemaID, &locks.Service)
		resolve("commission", prod.CommissionSchemaID, &locks.Commission)
		resolve("suspension", prod.SuspensionSchemaID, &locks.Suspension)
		if err := s.customers.UpdateLockedSchemaVersions(ctx, cust.ID, locks); err != nil {
			// Non-fatal — keep the customer + order, surface error
			// downstream via the resolver's DEFAULT fallback path.
			_ = err
		} else {
			// Reflect the snapshot back onto the in-memory customer
			// so the returned ConvertLeadOutput.Customer is current.
			cust.LockedOnboardingSchemaVersionID = locks.Onboarding
			cust.LockedBillingSchemaVersionID = locks.Billing
			cust.LockedServiceSchemaVersionID = locks.Service
			cust.LockedCommissionSchemaVersionID = locks.Commission
			cust.LockedSuspensionSchemaVersionID = locks.Suspension
		}
	}

	excess := 0.0
	if l.AcceptExcessCable && l.ExcessCharge != nil {
		excess = *l.ExcessCharge
	}

	// Gap B — OTC type dispatch. Round 1 hard-codes 'postpaid' here
	// because the product catalogue doesn't yet carry an otc_type. The
	// column is plumbed end-to-end (migration 0034 → repo → billing
	// gateway) so a follow-up that exposes per-product OTC type only
	// needs to flip the value passed in here.
	otcType := domain.OTCTypePostpaid
	if prod.OTCPrice == 0 {
		// Free install → free OTC. Avoids spawning a Rp 0 invoice.
		otcType = domain.OTCTypeFree
	}

	order := &domain.Order{
		ID:                uuid.New(),
		OrderNumber:       domain.GenerateOrderNumber(time.Now()),
		LeadID:            &l.ID,
		CustomerID:        cust.ID,
		ProductID:         &prod.ID,
		MonthlyPrice:      prod.MonthlyPrice,
		OTCPrice:          prod.OTCPrice,
		ExcessCharge:      excess,
		AcceptExcessCable: l.AcceptExcessCable,
		NearestNodeID:     l.NearestNodeID,
		BranchID:          l.BranchID,
		SalesID:           l.SalesID,
		Status:            domain.OrderStatusCreated,
		OTCType:           otcType,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
	if err := s.orders.Create(ctx, order); err != nil {
		return nil, err
	}

	if err := s.leads.MarkConverted(ctx, l.ID, cust.ID, order.ID, time.Now().UTC()); err != nil {
		return nil, err
	}

	// Auto-create the OTC invoice. We treat a billing failure as non-fatal:
	// the customer + order already exist, and Finance can issue the invoice
	// manually if the gateway is down. Surfacing the error here would block
	// conversion on a downstream system, which is the wrong trade-off.
	if s.billing != nil {
		label := prod.Code + " · " + prod.Name
		_ = s.billing.CreateOTCForOrder(ctx, port.OTCRequest{
			OrderID:      order.ID,
			CustomerID:   cust.ID,
			OTCType:      string(order.OTCType),
			OTCAmount:    prod.OTCPrice,
			ExcessAmount: excess,
			ProductLabel: label,
		})
	}

	return &port.ConvertLeadOutput{Customer: *cust, Order: *order}, nil
}

// =====================================================================
// Customers / Orders read-only surface
// =====================================================================

func (s *Service) ListCustomers(ctx context.Context, status string, limit, offset int) ([]domain.Customer, int, error) {
	return s.customers.List(ctx, status, limit, offset)
}

func (s *Service) GetCustomer(ctx context.Context, id uuid.UUID) (*domain.Customer, error) {
	return s.customers.FindByID(ctx, id)
}

func (s *Service) ListOrders(ctx context.Context, status string, limit, offset int) ([]domain.Order, int, error) {
	return s.orders.List(ctx, status, limit, offset)
}

func (s *Service) ListOrdersForCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]domain.Order, int, error) {
	return s.orders.ListForCustomer(ctx, customerID, limit, offset)
}

func (s *Service) GetOrder(ctx context.Context, id uuid.UUID) (*domain.Order, error) {
	return s.orders.FindByID(ctx, id)
}

package usecase

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 95 — Customer PO + Intercompany PO use cases
//
// AcceptCustomerPO is the linchpin: it flips the customer PO to
// 'accepted', reads the BOQ lines grouped by assigned_provider_company_id,
// and creates one IC-PO draft per distinct provider that is NOT the
// commercial owner. Pairs configured with auto_accept=true (and under
// threshold) are then auto-issued + auto-accepted in the same flow.
// =====================================================================

func errCustomerPONotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "customer_po.not_configured",
		"customer-PO surface is not configured for this service", nil)
}

func errIntercompanyPONotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "intercompany_po.not_configured",
		"intercompany-PO surface is not configured for this service", nil)
}

// =====================================================================
// Customer PO — list / get
// =====================================================================

func (s *Service) ListCustomerPOs(ctx context.Context, f port.CustomerPOListFilter) ([]domain.CustomerPO, int, error) {
	if s.customerPOs == nil {
		return nil, 0, errCustomerPONotConfigured()
	}
	return s.customerPOs.List(ctx, f)
}

func (s *Service) GetCustomerPO(ctx context.Context, id uuid.UUID) (*domain.CustomerPO, error) {
	if s.customerPOs == nil {
		return nil, errCustomerPONotConfigured()
	}
	return s.customerPOs.FindByID(ctx, id)
}

// UploadCustomerPO records a buyer-side PO against a Won opportunity +
// approved BOQ. Validations:
//   - opportunity must be Won (otherwise upload is premature)
//   - BOQ must be approved (otherwise downstream IC-PO automation
//     would reference an unstable snapshot)
//
// Both checks short-circuit with KindValidation so the FE shows a
// clear "wrong-state" message rather than a 500.
func (s *Service) UploadCustomerPO(ctx context.Context, in port.UploadCustomerPOInput) (*domain.CustomerPO, error) {
	if s.customerPOs == nil {
		return nil, errCustomerPONotConfigured()
	}

	// Opportunity-stage gate.
	if s.opps != nil && in.OpportunityID != uuid.Nil {
		op, err := s.opps.FindByID(ctx, in.OpportunityID)
		if err != nil {
			return nil, err
		}
		if op.Stage != domain.OpportunityStageWon {
			return nil, derrors.Validation(
				"customer_po.opportunity_not_won",
				"customer PO can only be uploaded against a Won opportunity (current stage: "+string(op.Stage)+")",
			)
		}
	}

	// BOQ-status gate.
	if s.boqs != nil && in.BOQVersionID != uuid.Nil {
		boq, err := s.boqs.FindByID(ctx, in.BOQVersionID)
		if err != nil {
			return nil, err
		}
		if boq.Status != domain.BOQStatusApproved {
			return nil, derrors.Validation(
				"customer_po.boq_not_approved",
				"customer PO can only be uploaded against an approved BOQ (current status: "+string(boq.Status)+")",
			)
		}
		// TC-CPO-002 — BOQ version must match the one the PO references.
		// We currently only validate that the BOQ exists + is approved;
		// the version-match-with-accepted-quotation check lands in a
		// follow-up wave when the quotation has a `customer_po_id` link.
		if boq.OpportunityID != in.OpportunityID {
			return nil, derrors.Validation(
				"customer_po.boq_opportunity_mismatch",
				"the supplied BOQ does not belong to the supplied opportunity",
			)
		}
	}

	po, err := domain.NewCustomerPO(
		in.OpportunityID, in.BOQVersionID, in.CommercialOwnerSubsidiaryID,
		in.PONumber,
	)
	if err != nil {
		return nil, err
	}
	po.CustomerID = in.CustomerID
	po.POValue = in.POValue
	po.FileURL = strings.TrimSpace(in.FileURL)
	po.FileHash = strings.TrimSpace(in.FileHash)
	po.UploadedBy = in.UploadedBy
	po.Notes = strings.TrimSpace(in.Notes)
	if in.UploadedBy != nil {
		now := time.Now().UTC()
		po.UploadedAt = &now
	}

	if err := s.customerPOs.Create(ctx, po); err != nil {
		return nil, err
	}

	// Audit
	uid := uuid.Nil
	if in.UploadedBy != nil {
		uid = *in.UploadedBy
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:     uid,
		Module:     "enterprise",
		RecordType: "enterprise.customer_po",
		RecordID:   po.ID.String(),
		After:      string(po.Status),
		Reason:     "customer_po_uploaded",
	})

	return po, nil
}

// ValidateCustomerPO flips received → validated (Finance gate).
func (s *Service) ValidateCustomerPO(ctx context.Context, id uuid.UUID) (*domain.CustomerPO, error) {
	if s.customerPOs == nil {
		return nil, errCustomerPONotConfigured()
	}
	po, err := s.customerPOs.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(po.Status)
	if err := po.Validate(); err != nil {
		return nil, err
	}
	if err := s.customerPOs.UpdateStatus(ctx, po); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.customer_po",
		RecordID:     po.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(po.Status),
		Reason:       "customer_po_validated",
	})
	return po, nil
}

// AcceptCustomerPO is the BIG one. In one transactional flow it:
//   1. Flips the customer PO to 'accepted'
//   2. Reads the BOQ lines, groups them by assigned_provider_company_id
//   3. For each provider that is NOT the commercial_owner_subsidiary_id,
//      creates one IC-PO draft + its line snapshot
//   4. Auto-issues + auto-accepts the IC-PO when the
//      intercompany_pairs config says auto_accept (and the total is
//      under threshold). Auto-accept fires the canonical
//      InternalTransaction recognition hook.
//   5. Writes audit rows for every state transition.
//
// We don't take a literal pgx tx because the repos are abstract; if a
// downstream step fails we leave the customer_po in 'accepted' (per
// BR-3 forward-only semantics) and surface the error so the operator
// can re-run the IC-PO automation manually.
func (s *Service) AcceptCustomerPO(
	ctx context.Context,
	id uuid.UUID,
	acceptorID *uuid.UUID,
) (*domain.CustomerPO, []domain.IntercompanyPO, error) {
	if s.customerPOs == nil {
		return nil, nil, errCustomerPONotConfigured()
	}
	if s.intercompanyPOs == nil {
		return nil, nil, errIntercompanyPONotConfigured()
	}

	po, err := s.customerPOs.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	beforeStatus := string(po.Status)

	// 1. Flip customer PO → accepted.
	if err := po.Accept(); err != nil {
		return nil, nil, err
	}
	if err := s.customerPOs.UpdateStatus(ctx, po); err != nil {
		return nil, nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.customer_po",
		RecordID:     po.ID.String(),
		FieldChanged: "status",
		Before:       beforeStatus,
		After:        string(po.Status),
		Reason:       "customer_po_accepted",
	})

	// Wave 107 — notify the sales rep (commercial owner) on accept.
	// Lookup is via the opportunity-owner backlink; nil-safe when the
	// opportunity repo isn't wired or the owner is unset.
	if s.opps != nil {
		if opp, oerr := s.opps.FindByID(ctx, po.OpportunityID); oerr == nil && opp != nil && opp.OwnerUserID != nil {
			s.Notify(ctx, domain.NewNotification(
				*opp.OwnerUserID,
				"customer_po.accepted",
				"customer_po", po.ID,
				"Customer PO accepted",
				"PO "+po.PONumber+" accepted by ops — IC-POs are issuing now.",
				domain.NotificationSeverityInfo,
			))
		}
	}

	// 2. Read BOQ lines.
	if s.boqLines == nil {
		// No BOQ-line repo wired — caller gets the accepted PO with an
		// empty IC-PO list. The operator can manually create IC-POs via
		// a follow-up endpoint (not in this wave).
		return po, []domain.IntercompanyPO{}, nil
	}
	lines, err := s.boqLines.ListByBOQ(ctx, po.BOQVersionID)
	if err != nil {
		return nil, nil, err
	}

	// 3. Group lines by assigned_provider_company_id.
	type providerGroup struct {
		providerID uuid.UUID
		lines      []domain.BOQLine
	}
	bucket := map[uuid.UUID]*providerGroup{}
	for i := range lines {
		l := &lines[i]
		if l.AssignedProviderCompanyID == nil {
			continue
		}
		pid := *l.AssignedProviderCompanyID
		// Skip lines whose provider is the commercial owner — that
		// subsidiary is fulfilling its own order, no IC-PO needed.
		if pid == po.CommercialOwnerSubsidiaryID {
			continue
		}
		g, ok := bucket[pid]
		if !ok {
			g = &providerGroup{providerID: pid}
			bucket[pid] = g
		}
		g.lines = append(g.lines, *l)
	}

	// Sort keys for deterministic iteration (so a fresh sweep produces
	// IC-POs in stable order).
	keys := make([]uuid.UUID, 0, len(bucket))
	for k := range bucket {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

	// 4. For each provider, create + maybe auto-issue+accept the IC-PO.
	out := make([]domain.IntercompanyPO, 0, len(keys))
	for _, pid := range keys {
		g := bucket[pid]

		// Header
		icPONumber := domain.GenerateICPONumber(time.Now())
		ic, err := domain.NewIntercompanyPO(
			po.ID, po.BOQVersionID,
			po.CommercialOwnerSubsidiaryID, g.providerID,
			icPONumber,
		)
		if err != nil {
			return nil, nil, err
		}
		// Compute total from BOQ-line snapshots.
		var total float64
		icLines := make([]domain.IntercompanyPOLine, 0, len(g.lines))
		for i := range g.lines {
			bl := &g.lines[i]
			lineTotal := bl.LineSellTotal()
			// Cost-side for IC-PO uses cost (the inter-subsidiary
			// transfer price), not sell — but for Wave 95 we use the
			// sell snapshot as the IC-PO total since cost may be nil on
			// some lines. Wave 95b will refine to vendor_unit_cost.
			total += lineTotal
			boqLineID := bl.ID
			ll := domain.IntercompanyPOLine{
				ID:          uuid.New(),
				ICPOID:      ic.ID,
				BOQLineID:   &boqLineID,
				Description: bl.Name,
				Qty:         bl.Quantity,
				UnitPrice:   bl.SellUnitPrice,
				LineTotal:   lineTotal,
				CreatedAt:   time.Now().UTC(),
			}
			icLines = append(icLines, ll)
		}
		ic.Total = &total
		ic.Notes = "Auto-drafted on customer_po accept (Wave 95)"

		if err := s.intercompanyPOs.Create(ctx, ic, icLines); err != nil {
			return nil, nil, err
		}
		audit.SafeWrite(ctx, s.audit, audit.Entry{
			Module:     "enterprise",
			RecordType: "enterprise.intercompany_po",
			RecordID:   ic.ID.String(),
			After:      string(ic.Status),
			Reason:     "intercompany_po_drafted",
		})

		// 4a. Auto-accept policy lookup.
		var pair *domain.IntercompanyPair
		if s.intercompanyPair != nil {
			p, err := s.intercompanyPair.FindByPair(ctx, po.CommercialOwnerSubsidiaryID, g.providerID)
			if err == nil {
				pair = p
			} else if !derrors.IsNotFound(err) {
				return nil, nil, err
			}
		}

		// Auto-accept path: issue → accept inline.
		if pair != nil && pair.MatchesAutoAccept(total) {
			if err := ic.Issue(); err != nil {
				return nil, nil, err
			}
			if err := s.intercompanyPOs.UpdateStatus(ctx, ic); err != nil {
				return nil, nil, err
			}
			if err := ic.Accept(acceptorID); err != nil {
				return nil, nil, err
			}
			if err := s.intercompanyPOs.UpdateStatus(ctx, ic); err != nil {
				return nil, nil, err
			}
			// Canonical IC-PO-accept recognition.
			if err := s.recordInternalTransactionsOnICPOAccept(ctx, ic, icLines); err != nil {
				if s.log != nil {
					s.log.Warn("internal_transactions write failed (ic_po auto-accept)",
						"ic_po_id", ic.ID.String(), "err", err.Error())
				}
			}
			audit.SafeWrite(ctx, s.audit, audit.Entry{
				Module:     "enterprise",
				RecordType: "enterprise.intercompany_po",
				RecordID:   ic.ID.String(),
				After:      string(ic.Status),
				Reason:     "intercompany_po_auto_accepted",
			})
		}

		out = append(out, *ic)
	}

	return po, out, nil
}

// RejectCustomerPO flips received | validated → rejected (terminal).
func (s *Service) RejectCustomerPO(ctx context.Context, id uuid.UUID, reason string) (*domain.CustomerPO, error) {
	if s.customerPOs == nil {
		return nil, errCustomerPONotConfigured()
	}
	po, err := s.customerPOs.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(po.Status)
	if err := po.Reject(reason); err != nil {
		return nil, err
	}
	if err := s.customerPOs.UpdateStatus(ctx, po); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.customer_po",
		RecordID:     po.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(po.Status),
		Reason:       "customer_po_rejected",
	})
	return po, nil
}

// CancelCustomerPO flips received | validated → cancelled (terminal).
func (s *Service) CancelCustomerPO(ctx context.Context, id uuid.UUID) (*domain.CustomerPO, error) {
	if s.customerPOs == nil {
		return nil, errCustomerPONotConfigured()
	}
	po, err := s.customerPOs.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(po.Status)
	if err := po.Cancel(); err != nil {
		return nil, err
	}
	if err := s.customerPOs.UpdateStatus(ctx, po); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.customer_po",
		RecordID:     po.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(po.Status),
		Reason:       "customer_po_cancelled",
	})
	return po, nil
}

// =====================================================================
// Intercompany PO — list / get / lifecycle
// =====================================================================

func (s *Service) ListIntercompanyPOs(ctx context.Context, f port.IntercompanyPOListFilter) ([]domain.IntercompanyPO, int, error) {
	if s.intercompanyPOs == nil {
		return nil, 0, errIntercompanyPONotConfigured()
	}
	return s.intercompanyPOs.List(ctx, f)
}

func (s *Service) GetIntercompanyPO(ctx context.Context, id uuid.UUID) (*domain.IntercompanyPO, []domain.IntercompanyPOLine, error) {
	if s.intercompanyPOs == nil {
		return nil, nil, errIntercompanyPONotConfigured()
	}
	po, err := s.intercompanyPOs.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	lines, err := s.intercompanyPOs.FindLines(ctx, po.ID)
	if err != nil {
		return nil, nil, err
	}
	return po, lines, nil
}

func (s *Service) IssueIntercompanyPO(ctx context.Context, id uuid.UUID) (*domain.IntercompanyPO, error) {
	if s.intercompanyPOs == nil {
		return nil, errIntercompanyPONotConfigured()
	}
	po, err := s.intercompanyPOs.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(po.Status)
	if err := po.Issue(); err != nil {
		return nil, err
	}
	if err := s.intercompanyPOs.UpdateStatus(ctx, po); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.intercompany_po",
		RecordID:     po.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(po.Status),
		Reason:       "intercompany_po_issued",
	})
	// Wave 107 — notify executing-side. We don't have a user-id for
	// the vendor_admin role on the executing subsidiary handy at this
	// layer; the consumer-side resolver (notification handler) routes
	// by role. We fire one inbox row per executing subsidiary id —
	// the recipient_user_id is the subsidiary id stand-in (NotificationSeverityInfo
	// + subject_type carries the routing hint for the FE).
	s.Notify(ctx, domain.NewNotification(
		po.ExecutingSubsidiaryID,
		"intercompany_po.issued",
		"intercompany_po", po.ID,
		"Intercompany PO issued",
		"A new IC-PO is awaiting acceptance.",
		domain.NotificationSeverityInfo,
	))
	return po, nil
}

// AcceptIntercompanyPO flips issued → accepted and fires the canonical
// InternalTransaction recognition hook (TC-VF-001/002).
//
// Wave 96 extension: this is the load-bearing trigger for the dual-EWO
// model. On accept we auto-spawn an EWO-Y bound to the IC-PO, then try
// to locate the matching EWO-X (via the parent customer PO's
// opportunity + BOQ) and chain them via PairedEWOID. If the EWO-X is
// not yet created (legacy data, or quotation-accept hook hasn't fired
// yet), we still create the EWO-Y and leave the pair null — a follow-
// up cron in a later wave will backfill.
//
// The auto-spawn is best-effort: failures are logged but do NOT roll
// back the IC-PO accept (consistent with the BR-3 forward-only rule
// the rest of the wave follows).
func (s *Service) AcceptIntercompanyPO(ctx context.Context, id uuid.UUID, byUserID *uuid.UUID) (*domain.IntercompanyPO, error) {
	if s.intercompanyPOs == nil {
		return nil, errIntercompanyPONotConfigured()
	}
	po, err := s.intercompanyPOs.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(po.Status)
	if err := po.Accept(byUserID); err != nil {
		return nil, err
	}
	if err := s.intercompanyPOs.UpdateStatus(ctx, po); err != nil {
		return nil, err
	}

	// Canonical recognition trigger (Wave 95).
	lines, lerr := s.intercompanyPOs.FindLines(ctx, po.ID)
	if lerr == nil {
		if err := s.recordInternalTransactionsOnICPOAccept(ctx, po, lines); err != nil {
			if s.log != nil {
				s.log.Warn("internal_transactions write failed (ic_po accept)",
					"ic_po_id", po.ID.String(), "err", err.Error())
			}
		}
	} else if s.log != nil {
		s.log.Warn("ic_po lines load failed during recognition",
			"ic_po_id", po.ID.String(), "err", lerr.Error())
	}

	uid := uuid.Nil
	if byUserID != nil {
		uid = *byUserID
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:       uid,
		Module:       "enterprise",
		RecordType:   "enterprise.intercompany_po",
		RecordID:     po.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(po.Status),
		Reason:       "intercompany_po_accepted",
	})

	// Wave 96 — auto-spawn EWO-Y. Best-effort. Failures are logged and
	// the IC-PO accept stays committed (operator can manually create
	// the EWO-Y later via a follow-up endpoint).
	if err := s.autoSpawnEWOYOnICPOAccept(ctx, po); err != nil {
		if s.log != nil {
			s.log.Warn("ewo_y auto-spawn failed",
				"ic_po_id", po.ID.String(), "err", err.Error())
		}
	}
	return po, nil
}

// autoSpawnEWOYOnICPOAccept materialises the executing-side EWO for an
// accepted IC-PO and tries to link it to the matching EWO-X (commercial
// side). Single EWO-Y per IC-PO — multiple BOQ lines roll up under one
// row. Wave 96 acceptance criterion: IC-PO accept produces exactly one
// EWO-Y, paired symmetrically with the EWO-X when one exists.
func (s *Service) autoSpawnEWOYOnICPOAccept(
	ctx context.Context,
	ic *domain.IntercompanyPO,
) error {
	if s.ewos == nil {
		// Phase 5 (finance/EWO) not wired — skip.
		return nil
	}

	// We need the parent customer PO so we can copy opportunity / BOQ
	// version onto the EWO-Y. Without it the join keys would be uuid.Nil
	// and the EWO row would be unjoinable.
	var opportunityID, boqVersionID uuid.UUID
	if s.customerPOs != nil {
		cpo, err := s.customerPOs.FindByID(ctx, ic.CustomerPOID)
		if err != nil {
			return err
		}
		opportunityID = cpo.OpportunityID
		boqVersionID = cpo.BOQVersionID
	} else {
		// Fall back to the IC-PO's own BOQ pointer (no opportunity).
		boqVersionID = ic.BOQVersionID
	}

	// Idempotency — re-firing the accept (e.g. operator retried after a
	// flake) must not spawn a duplicate EWO-Y. Use the (intercompany_po_id,
	// side='y') tuple as the natural key.
	existing, err := s.ewos.FindBySide(ctx, domain.EWOSideY, port.EWOListFilter{
		IntercompanyPOID: &ic.ID,
		Limit:            1,
	})
	if err == nil && len(existing) > 0 {
		return nil
	}

	// Synthesize a quotation-id from the IC-PO id so the existing NOT-NULL
	// constraint on quotation_id (legacy column from single-EWO days) is
	// satisfied. The downstream consumer doesn't follow this FK for EWO-Y
	// rows; the canonical join is via intercompany_po_id.
	synthQuotationID := uuid.New()

	ewoY, err := domain.NewEWOY(
		synthQuotationID, opportunityID, boqVersionID,
		ic.ExecutingSubsidiaryID, ic.ID,
		domain.GenerateEWONumber(time.Now()),
		"Auto-spawned on IC-PO accept (Wave 96; ic_po_id="+ic.ID.String()+")",
	)
	if err != nil {
		return err
	}
	if err := ewoY.Validate(); err != nil {
		return err
	}

	// Locate the EWO-X for the parent customer PO's opportunity. We use
	// (opportunity_id, side='x') as the search key — that's the natural
	// junction once Wave 96 lands. Multiple EWO-X rows are unusual but
	// possible (legacy data); pick the most recent.
	var pairedX *domain.EWO
	if opportunityID != uuid.Nil {
		candidates, err := s.ewos.FindBySide(ctx, domain.EWOSideX, port.EWOListFilter{
			OpportunityID: &opportunityID,
			Limit:         5,
		})
		if err == nil && len(candidates) > 0 {
			pairedX = &candidates[0]
		}
	}

	if pairedX != nil {
		if err := ewoY.LinkPair(pairedX); err != nil {
			// Bad-pair invariant — log and continue without the link.
			if s.log != nil {
				s.log.Warn("ewo_y link_pair failed; spawning unpaired",
					"ic_po_id", ic.ID.String(), "ewo_x_id", pairedX.ID.String(),
					"err", err.Error())
			}
			pairedX = nil
			ewoY.PairedEWOID = nil
		}
	}

	if err := s.ewos.Create(ctx, ewoY); err != nil {
		return err
	}
	// Persist the symmetric paired_ewo_id on the EWO-X.
	if pairedX != nil {
		if err := s.ewos.UpdatePair(ctx, pairedX.ID, ewoY.ID); err != nil {
			if s.log != nil {
				s.log.Warn("ewo_x pair update failed (ewo_y created, pair half-set)",
					"ewo_x_id", pairedX.ID.String(), "ewo_y_id", ewoY.ID.String(),
					"err", err.Error())
			}
		}
	}

	pairedTag := ""
	if pairedX != nil {
		pairedTag = " paired_ewo_x_id=" + pairedX.ID.String()
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:     "enterprise",
		RecordType: "enterprise.ewo",
		RecordID:   ewoY.ID.String(),
		After:      "auto_spawned",
		Reason:     "ewo_y.auto_spawned ic_po_id=" + ic.ID.String() + pairedTag,
	})
	return nil
}

func (s *Service) RejectIntercompanyPO(ctx context.Context, id uuid.UUID, reason string) (*domain.IntercompanyPO, error) {
	if s.intercompanyPOs == nil {
		return nil, errIntercompanyPONotConfigured()
	}
	po, err := s.intercompanyPOs.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(po.Status)
	if err := po.Reject(reason); err != nil {
		return nil, err
	}
	if err := s.intercompanyPOs.UpdateStatus(ctx, po); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.intercompany_po",
		RecordID:     po.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(po.Status),
		Reason:       "intercompany_po_rejected",
	})
	return po, nil
}

func (s *Service) CancelIntercompanyPO(ctx context.Context, id uuid.UUID) (*domain.IntercompanyPO, error) {
	if s.intercompanyPOs == nil {
		return nil, errIntercompanyPONotConfigured()
	}
	po, err := s.intercompanyPOs.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(po.Status)
	if err := po.Cancel(); err != nil {
		return nil, err
	}
	if err := s.intercompanyPOs.UpdateStatus(ctx, po); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.intercompany_po",
		RecordID:     po.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(po.Status),
		Reason:       "intercompany_po_cancelled",
	})
	return po, nil
}

// =====================================================================
// IC-PO-accept InternalTransaction recognition — canonical path
// =====================================================================

// recordInternalTransactionsOnICPOAccept is the Wave 95 IC-PO-accept
// hook for the sub-company revenue ledger. One ledger row per IC-PO
// line; recognized_at = ic_po.accepted_at; vendor_company_id =
// executing_subsidiary_id (the subsidiary fulfilling the work).
//
// Best-effort: caller logs but does NOT roll back the accept if this
// fails. Idempotent via the unique index on internal_transactions.boq_line_id
// — the same BOQ line cannot produce two ledger rows even if both the
// BOQ-approval (legacy) and IC-PO-accept (canonical) hooks fire.
func (s *Service) recordInternalTransactionsOnICPOAccept(
	ctx context.Context,
	ic *domain.IntercompanyPO,
	lines []domain.IntercompanyPOLine,
) error {
	if s.internalTxs == nil {
		return nil
	}
	if ic.AcceptedAt == nil {
		return derrors.Validation(
			"internal_transaction.ic_po_not_accepted",
			"can only recognize internal_transaction at IC-PO accept (no accepted_at)",
		)
	}
	now := *ic.AcceptedAt
	executing := ic.ExecutingSubsidiaryID
	currency := "IDR"
	out := make([]domain.InternalTransaction, 0, len(lines))
	for _, l := range lines {
		if l.BOQLineID == nil {
			// Skip lines without a BOQ-line backlink (manually inserted
			// IC-PO lines would land here; not part of Wave 95 scope).
			continue
		}
		exec := executing
		out = append(out, domain.InternalTransaction{
			ID:              uuid.New(),
			BOQVersionID:    ic.BOQVersionID,
			BOQLineID:       *l.BOQLineID,
			VendorCompanyID: &exec,
			SellAmount:      l.LineTotal,
			// Cost is not carried on IntercompanyPOLine; the legacy
			// BOQ-approval hook captures it. Wave 95b reconciliation
			// will merge the two views.
			CostAmount:   0,
			Currency:     currency,
			RecognizedAt: now,
			Notes:        "IC-PO accept recognition (Wave 95; ic_po_id=" + ic.ID.String() + ")",
			CreatedAt:    time.Now().UTC(),
			// Wave 95b — canonical source tag; the reconciler prefers
			// this row over any legacy boq_approval row for the same
			// boq_line_id and supersedes the older one.
			SourceEvent: domain.InternalTransactionSourceICPOAccept,
		})
	}
	if len(out) == 0 {
		return nil
	}
	if err := s.internalTxs.CreateBatch(ctx, out); err != nil {
		return err
	}

	// Wave 107 — provider counter fan-out. The BOQ line's
	// AssignedProviderCompanyID double-acts as a provider id from the
	// vendor registry; when the seam is wired we bump the lifetime
	// jobs + revenue counters. Failures are logged + swallowed —
	// the canonical revenue ledger (internal_transactions, just written
	// above) is already durable, so a metric miss isn't material.
	if s.vendorMetrics != nil {
		for _, l := range lines {
			if l.BOQLineID == nil {
				continue
			}
			// We don't currently load the BOQ line's provider id here —
			// the executing_subsidiary_id on the IC-PO header is the
			// canonical "who did the work" handle, which the vendor
			// schema accepts as a provider_id (the registry IDs are
			// kept compatible with subsidiary IDs). When the IDs
			// diverge in a future wave, this is the place to insert
			// the lookup.
			if err := s.vendorMetrics.IncrementCompletedJob(
				ctx, ic.ExecutingSubsidiaryID, l.LineTotal,
			); err != nil {
				if s.log != nil {
					s.log.Warn("vendor_metrics increment failed",
						"provider_id", ic.ExecutingSubsidiaryID.String(),
						"ic_po_id", ic.ID.String(),
						"err", err.Error())
				}
			}
		}
	}
	return nil
}

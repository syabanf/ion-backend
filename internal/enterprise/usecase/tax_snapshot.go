// Wave 101 — tax snapshot chain plumbing.
//
// Lives in its own file so the BOQ + Quotation + Invoice usecases can
// each call a single helper rather than duplicate the
// "resolve profile → compute snapshot → audit" sequence. The helpers
// are all nil-safe: when WithTaxResolver wasn't called, every public
// entry point short-circuits and returns nil; no usecase has to nil-
// check before calling.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// resolveSubsidiaryForBOQ best-effort looks up the subsidiary that
// should be used for the BOQ's tax stance.
//
// At BOQ approval time the customer PO usually hasn't arrived yet, so
// most calls return uuid.Nil — that's expected and the snapshot
// stamping is deferred. Once the customer PO is uploaded (and carries
// commercial_owner_subsidiary_id), the IC-PO acceptance path
// re-stamps via the same helper. Both paths are idempotent through
// ApplyTaxProfile + the unique snapshot-hash invariant.
func (s *Service) resolveSubsidiaryForBOQ(ctx context.Context, boqVersionID uuid.UUID) uuid.UUID {
	if s.customerPOs == nil {
		return uuid.Nil
	}
	// We use the broad list filter — there's typically zero or one
	// customer PO per BOQ version. ListByBOQ-equivalent is a small
	// scan; cost is negligible per approval (one-shot).
	pos, _, err := s.customerPOs.List(ctx, port.CustomerPOListFilter{
		BOQVersionID: &boqVersionID,
		Limit:        1,
	})
	if err != nil || len(pos) == 0 {
		return uuid.Nil
	}
	return pos[0].CommercialOwnerSubsidiaryID
}

// stampBOQTaxSnapshotIfResolvable stamps the tax_profile + snapshot
// hash onto the BOQ when a resolver is wired AND we can determine the
// subsidiary. Best-effort: every failure mode is logged + swallowed
// so a missing tax profile never blocks BOQ approval.
//
// Audit row is emitted regardless of outcome (stamp / waived / error)
// so the operator can grep `tax_snapshot.*` events for any approval.
func (s *Service) stampBOQTaxSnapshotIfResolvable(ctx context.Context, b *domain.BOQ) {
	if s.taxResolver == nil {
		return
	}
	subsidiaryID := s.resolveSubsidiaryForBOQ(ctx, b.ID)
	if subsidiaryID == uuid.Nil {
		// Deferred — the IC-PO accept path will stamp once the
		// customer PO arrives. Not an error.
		return
	}
	snap, err := s.taxResolver.ActiveProfile(ctx, subsidiaryID, time.Now().UTC())
	if err != nil {
		if derrors.IsNotFound(err) {
			// No profile for this subsidiary at this time — Non-PKP
			// fallback. Audit so the operator can see the chain decision.
			audit.SafeWrite(ctx, s.audit, audit.Entry{
				Module:       "enterprise",
				RecordType:   "boq",
				RecordID:     b.ID.String(),
				FieldChanged: "tax_snapshot_hash",
				After:        "",
				Reason:       "tax_profile.not_found subsidiary_id=" + subsidiaryID.String(),
			})
			return
		}
		if s.log != nil {
			s.log.Warn("tax_resolver active_profile failed",
				"boq_version_id", b.ID.String(),
				"subsidiary_id", subsidiaryID.String(),
				"err", err.Error())
		}
		return
	}
	if snap == nil {
		return
	}
	domainSnap := domain.TaxSnapshot{
		ProfileID:     snap.ProfileID,
		IsPKP:         snap.IsPKP,
		PPNRate:       snap.PPNRate,
		PPh23Rate:     snap.PPh23Rate,
		EffectiveFrom: snap.EffectiveFrom,
	}
	if err := b.ApplyTaxProfile(domainSnap); err != nil {
		if s.log != nil {
			s.log.Warn("boq apply_tax_profile failed",
				"boq_version_id", b.ID.String(), "err", err.Error())
		}
		return
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "boq",
		RecordID:     b.ID.String(),
		FieldChanged: "tax_snapshot_hash",
		After:        *b.TaxSnapshotHash,
		Reason:       "tax_profile.stamped profile_id=" + snap.ProfileID.String(),
	})
}

// inheritTaxSnapshotForQuotation copies the parent BOQ's
// tax_snapshot_hash onto a fresh Quotation. Called by
// GenerateQuotation. Tolerant of an empty BOQ snapshot (the chain is
// optional in deployments without a tax resolver).
func (s *Service) inheritTaxSnapshotForQuotation(boq *domain.BOQ, q *domain.Quotation) error {
	if boq.TaxSnapshotHash == nil || *boq.TaxSnapshotHash == "" {
		return nil
	}
	return q.InheritTaxSnapshot(*boq.TaxSnapshotHash)
}

// inheritTaxSnapshotForInvoice copies the Quotation's tax_snapshot_hash
// onto a fresh Invoice. Called by IssueInvoice. Same tolerance as
// the BOQ→Quotation step.
func (s *Service) inheritTaxSnapshotForInvoice(q *domain.Quotation, inv *domain.Invoice) error {
	if q.TaxSnapshotHash == nil || *q.TaxSnapshotHash == "" {
		return nil
	}
	return inv.InheritTaxSnapshot(*q.TaxSnapshotHash)
}

package http

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/vendormgmt/domain"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// rfc3339Ptr formats a *time.Time as a *string in RFC 3339, or nil.
func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func toProviderDTO(p *domain.Provider, caps []domain.ProviderCapability) map[string]any {
	if p == nil {
		return nil
	}
	out := map[string]any{
		"id":                   p.ID.String(),
		"name":                 p.Name,
		"npwp":                 p.NPWP,
		"contact_email":        p.ContactEmail,
		"contact_phone":        p.ContactPhone,
		"status":               string(p.Status),
		"kyc_completed":        p.KYCCompleted,
		"capabilities":         p.Capabilities,
		"rating_score":         p.RatingScore,
		"total_completed_jobs": p.TotalCompletedJobs,
		"total_revenue":        p.TotalRevenue,
		"created_at":           p.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":           p.UpdatedAt.UTC().Format(time.RFC3339),
		"suspended_at":         rfc3339Ptr(p.SuspendedAt),
		"suspended_reason":     p.SuspendedReason,
	}
	if caps != nil {
		details := make([]map[string]any, 0, len(caps))
		for _, c := range caps {
			details = append(details, map[string]any{
				"id":              c.ID.String(),
				"capability_key":  c.CapabilityKey,
				"capability_name": c.CapabilityName,
				"max_capacity":    c.MaxCapacity,
			})
		}
		out["capability_details"] = details
	}
	return out
}

func toSubmissionDTO(s *domain.InputSubmission) map[string]any {
	if s == nil {
		return nil
	}
	out := map[string]any{
		"id":               s.ID.String(),
		"opportunity_id":   s.OpportunityID.String(),
		"provider_id":      s.ProviderID.String(),
		"unit_cost":        s.UnitCost,
		"notes":            s.Notes,
		"status":           string(s.Status),
		"submitted_at":     s.SubmittedAt.UTC().Format(time.RFC3339),
		"reviewed_at":      rfc3339Ptr(s.ReviewedAt),
		"rejection_reason": s.RejectionReason,
	}
	if s.BOQLineID != nil {
		out["boq_line_id"] = s.BOQLineID.String()
	}
	if s.SubmittedBy != nil {
		out["submitted_by"] = s.SubmittedBy.String()
	}
	if s.ReviewedBy != nil {
		out["reviewed_by"] = s.ReviewedBy.String()
	}
	return out
}

// ---------------------------------------------------------------------
// Helpers — namespaced to dodge collisions if multiple HTTP packages
// land in the same binary later.
// ---------------------------------------------------------------------

func parseUUID(s, field string) (uuid.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, errors.Validation(field+".invalid_uuid", field+" is not a valid uuid")
	}
	return u, nil
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// actorUserID pulls the JWT subject (user_id) out of the request
// context. Returns nil for unauthenticated requests so the caller can
// 403 explicitly rather than panicking.
func actorUserID(ctx context.Context) *uuid.UUID {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return nil
	}
	id := c.UserID
	if id == uuid.Nil {
		return nil
	}
	return &id
}


// M5 round-2 service methods: reschedule, OTP-based remote BAST sign-off,
// and the SLA-breach queue. The reschedule + SLA repos and the BAST repo
// (already from r1) are wired here. OTP generation lives in this file
// to keep the crypto choice in one place.
package usecase

import (
	"context"
	"crypto/rand"
	"math/big"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithReschedule attaches the reschedule repo so RescheduleWO,
// ListRescheduleHistory, and ListSLABreaches become operative. Optional
// (r1 callers stay nil-safe).
func (s *Service) WithReschedule(rs port.RescheduleRepository) *Service {
	s.reschedules = rs
	return s
}

// generate6DigitOTP returns a 6-digit numeric string + its bcrypt hash.
// We use crypto/rand for the digits (not math/rand) so the code isn't
// guessable. Bcrypt cost is the package default — fine for one-shot
// codes that are stored briefly.
func generate6DigitOTP() (plain, hashed string, err error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", "", err
	}
	plain = padLeft6(n.Int64())
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", "", err
	}
	return plain, string(h), nil
}

func padLeft6(n int64) string {
	out := make([]byte, 6)
	for i := 5; i >= 0; i-- {
		out[i] = byte('0' + n%10)
		n /= 10
	}
	return string(out)
}

// =====================================================================
// Reschedule
// =====================================================================

// RescheduleWO records an audit row and updates the WO's scheduled_date.
// The WO status is set to 'rescheduled' so the Team Leader can re-assign
// or re-route. Per state machine, only assigned/dispatched/in_progress
// can be rescheduled.
func (s *Service) RescheduleWO(ctx context.Context, in port.RescheduleWOInput) (*port.WODetail, error) {
	if s.reschedules == nil {
		return nil, derrors.New(derrors.KindInternal, "field.r2_not_wired",
			"reschedule repo not configured")
	}
	d, err := s.wos.FindByID(ctx, in.WOID)
	if err != nil {
		return nil, err
	}
	if err := d.WO.AssertCanReschedule(); err != nil {
		return nil, err
	}

	original := d.WO.ScheduledDate
	newDate := in.NewDate.UTC()
	w := &d.WO
	w.ScheduledDate = &newDate
	w.Status = domain.WOStatusRescheduled
	if err := s.wos.Update(ctx, w); err != nil {
		return nil, err
	}

	by := in.RescheduledBy
	rs := &domain.Reschedule{
		ID:            uuid.New(),
		WOID:          in.WOID,
		Reason:        in.Reason,
		Notes:         in.Notes,
		OriginalDate:  original,
		NewDate:       &newDate,
		RescheduledBy: &by,
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.reschedules.Create(ctx, rs); err != nil {
		return nil, err
	}
	return s.GetWO(ctx, in.WOID)
}

func (s *Service) ListRescheduleHistory(ctx context.Context, woID uuid.UUID) ([]domain.Reschedule, error) {
	if s.reschedules == nil {
		return nil, derrors.New(derrors.KindInternal, "field.r2_not_wired",
			"reschedule repo not configured")
	}
	return s.reschedules.ListForWO(ctx, woID)
}

// =====================================================================
// OTP verification (remote BAST sign-off)
// =====================================================================

// VerifyBASTOTP compares the submitted 6-digit code against the bcrypt
// hash stored on the BAST. Stamps otp_verified_at on success. NOC can
// then verify the BAST as normal — the payment gate still applies.
func (s *Service) VerifyBASTOTP(ctx context.Context, in port.VerifyOTPInput) (*domain.BAST, error) {
	b, err := s.basts.FindByID(ctx, in.BASTID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, derrors.NotFound("bast.not_found", "bast not found")
	}
	if b.SignOffMode != domain.SignOffRemote {
		return nil, derrors.Conflict("bast.otp_unused",
			"OTP verification only applies to remote sign-off")
	}
	if b.OTPVerifiedAt != nil {
		return nil, derrors.Conflict("bast.otp_already_verified",
			"OTP already verified for this BAST")
	}
	if b.OTPCode == "" {
		return nil, derrors.Conflict("bast.otp_missing",
			"no OTP recorded on this BAST (generated only at submit time)")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(b.OTPCode), []byte(in.Code)); err != nil {
		return nil, derrors.Validation("bast.otp_mismatch", "OTP code is incorrect")
	}
	if err := s.basts.VerifyOTP(ctx, in.BASTID); err != nil {
		return nil, err
	}
	return s.basts.FindByID(ctx, in.BASTID)
}

// =====================================================================
// SLA breach view
// =====================================================================

func (s *Service) ListSLABreaches(ctx context.Context, limit, offset int) ([]port.WODetail, int, error) {
	if s.reschedules == nil {
		return nil, 0, derrors.New(derrors.KindInternal, "field.r2_not_wired",
			"reschedule repo not configured")
	}
	return s.reschedules.ListSLABreaches(ctx, limit, offset)
}

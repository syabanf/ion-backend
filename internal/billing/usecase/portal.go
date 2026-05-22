// M6 r3 — customer-portal self-service termination usecase.
//
// This is the only "public" surface on billing-svc today — no JWT,
// authentication is by OTP-to-phone. The two methods here pair up:
//
//   1. RequestTerminationOTP: customer typed (customer_number, phone).
//      Resolve to customer_id, mint a 6-digit OTP, bcrypt-hash it,
//      persist (TTL 10 min), and return the metadata. Plaintext goes
//      out-of-band — round-3 just logs it; round-4 sends WhatsApp/SMS.
//
//   2. ConfirmTermination: customer typed (customer_number, otp, reason).
//      Resolve customer, find the active OTP, bcrypt-compare, mark
//      verified, then invoke the standard RequestVoluntaryTermination.
//
// Attempts are capped at 5 per OTP to defeat brute-force; the per-IP
// rate limit on the route prevents distributed guessing.

package usecase

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// PortalOTPTTL controls how long a freshly minted OTP stays valid.
const PortalOTPTTL = 10 * time.Minute

// PortalOTPMaxAttempts caps wrong-code retries before the customer must
// request a fresh OTP. Five is a usability-vs-security balance — the
// customer can typo twice without restarting the flow.
const PortalOTPMaxAttempts = 5

// PortalOTPRequestWindow + PortalOTPMaxRequestsPerWindow cap how often
// the same customer can mint a fresh OTP. The per-IP RL upstream
// protects against single-source bursts; this cap blocks the
// distributed-attack shape where a botnet rotates IPs to mint fresh
// OTPs for one known customer_number indefinitely.
//
// Five per hour leaves room for a customer who triggers a few re-sends
// (lost SMS, typoed code → fresh request) without hitting the cap.
const (
	PortalOTPRequestWindow        = time.Hour
	PortalOTPMaxRequestsPerWindow = 5
)

// WithPortal attaches the customer-portal repos. Nil-safe: callers that
// don't wire it get a clean "not configured" error.
func (s *Service) WithPortal(
	otps port.CustomerOTPRepository,
	lookup port.CustomerLookupGateway,
	includeDevOTP bool,
) *Service {
	s.portalOTPs = otps
	s.portalLookup = lookup
	s.includeDevOTP = includeDevOTP
	return s
}

func (s *Service) RequestTerminationOTP(ctx context.Context, in port.PortalRequestTerminationOTPInput) (*port.PortalRequestTerminationOTPOutput, error) {
	if s.portalOTPs == nil || s.portalLookup == nil {
		return nil, derrors.New(derrors.KindInternal, "portal.not_wired",
			"customer portal not configured")
	}
	num := strings.TrimSpace(in.CustomerNumber)
	phone := strings.TrimSpace(in.Phone)
	if num == "" || phone == "" {
		return nil, derrors.Validation("portal.input",
			"customer_number and phone are required")
	}

	customerID, err := s.portalLookup.FindCustomerByNumberAndPhone(ctx, num, phone)
	if err != nil {
		return nil, err
	}

	// Per-customer rate limit. The per-IP middleware upstream catches
	// the easy attack shapes; this catches the distributed one where a
	// rotating IP set targets a single customer_number. We don't
	// distinguish between "fresh request" and "re-send" — the cap is
	// on requests reaching this path, regardless of how the customer
	// got here.
	since := time.Now().UTC().Add(-PortalOTPRequestWindow)
	if n, err := s.portalOTPs.CountRequestsSince(ctx, customerID, "termination", since); err == nil {
		if n >= PortalOTPMaxRequestsPerWindow {
			// retry_after is the conservative upper bound: the full
			// window. Computing the exact freed-token moment would
			// need a second query against the oldest counted row,
			// which isn't worth the extra DB hop on an already-
			// throttled path. The client can poll if it wants tighter
			// resolution.
			retryAfter := int(PortalOTPRequestWindow.Seconds())
			return nil, derrors.New(derrors.KindRateLimited,
				"portal.too_many_requests",
				"too many OTP requests for this customer; try again later").
				WithRetryAfter(retryAfter)
		}
	}

	// Opportunistic janitor — we only delete on the request path so a
	// burst of legitimate verifies doesn't pay the cleanup cost. Misses
	// don't matter; the table only grows by one row per request.
	_, _ = s.portalOTPs.DeleteExpired(ctx, time.Now().UTC().Add(-time.Hour))

	plain, hashed, err := generate6DigitOTPPortal()
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "portal.otp_gen",
			"generate otp", err)
	}
	now := time.Now().UTC()
	rec := &port.CustomerOTPRecord{
		ID:         uuid.New(),
		CustomerID: customerID,
		Purpose:    "termination",
		OTPHash:    hashed,
		ExpiresAt:  now.Add(PortalOTPTTL),
		CreatedAt:  now,
	}
	if err := s.portalOTPs.Create(ctx, rec); err != nil {
		return nil, err
	}

	// Surface the plaintext only when the binary was started with
	// IncludeDevOTP=true (local dev + e2e). Production paths leave
	// DevOTP empty and rely on out-of-band delivery.
	out := &port.PortalRequestTerminationOTPOutput{
		CustomerID: customerID,
		ExpiresAt:  rec.ExpiresAt,
	}
	if s.includeDevOTP {
		out.DevOTP = plain
	}
	if s.log != nil {
		// Log the plaintext at INFO so a dev tail can pick it up while
		// the WhatsApp gateway is not yet live. We deliberately log it
		// because the customer hasn't been authenticated yet — there's
		// no other way to deliver it in round-3.
		s.log.Info("portal otp minted",
			"customer_id", customerID, "otp", plain, "expires_at", rec.ExpiresAt)
	}
	return out, nil
}

func (s *Service) ConfirmTermination(ctx context.Context, in port.PortalConfirmTerminationInput) (*port.PortalConfirmTerminationOutput, error) {
	if s.portalOTPs == nil || s.portalLookup == nil {
		return nil, derrors.New(derrors.KindInternal, "portal.not_wired",
			"customer portal not configured")
	}
	num := strings.TrimSpace(in.CustomerNumber)
	otp := strings.TrimSpace(in.OTP)
	if num == "" || otp == "" {
		return nil, derrors.Validation("portal.input",
			"customer_number and otp are required")
	}
	// Resolve customer by number-only here — we already proved phone
	// possession at request time. Re-checking phone now would double
	// the friction without adding security.
	customerID, err := s.portalLookupByNumber(ctx, num)
	if err != nil {
		return nil, err
	}

	rec, err := s.portalOTPs.FindActive(ctx, customerID, "termination")
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, derrors.NotFound("portal.otp_expired",
			"no active OTP — request a fresh one")
	}
	if rec.Attempts >= PortalOTPMaxAttempts {
		return nil, derrors.Conflict("portal.otp_attempts",
			"too many wrong attempts; request a fresh OTP")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(rec.OTPHash), []byte(otp)); err != nil {
		// Don't reveal whether the code was wrong vs the row is stale —
		// both surface as the same client error. We do bump attempts
		// so brute-force burns through the cap.
		_ = s.portalOTPs.IncrementAttempts(ctx, rec.ID)
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return nil, derrors.Validation("portal.otp_invalid", "invalid OTP")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "portal.otp_compare",
			"otp compare", err)
	}

	if err := s.portalOTPs.MarkVerified(ctx, rec.ID); err != nil {
		return nil, err
	}

	// OTP verified — now invoke the staff-side termination flow.
	// RequestedBy=uuid.Nil because there is no operator; the request
	// row will record kind=voluntary and notes='customer self-service'.
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "customer self-service"
	} else {
		reason = "customer self-service: " + reason
	}
	t, err := s.RequestVoluntaryTermination(ctx, port.RequestTerminationInput{
		CustomerID:  customerID,
		Reason:      reason,
		RequestedBy: uuid.Nil,
	})
	if err != nil {
		return nil, err
	}
	return &port.PortalConfirmTerminationOutput{
		TerminationID: t.ID,
		Status:        string(t.Status),
	}, nil
}

// portalLookupByNumber resolves a customer by number alone. Called only
// from the OTP confirm leg, where phone possession is already proved by
// the OTP gate.
func (s *Service) portalLookupByNumber(ctx context.Context, customerNumber string) (uuid.UUID, error) {
	return s.portalLookup.FindCustomerByNumber(ctx, customerNumber)
}

// generate6DigitOTPPortal mirrors the field-service helper; we keep a
// separate copy in this package so the portal flow doesn't pull in
// the field package as a dependency.
func generate6DigitOTPPortal() (plain, hashed string, err error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", "", err
	}
	plain = padLeft6Portal(n.Int64())
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", "", err
	}
	return plain, string(h), nil
}

func padLeft6Portal(n int64) string {
	out := make([]byte, 6)
	for i := 5; i >= 0; i-- {
		out[i] = byte('0' + n%10)
		n /= 10
	}
	return string(out)
}

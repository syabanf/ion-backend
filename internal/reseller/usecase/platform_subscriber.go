package usecase

import (
	"context"
	"encoding/csv"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	"github.com/ion-core/backend/pkg/errors"
)

// SubscriberService implements port.SubscriberUseCase. Every method
// resolves the tenant from request context (via TenantFromContext,
// supplied through the contextKey shared with the HTTP layer) and
// scopes the underlying repo calls. Cross-tenant access through the
// service surface is structurally impossible — the postgres adapters
// refuse uuid.Nil tenant filters.
//
// Note: this service intentionally does NOT take a TenantFromContext
// function as a dependency. It calls the package-level
// TenantFromContext helper exported by the HTTP adapter. To avoid a
// cyclic import, we accept the tenant id as a function value at
// construction. The HTTP layer wires it via:
//
//	NewSubscriberService(..., http.TenantFromContext)
//
// This is the established pattern in identity / crm / warehouse.
type SubscriberService struct {
	subscribers port.SubscriberRepository
	imports     port.SubscriberImportRepository
	tenantOf    func(ctx context.Context) uuid.UUID
}

// NewSubscriberService wires the dependencies. tenantOf is the
// per-request tenant resolver — the HTTP adapter passes its
// TenantFromContext helper here so the package boundary stays clean
// (no cross-imports from usecase → adapter/http).
func NewSubscriberService(subs port.SubscriberRepository, imp port.SubscriberImportRepository, tenantOf func(ctx context.Context) uuid.UUID) *SubscriberService {
	return &SubscriberService{subscribers: subs, imports: imp, tenantOf: tenantOf}
}

var _ port.SubscriberUseCase = (*SubscriberService)(nil)

// guardTenant returns the request-context tenant or refuses the call
// with an Unauthorized error. Every method below funnels through this
// helper so the auth-missing check is single-sourced.
func (s *SubscriberService) guardTenant(ctx context.Context) (uuid.UUID, error) {
	tenant := s.tenantOf(ctx)
	if tenant == uuid.Nil {
		return uuid.Nil, errors.Unauthorized("session.missing", "tenant not resolved")
	}
	return tenant, nil
}

// CreateSubscriber validates the body's reseller id matches the
// request-context tenant — a tampered body that names a different
// tenant is refused with Forbidden / subscriber.cross_tenant. The
// repo-level check (refusing uuid.Nil) is the second layer of defense.
func (s *SubscriberService) CreateSubscriber(ctx context.Context, in port.CreateSubscriberInput) (*domain.Subscriber, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	if in.ResellerAccountID != uuid.Nil && in.ResellerAccountID != tenant {
		return nil, errors.Forbidden("subscriber.cross_tenant", "subscriber must belong to current tenant")
	}
	sub, err := domain.NewSubscriber(tenant, in.CustomerName, in.CustomerEmail, in.CustomerPhone, in.MonthlyFee)
	if err != nil {
		return nil, err
	}
	sub.AddressLine = strings.TrimSpace(in.AddressLine)
	sub.Notes = strings.TrimSpace(in.Notes)
	sub.SubAreaID = in.SubAreaID
	sub.ServicePlanID = in.ServicePlanID
	if err := s.subscribers.Create(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

// ListMySubscribers force-scopes the filter to the request tenant —
// even if the caller passes a different ResellerAccountID in the
// filter (which the HTTP layer never does), we overwrite it. The repo
// refuses uuid.Nil as a third safety net.
func (s *SubscriberService) ListMySubscribers(ctx context.Context, f port.SubscriberListFilter) ([]domain.Subscriber, int, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, 0, err
	}
	f.ResellerAccountID = tenant
	return s.subscribers.List(ctx, f)
}

// GetMySubscriber refuses to leak a cross-tenant id. The repo's
// FindForReseller returns NotFound when the id doesn't belong to this
// tenant — which is what we want: the row's existence stays invisible.
func (s *SubscriberService) GetMySubscriber(ctx context.Context, id uuid.UUID) (*domain.Subscriber, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	return s.subscribers.FindForReseller(ctx, tenant, id)
}

// UpdateMySubscriber re-runs the tenant guard at every step: fetch the
// row via FindForReseller (NotFound on cross-tenant), apply field
// changes, persist. Status transitions go through Suspend / Reactivate
// / Terminate — this method explicitly refuses status updates so the
// state machine isn't bypassed.
func (s *SubscriberService) UpdateMySubscriber(ctx context.Context, id uuid.UUID, fields port.UpdateSubscriberInput) (*domain.Subscriber, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := s.subscribers.FindForReseller(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if fields.CustomerName != nil {
		n := strings.TrimSpace(*fields.CustomerName)
		if n == "" {
			return nil, errors.Validation("subscriber.name_required", "customer_name is required")
		}
		sub.CustomerName = n
	}
	if fields.CustomerEmail != nil {
		sub.CustomerEmail = strings.TrimSpace(*fields.CustomerEmail)
	}
	if fields.CustomerPhone != nil {
		sub.CustomerPhone = strings.TrimSpace(*fields.CustomerPhone)
	}
	if fields.AddressLine != nil {
		sub.AddressLine = strings.TrimSpace(*fields.AddressLine)
	}
	if fields.SubAreaID != nil {
		sub.SubAreaID = fields.SubAreaID
	}
	if fields.ServicePlanID != nil {
		sub.ServicePlanID = fields.ServicePlanID
	}
	if fields.MonthlyFee != nil {
		if *fields.MonthlyFee < 0 {
			return nil, errors.Validation("subscriber.fee_negative", "monthly_fee must be >= 0")
		}
		sub.MonthlyFee = *fields.MonthlyFee
	}
	if fields.Notes != nil {
		sub.Notes = strings.TrimSpace(*fields.Notes)
	}
	sub.UpdatedAt = time.Now().UTC()
	if err := s.subscribers.Update(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *SubscriberService) SuspendMySubscriber(ctx context.Context, id uuid.UUID, reason string) (*domain.Subscriber, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := s.subscribers.FindForReseller(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if err := sub.Suspend(reason, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.subscribers.UpdateStatus(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *SubscriberService) ReactivateMySubscriber(ctx context.Context, id uuid.UUID) (*domain.Subscriber, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := s.subscribers.FindForReseller(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if err := sub.Reactivate(time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.subscribers.UpdateStatus(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *SubscriberService) TerminateMySubscriber(ctx context.Context, id uuid.UUID) (*domain.Subscriber, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := s.subscribers.FindForReseller(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if err := sub.Terminate(time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.subscribers.UpdateStatus(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

// ImportSubscribersCSV parses a CSV with columns
//   customer_name, customer_email, customer_phone, address_line, monthly_fee
// (case-insensitive header match; column order flexible).
//
// Behaviour:
//   1. Parse the header. Header mismatch (missing customer_name) =
//      status=failed, nothing persisted.
//   2. Create the import row up-front in status=processing so a
//      crashed import leaves a tombstone.
//   3. Row-by-row: validate name (required) + monthly_fee (numeric,
//      >= 0). Successful rows insert a subscriber; failed rows
//      accumulate ImportRowError entries.
//   4. Finalize the import row with the per-status counts.
//
// We deliberately do NOT roll back successful rows when later rows
// fail — partial imports are a first-class status. The error_summary
// jsonb is the source of truth for which rows failed.
//
// Error policy: row-level errors are derrors.Validation values; we
// trim them down to a {row, field, reason} struct stored in
// error_summary. The CSV-level error (e.g. malformed header) bubbles
// up as the top-level return.
func (s *SubscriberService) ImportSubscribersCSV(ctx context.Context, csvBytes []byte) (*domain.SubscriberImport, error) {
	tenant, err := s.guardTenant(ctx)
	if err != nil {
		return nil, err
	}
	if len(csvBytes) == 0 {
		return nil, errors.Validation("subscriber_import.empty", "csv body is empty")
	}

	reader := csv.NewReader(strings.NewReader(string(csvBytes)))
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		return nil, errors.Validation("subscriber_import.header_unreadable", "could not read csv header")
	}
	hdr := indexHeaders(header)
	if _, ok := hdr["customer_name"]; !ok {
		return nil, errors.Validation("subscriber_import.header_missing_name", "csv must include a 'customer_name' column")
	}

	im, err := domain.NewSubscriberImport(tenant, "csv", nil)
	if err != nil {
		return nil, err
	}
	im.MarkProcessing()
	if err := s.imports.Create(ctx, im); err != nil {
		return nil, err
	}

	total := 0
	ok := 0
	rowErrs := []domain.ImportRowError{}
	rowNum := 1 // header is row 1 in human terms; first data row is 2
	for {
		rec, rErr := reader.Read()
		if rErr == io.EOF {
			break
		}
		rowNum++
		total++
		if rErr != nil {
			rowErrs = append(rowErrs, domain.ImportRowError{
				Row:    rowNum,
				Reason: "csv parse error: " + rErr.Error(),
			})
			continue
		}
		sub, rowErr := s.csvRowToSubscriber(tenant, hdr, rec, rowNum)
		if rowErr != nil {
			rowErrs = append(rowErrs, *rowErr)
			continue
		}
		if err := s.subscribers.Create(ctx, sub); err != nil {
			rowErrs = append(rowErrs, domain.ImportRowError{
				Row:    rowNum,
				Reason: "insert failed: " + err.Error(),
			})
			continue
		}
		ok++
	}

	errs := total - ok
	im.Finalize(total, ok, errs, rowErrs, time.Now().UTC())
	if err := s.imports.UpdateStatus(ctx, im); err != nil {
		return nil, err
	}
	return im, nil
}

// csvRowToSubscriber converts one CSV record into a domain Subscriber.
// Returns a row-level error (NOT a Go error) when a field fails
// validation so the caller can accumulate them rather than aborting
// the whole import. A nil return + nil error means "ignore this row"
// (currently never used; included for symmetry).
func (s *SubscriberService) csvRowToSubscriber(tenant uuid.UUID, hdr map[string]int, rec []string, rowNum int) (*domain.Subscriber, *domain.ImportRowError) {
	get := func(field string) string {
		idx, ok := hdr[field]
		if !ok || idx >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[idx])
	}
	name := get("customer_name")
	if name == "" {
		return nil, &domain.ImportRowError{Row: rowNum, Field: "customer_name", Reason: "required"}
	}
	feeStr := get("monthly_fee")
	fee := 0.0
	if feeStr != "" {
		v, err := strconv.ParseFloat(feeStr, 64)
		if err != nil {
			return nil, &domain.ImportRowError{Row: rowNum, Field: "monthly_fee", Reason: "not a number"}
		}
		if v < 0 {
			return nil, &domain.ImportRowError{Row: rowNum, Field: "monthly_fee", Reason: "must be >= 0"}
		}
		fee = v
	}
	sub, err := domain.NewSubscriber(tenant, name, get("customer_email"), get("customer_phone"), fee)
	if err != nil {
		// Domain refused — surface as a row-level error. The Error
		// interface from pkg/errors returns "kind: msg".
		return nil, &domain.ImportRowError{Row: rowNum, Field: "", Reason: err.Error()}
	}
	sub.AddressLine = get("address_line")
	return sub, nil
}

// indexHeaders normalizes CSV headers (lowercase, strip leading/trailing
// whitespace) and maps them to column index. Duplicate headers keep the
// last index (consistent with encoding/csv reader semantics).
func indexHeaders(row []string) map[string]int {
	out := make(map[string]int, len(row))
	for i, raw := range row {
		k := strings.ToLower(strings.TrimSpace(raw))
		out[k] = i
	}
	return out
}


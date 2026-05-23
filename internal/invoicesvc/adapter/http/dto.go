package http

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
	"github.com/ion-core/backend/pkg/httpserver"
)

// ---------------------------------------------------------------------
// Snapshots
// ---------------------------------------------------------------------

type createSnapshotRequest struct {
	InvoiceID        string                      `json:"invoice_id"`
	SchemaSnapshotID string                      `json:"schema_snapshot_id,omitempty"`
	LineItems        []snapshotLineItemDTO       `json:"line_items"`
}

type snapshotLineItemDTO struct {
	Description string  `json:"description"`
	ItemType    string  `json:"item_type"`
	Quantity    float64 `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
	Amount      float64 `json:"amount"`
}

type snapshotDTO struct {
	ID               string                `json:"id"`
	InvoiceID        string                `json:"invoice_id"`
	CustomerID       string                `json:"customer_id,omitempty"`
	PlanID           string                `json:"plan_id,omitempty"`
	SchemaSnapshotID string                `json:"schema_snapshot_id,omitempty"`
	SnapshottedAt    string                `json:"snapshotted_at"`
	TotalAmount      float64               `json:"total_amount"`
	LineItems        []snapshotLineItemDTO `json:"line_items"`
	StatusAtSnapshot string                `json:"status_at_snapshot,omitempty"`
	SourceModule     string                `json:"source_module"`
}

func toSnapshotDTO(s domain.InvoiceSnapshot) snapshotDTO {
	lines := make([]snapshotLineItemDTO, 0, len(s.LineItems))
	for _, l := range s.LineItems {
		lines = append(lines, snapshotLineItemDTO{
			Description: l.Description,
			ItemType:    l.ItemType,
			Quantity:    l.Quantity,
			UnitPrice:   l.UnitPrice,
			Amount:      l.Amount,
		})
	}
	out := snapshotDTO{
		ID:               s.ID.String(),
		InvoiceID:        s.InvoiceID.String(),
		SnapshottedAt:    httpserver.FormatRFC3339(s.SnapshottedAt),
		TotalAmount:      s.TotalAmount,
		LineItems:        lines,
		StatusAtSnapshot: s.StatusAtSnapshot,
		SourceModule:     string(s.SourceModule),
	}
	if s.CustomerID != nil {
		out.CustomerID = s.CustomerID.String()
	}
	if s.PlanID != nil {
		out.PlanID = s.PlanID.String()
	}
	if s.SchemaSnapshotID != nil {
		out.SchemaSnapshotID = s.SchemaSnapshotID.String()
	}
	return out
}

// ---------------------------------------------------------------------
// Credit notes
// ---------------------------------------------------------------------

type createCreditNoteRequest struct {
	InvoiceID  string  `json:"invoice_id"`
	CustomerID string  `json:"customer_id,omitempty"`
	Amount     float64 `json:"amount"`
	Reason     string  `json:"reason,omitempty"`
}

type voidCreditNoteRequest struct {
	Reason string `json:"reason"`
}

type creditNoteDTO struct {
	ID         string  `json:"id"`
	InvoiceID  string  `json:"invoice_id"`
	CustomerID string  `json:"customer_id,omitempty"`
	CreditNo   string  `json:"credit_no,omitempty"`
	Amount     float64 `json:"amount"`
	Reason     string  `json:"reason,omitempty"`
	Status     string  `json:"status"`
	IssuedAt   string  `json:"issued_at,omitempty"`
	AppliedAt  string  `json:"applied_at,omitempty"`
	VoidedAt   string  `json:"voided_at,omitempty"`
	CreatedBy  string  `json:"created_by,omitempty"`
	ApprovedBy string  `json:"approved_by,omitempty"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

func toCreditNoteDTO(c domain.CreditNote) creditNoteDTO {
	out := creditNoteDTO{
		ID:        c.ID.String(),
		InvoiceID: c.InvoiceID.String(),
		CreditNo:  c.CreditNo,
		Amount:    c.Amount,
		Reason:    c.Reason,
		Status:    string(c.Status),
		IssuedAt:  httpserver.FormatRFC3339Ptr(c.IssuedAt),
		AppliedAt: httpserver.FormatRFC3339Ptr(c.AppliedAt),
		VoidedAt:  httpserver.FormatRFC3339Ptr(c.VoidedAt),
		CreatedAt: httpserver.FormatRFC3339(c.CreatedAt),
		UpdatedAt: httpserver.FormatRFC3339(c.UpdatedAt),
	}
	if c.CustomerID != nil {
		out.CustomerID = c.CustomerID.String()
	}
	if c.CreatedBy != nil {
		out.CreatedBy = c.CreatedBy.String()
	}
	if c.ApprovedBy != nil {
		out.ApprovedBy = c.ApprovedBy.String()
	}
	return out
}

// ---------------------------------------------------------------------
// Bulk jobs
// ---------------------------------------------------------------------

type startBulkJobRequest struct {
	Kind         string         `json:"kind"`
	TargetFilter map[string]any `json:"target_filter,omitempty"`
}

type bulkJobDTO struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Status         string  `json:"status"`
	TotalExpected  int     `json:"total_expected"`
	TotalGenerated int     `json:"total_generated"`
	TotalFailed    int     `json:"total_failed"`
	StartedAt      string  `json:"started_at,omitempty"`
	CompletedAt    string  `json:"completed_at,omitempty"`
	CreatedBy      string  `json:"created_by,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

func toBulkJobDTO(j domain.BulkGenerationJob) bulkJobDTO {
	out := bulkJobDTO{
		ID:             j.ID.String(),
		Kind:           string(j.Kind),
		Status:         string(j.Status),
		TotalExpected:  j.TotalExpected,
		TotalGenerated: j.TotalGenerated,
		TotalFailed:    j.TotalFailed,
		StartedAt:      httpserver.FormatRFC3339Ptr(j.StartedAt),
		CompletedAt:    httpserver.FormatRFC3339Ptr(j.CompletedAt),
		CreatedAt:      httpserver.FormatRFC3339(j.CreatedAt),
	}
	if j.CreatedBy != nil {
		out.CreatedBy = j.CreatedBy.String()
	}
	return out
}

type bulkItemDTO struct {
	ID          string `json:"id"`
	CustomerID  string `json:"customer_id,omitempty"`
	InvoiceID   string `json:"invoice_id,omitempty"`
	Status      string `json:"status"`
	ErrorMsg    string `json:"error_msg,omitempty"`
	GeneratedAt string `json:"generated_at,omitempty"`
}

func toBulkItemDTO(it domain.BulkGenerationItem) bulkItemDTO {
	out := bulkItemDTO{
		ID:          it.ID.String(),
		Status:      string(it.Status),
		ErrorMsg:    it.ErrorMsg,
		GeneratedAt: httpserver.FormatRFC3339Ptr(it.GeneratedAt),
	}
	if it.CustomerID != nil {
		out.CustomerID = it.CustomerID.String()
	}
	if it.InvoiceID != nil {
		out.InvoiceID = it.InvoiceID.String()
	}
	return out
}

// ---------------------------------------------------------------------
// Monitoring / projections
// ---------------------------------------------------------------------

type invoiceProjectionDTO struct {
	ID            string  `json:"id"`
	InvoiceNumber string  `json:"invoice_number"`
	CustomerID    string  `json:"customer_id"`
	OrderID       string  `json:"order_id,omitempty"`
	InvoiceType   string  `json:"invoice_type"`
	InvoiceDate   string  `json:"invoice_date"`
	DueDate       string  `json:"due_date"`
	Subtotal      float64 `json:"subtotal"`
	PPNAmount     float64 `json:"ppn_amount"`
	Total         float64 `json:"total"`
	Status        string  `json:"status"`
	PaidAt        string  `json:"paid_at,omitempty"`
	AmountPaid    float64 `json:"amount_paid"`
	Outstanding   float64 `json:"outstanding"`
	PaymentMethod string  `json:"payment_method,omitempty"`
	SourceModule  string  `json:"source_module"`
}

func toInvoiceProjectionDTO(p port.InvoiceProjection) invoiceProjectionDTO {
	out := invoiceProjectionDTO{
		ID:            p.ID.String(),
		InvoiceNumber: p.InvoiceNumber,
		CustomerID:    p.CustomerID.String(),
		InvoiceType:   p.InvoiceType,
		InvoiceDate:   p.InvoiceDate.Format(time.RFC3339),
		DueDate:       p.DueDate.Format(time.RFC3339),
		Subtotal:      p.Subtotal,
		PPNAmount:     p.PPNAmount,
		Total:         p.Total,
		Status:        p.Status,
		PaidAt:        httpserver.FormatRFC3339Ptr(p.PaidAt),
		AmountPaid:    p.AmountPaid,
		Outstanding:   p.Outstanding,
		PaymentMethod: p.PaymentMethod,
		SourceModule:  p.SourceModule,
	}
	if p.OrderID != nil {
		out.OrderID = p.OrderID.String()
	}
	return out
}

type aggregationDTO struct {
	TotalCount    int                `json:"total_count"`
	TotalAmount   float64            `json:"total_amount"`
	PaidCount     int                `json:"paid_count"`
	PaidAmount    float64            `json:"paid_amount"`
	OverdueCount  int                `json:"overdue_count"`
	OverdueAmount float64            `json:"overdue_amount"`
	IssuedCount   int                `json:"issued_count"`
	IssuedAmount  float64            `json:"issued_amount"`
	CreditedCount int                `json:"credited_count"`
	AgingBuckets  port.AgingBuckets  `json:"aging_buckets"`
	ByStatus      map[string]int     `json:"by_status"`
}

func toAggregationDTO(a port.AggregationResult) aggregationDTO {
	return aggregationDTO{
		TotalCount:    a.TotalCount,
		TotalAmount:   a.TotalAmount,
		PaidCount:     a.PaidCount,
		PaidAmount:    a.PaidAmount,
		OverdueCount:  a.OverdueCount,
		OverdueAmount: a.OverdueAmount,
		IssuedCount:   a.IssuedCount,
		IssuedAmount:  a.IssuedAmount,
		CreditedCount: a.CreditedCount,
		AgingBuckets:  a.AgingBuckets,
		ByStatus:      a.ByStatus,
	}
}

type cycleHealthDTO struct {
	CycleID      string  `json:"cycle_id"`
	LastRunAt    string  `json:"last_run_at,omitempty"`
	SuccessCount int     `json:"success_count"`
	FailureCount int     `json:"failure_count"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	StaleBy24h   bool    `json:"stale_by_24h"`
}

func toCycleHealthDTO(c port.CycleHealthResult) cycleHealthDTO {
	return cycleHealthDTO{
		CycleID:      c.CycleID.String(),
		LastRunAt:    httpserver.FormatRFC3339Ptr(c.LastRunAt),
		SuccessCount: c.SuccessCount,
		FailureCount: c.FailureCount,
		AvgLatencyMS: c.AvgLatencyMS,
		StaleBy24h:   c.StaleBy24h,
	}
}

type topOverdueDTO struct {
	CustomerID        string  `json:"customer_id"`
	CustomerName      string  `json:"customer_name"`
	OverdueAmount     float64 `json:"overdue_amount"`
	OldestOverdueDays int     `json:"oldest_overdue_days"`
	InvoiceCount      int     `json:"invoice_count"`
}

func toTopOverdueDTO(r port.TopOverdueRow) topOverdueDTO {
	return topOverdueDTO{
		CustomerID:        r.CustomerID.String(),
		CustomerName:      r.CustomerName,
		OverdueAmount:     r.OverdueAmount,
		OldestOverdueDays: r.OldestOverdueDays,
		InvoiceCount:      r.InvoiceCount,
	}
}

type paymentHistoryDTO struct {
	ID          string  `json:"id"`
	InvoiceID   string  `json:"invoice_id"`
	Amount      float64 `json:"amount"`
	Method      string  `json:"method"`
	GatewayRef  string  `json:"gateway_ref,omitempty"`
	PaymentDate string  `json:"payment_date"`
	Status      string  `json:"status"`
}

func toPaymentHistoryDTO(p port.PaymentHistoryRow) paymentHistoryDTO {
	return paymentHistoryDTO{
		ID:          p.ID.String(),
		InvoiceID:   p.InvoiceID.String(),
		Amount:      p.Amount,
		Method:      p.Method,
		GatewayRef:  p.GatewayRef,
		PaymentDate: p.PaymentDate.Format(time.RFC3339),
		Status:      p.Status,
	}
}

type reminderHistoryDTO struct {
	ID        string `json:"id"`
	InvoiceID string `json:"invoice_id"`
	Kind      string `json:"kind"`
	Channel   string `json:"channel"`
	SentAt    string `json:"sent_at"`
	Delivered *bool  `json:"delivered,omitempty"`
	ErrorMsg  string `json:"error_msg,omitempty"`
}

func toReminderHistoryDTO(r port.ReminderHistoryRow) reminderHistoryDTO {
	return reminderHistoryDTO{
		ID:        r.ID.String(),
		InvoiceID: r.InvoiceID.String(),
		Kind:      r.Kind,
		Channel:   r.Channel,
		SentAt:    r.SentAt.Format(time.RFC3339),
		Delivered: r.Delivered,
		ErrorMsg:  r.ErrorMsg,
	}
}

// parseUUIDOrNil returns uuid.Nil for empty/invalid strings.
func parseUUIDOrNil(s string) uuid.UUID {
	if s == "" {
		return uuid.Nil
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return u
}

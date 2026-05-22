package domain

import (
	"time"

	"github.com/google/uuid"
)

// Notification is an in-app notification — minimal MVP for NT-1/NT-2.
// Out-of-band channels (email / WhatsApp) layer on top by tailing
// inserts; for MVP we just persist + serve via /notifications.
type Notification struct {
	ID              uuid.UUID
	RecipientUserID uuid.UUID
	Kind            string // e.g. "boq.submit", "negotiation.round_pending"
	SubjectType     string // "boq" | "negotiation_round" | "invoice" | "ewo" | "boq_line"
	SubjectID       uuid.UUID
	Title           string
	Body            string
	Severity        NotificationSeverity
	ReadAt          *time.Time
	CreatedAt       time.Time
}

type NotificationSeverity string

const (
	NotificationSeverityInfo     NotificationSeverity = "info"
	NotificationSeverityWarn     NotificationSeverity = "warn"
	NotificationSeverityCritical NotificationSeverity = "critical"
)

// NewNotification is the small constructor. Validation is intentionally
// loose — the producer-side ought to know what it's doing; we just
// stamp + persist.
func NewNotification(
	recipient uuid.UUID,
	kind, subjectType string,
	subjectID uuid.UUID,
	title, body string,
	severity NotificationSeverity,
) *Notification {
	if severity == "" {
		severity = NotificationSeverityInfo
	}
	return &Notification{
		ID:              uuid.New(),
		RecipientUserID: recipient,
		Kind:            kind,
		SubjectType:     subjectType,
		SubjectID:       subjectID,
		Title:           title,
		Body:            body,
		Severity:        severity,
		CreatedAt:       time.Now().UTC(),
	}
}

// MarkRead stamps the read_at timestamp. Idempotent — re-marking a
// read notification is a no-op.
func (n *Notification) MarkRead() {
	if n.ReadAt != nil {
		return
	}
	now := time.Now().UTC()
	n.ReadAt = &now
}

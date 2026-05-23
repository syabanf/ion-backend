package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestCommunication_OutboundConstruct(t *testing.T) {
	tid := uuid.New()
	c, err := NewCommunication(
		&tid, CommKindEmailOut,
		CounterpartyCustomer, nil, "customer@example.com",
		"RE: ticket", "thanks for reaching out",
		nil,
	)
	if err != nil {
		t.Fatalf("NewCommunication: %v", err)
	}
	if c.Direction != CommDirectionOut {
		t.Fatalf("expected outbound, got %s", c.Direction)
	}
	if c.Subject != "RE: ticket" {
		t.Fatalf("subject not preserved")
	}
}

func TestCommunication_InboundDirection(t *testing.T) {
	tid := uuid.New()
	c, err := NewCommunication(
		&tid, CommKindWhatsAppIn,
		CounterpartyCustomer, nil, "+628123456789",
		"", "still no internet",
		nil,
	)
	if err != nil {
		t.Fatalf("NewCommunication: %v", err)
	}
	if c.Direction != CommDirectionIn {
		t.Fatalf("expected inbound, got %s", c.Direction)
	}
}

func TestCommunication_BodyOrSubjectRequired(t *testing.T) {
	tid := uuid.New()
	if _, err := NewCommunication(&tid, CommKindEmailOut, CounterpartyCustomer, nil, "", "", "", nil); err == nil {
		t.Fatalf("expected validation error when both subject and body are empty")
	}
}

func TestCommunication_MarkDelivered(t *testing.T) {
	tid := uuid.New()
	c, _ := NewCommunication(&tid, CommKindEmailOut, CounterpartyCustomer, nil, "x", "Y", "Z", nil)
	c.MarkDelivered(c.SentAt.Add(1))
	if c.DeliveredAt == nil {
		t.Fatalf("DeliveredAt not stamped")
	}
	prev := *c.DeliveredAt
	c.MarkDelivered(c.SentAt.Add(2))
	if !c.DeliveredAt.Equal(prev) {
		t.Fatalf("MarkDelivered should be idempotent")
	}
}

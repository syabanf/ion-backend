package http

import (
	"time"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
)

// =====================================================================
// SLA Matrix DTO
// =====================================================================

type slaMatrixDTO struct {
	ID                   string  `json:"id"`
	CustomerType         string  `json:"customer_type"`
	TicketType           string  `json:"ticket_type"`
	Priority             string  `json:"priority"`
	FirstResponseMinutes int     `json:"first_response_minutes"`
	ResolveMinutes       int     `json:"resolve_minutes"`
	BreachWarnPct        float64 `json:"breach_warn_pct"`
	IsActive             bool    `json:"is_active"`
	EffectiveFrom        string  `json:"effective_from"`
	EffectiveTo          *string `json:"effective_to,omitempty"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
}

func toSLAMatrixDTO(e domain.SLAMatrixEntry) slaMatrixDTO {
	var effTo *string
	if e.EffectiveTo != nil {
		s := e.EffectiveTo.UTC().Format("2006-01-02")
		effTo = &s
	}
	return slaMatrixDTO{
		ID:                   e.ID.String(),
		CustomerType:         string(e.CustomerType),
		TicketType:           string(e.TicketType),
		Priority:             string(e.Priority),
		FirstResponseMinutes: e.FirstResponseMinutes,
		ResolveMinutes:       e.ResolveMinutes,
		BreachWarnPct:        e.BreachWarnPct,
		IsActive:             e.IsActive,
		EffectiveFrom:        e.EffectiveFrom.UTC().Format("2006-01-02"),
		EffectiveTo:          effTo,
		CreatedAt:            e.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:            e.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Per-ticket SLA summary DTO
// =====================================================================

type ticketSLADTO struct {
	TicketID                  string  `json:"ticket_id"`
	SLAMatrixID               *string `json:"sla_matrix_id,omitempty"`
	FirstResponseDueAt        *string `json:"first_response_due_at,omitempty"`
	ResolveDueAt              *string `json:"resolve_due_at,omitempty"`
	RemainingFirstRespSeconds int64   `json:"remaining_first_response_seconds"`
	RemainingResolveSeconds   int64   `json:"remaining_resolve_seconds"`
	BreachedFirstResponse     bool    `json:"breached_first_response"`
	BreachedResolve           bool    `json:"breached_resolve"`
	WarnedAt                  *string `json:"warned_at,omitempty"`
}

func toTicketSLADTO(s port.TicketSLASummary) ticketSLADTO {
	var matrixID *string
	if s.SLAMatrixID != nil {
		m := s.SLAMatrixID.String()
		matrixID = &m
	}
	return ticketSLADTO{
		TicketID:                  s.TicketID.String(),
		SLAMatrixID:               matrixID,
		FirstResponseDueAt:        rfc3339Ptr(s.FirstResponseDueAt),
		ResolveDueAt:              rfc3339Ptr(s.ResolveDueAt),
		RemainingFirstRespSeconds: s.RemainingFirstRespSeconds,
		RemainingResolveSeconds:   s.RemainingResolveSeconds,
		BreachedFirstResponse:     s.BreachedFirstResponse,
		BreachedResolve:           s.BreachedResolve,
		WarnedAt:                  rfc3339Ptr(s.WarnedAt),
	}
}

// =====================================================================
// Service Request DTO
// =====================================================================

type serviceRequestDTO struct {
	ID                 string         `json:"id"`
	TicketID           string         `json:"ticket_id"`
	CustomerID         string         `json:"customer_id"`
	RequestType        string         `json:"request_type"`
	ReferenceID        *string        `json:"reference_id,omitempty"`
	Status             string         `json:"status"`
	SubmittedBy        *string        `json:"submitted_by,omitempty"`
	ApprovedBy         *string        `json:"approved_by,omitempty"`
	ApprovalDecisionAt *string        `json:"approval_decision_at,omitempty"`
	RejectionReason    string         `json:"rejection_reason,omitempty"`
	FulfilledAt        *string        `json:"fulfilled_at,omitempty"`
	CancelledReason    string         `json:"cancelled_reason,omitempty"`
	SLADueAt           *string        `json:"sla_due_at,omitempty"`
	Payload            map[string]any `json:"payload,omitempty"`
	CreatedAt          string         `json:"created_at"`
	UpdatedAt          string         `json:"updated_at"`
}

func toSRDTO(sr domain.ServiceRequest) serviceRequestDTO {
	return serviceRequestDTO{
		ID:                 sr.ID.String(),
		TicketID:           sr.TicketID.String(),
		CustomerID:         sr.CustomerID.String(),
		RequestType:        string(sr.RequestType),
		ReferenceID:        uuidPtrString(sr.ReferenceID),
		Status:             string(sr.Status),
		SubmittedBy:        uuidPtrString(sr.SubmittedBy),
		ApprovedBy:         uuidPtrString(sr.ApprovedBy),
		ApprovalDecisionAt: rfc3339Ptr(sr.ApprovalDecisionAt),
		RejectionReason:    sr.RejectionReason,
		FulfilledAt:        rfc3339Ptr(sr.FulfilledAt),
		CancelledReason:    sr.CancelledReason,
		SLADueAt:           rfc3339Ptr(sr.SLADueAt),
		Payload:            sr.Payload,
		CreatedAt:          sr.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          sr.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Team DTO
// =====================================================================

type teamDTO struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	ManagerUserID    *string  `json:"manager_user_id,omitempty"`
	MembersCount     int      `json:"members_count"`
	FocusTicketTypes []string `json:"focus_ticket_types"`
	IsActive         bool     `json:"is_active"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

func toTeamDTO(t domain.Team) teamDTO {
	focus := make([]string, 0, len(t.FocusTicketTypes))
	for _, f := range t.FocusTicketTypes {
		focus = append(focus, string(f))
	}
	return teamDTO{
		ID:               t.ID.String(),
		Name:             t.Name,
		Description:      t.Description,
		ManagerUserID:    uuidPtrString(t.ManagerUserID),
		MembersCount:     t.MembersCount,
		FocusTicketTypes: focus,
		IsActive:         t.IsActive,
		CreatedAt:        t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        t.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type teamMemberDTO struct {
	ID         string  `json:"id"`
	TeamID     string  `json:"team_id"`
	UserID     string  `json:"user_id"`
	RoleInTeam string  `json:"role_in_team"`
	JoinedAt   string  `json:"joined_at"`
	LeftAt     *string `json:"left_at,omitempty"`
}

func toTeamMemberDTO(m domain.TeamMember) teamMemberDTO {
	return teamMemberDTO{
		ID:         m.ID.String(),
		TeamID:     m.TeamID.String(),
		UserID:     m.UserID.String(),
		RoleInTeam: string(m.RoleInTeam),
		JoinedAt:   m.JoinedAt.UTC().Format(time.RFC3339),
		LeftAt:     rfc3339Ptr(m.LeftAt),
	}
}

// =====================================================================
// CSAT DTO
// =====================================================================

type csatDTO struct {
	ID          string  `json:"id"`
	TicketID    string  `json:"ticket_id"`
	CustomerID  string  `json:"customer_id"`
	Rating      int     `json:"rating"`
	Comment     string  `json:"comment,omitempty"`
	Channel     string  `json:"channel"`
	RequestedAt string  `json:"requested_at"`
	RespondedAt *string `json:"responded_at,omitempty"`
}

func toCSATDTO(c domain.CSATResponse) csatDTO {
	return csatDTO{
		ID:          c.ID.String(),
		TicketID:    c.TicketID.String(),
		CustomerID:  c.CustomerID.String(),
		Rating:      c.Rating,
		Comment:     c.Comment,
		Channel:     string(c.Channel),
		RequestedAt: c.RequestedAt.UTC().Format(time.RFC3339),
		RespondedAt: rfc3339Ptr(c.RespondedAt),
	}
}

// =====================================================================
// Communication DTO
// =====================================================================

type communicationDTO struct {
	ID                string                  `json:"id"`
	TicketID          *string                 `json:"ticket_id,omitempty"`
	Kind              string                  `json:"kind"`
	Direction         string                  `json:"direction"`
	CounterpartyKind  string                  `json:"counterparty_kind"`
	CounterpartyID    *string                 `json:"counterparty_id,omitempty"`
	CounterpartyLabel string                  `json:"counterparty_label,omitempty"`
	Subject           string                  `json:"subject,omitempty"`
	Body              string                  `json:"body,omitempty"`
	Attachments       []domain.CommAttachment `json:"attachments"`
	ExternalMessageID string                  `json:"external_message_id,omitempty"`
	SentAt            string                  `json:"sent_at"`
	DeliveredAt       *string                 `json:"delivered_at,omitempty"`
	ReadAt            *string                 `json:"read_at,omitempty"`
	ErrorMsg          string                  `json:"error_msg,omitempty"`
	CreatedAt         string                  `json:"created_at"`
}

func toCommunicationDTO(c domain.Communication) communicationDTO {
	att := c.Attachments
	if att == nil {
		att = []domain.CommAttachment{}
	}
	return communicationDTO{
		ID:                c.ID.String(),
		TicketID:          uuidPtrString(c.TicketID),
		Kind:              string(c.Kind),
		Direction:         string(c.Direction),
		CounterpartyKind:  string(c.CounterpartyKind),
		CounterpartyID:    uuidPtrString(c.CounterpartyID),
		CounterpartyLabel: c.CounterpartyLabel,
		Subject:           c.Subject,
		Body:              c.Body,
		Attachments:       att,
		ExternalMessageID: c.ExternalMessageID,
		SentAt:            c.SentAt.UTC().Format(time.RFC3339),
		DeliveredAt:       rfc3339Ptr(c.DeliveredAt),
		ReadAt:            rfc3339Ptr(c.ReadAt),
		ErrorMsg:          c.ErrorMsg,
		CreatedAt:         c.CreatedAt.UTC().Format(time.RFC3339),
	}
}

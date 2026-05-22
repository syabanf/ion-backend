package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// E7 — PO Document
// =====================================================================

type PODocument struct {
	ID             uuid.UUID
	OpportunityID  uuid.UUID
	PONumber       string
	PORevision     int
	FileURL        string
	FileName       string
	FileSizeBytes  int64
	ContentType    string
	IssuedByPIC    string
	ReceivedAt     time.Time
	UploadedBy     *uuid.UUID
	Notes          string
	CreatedAt      time.Time
}

func NewPODocument(
	opportunityID uuid.UUID,
	poNumber string, poRevision int,
	fileURL, fileName, contentType string, fileSize int64,
) (*PODocument, error) {
	if poNumber == "" {
		return nil, derrors.Validation("po_document.number_required", "po_number is required")
	}
	if fileURL == "" {
		return nil, derrors.Validation("po_document.url_required", "file_url is required")
	}
	if poRevision < 1 {
		poRevision = 1
	}
	if contentType == "" {
		contentType = "application/pdf"
	}
	now := time.Now().UTC()
	return &PODocument{
		ID:            uuid.New(),
		OpportunityID: opportunityID,
		PONumber:      poNumber,
		PORevision:    poRevision,
		FileURL:       fileURL,
		FileName:      fileName,
		FileSizeBytes: fileSize,
		ContentType:   contentType,
		ReceivedAt:    now,
		CreatedAt:     now,
	}, nil
}

// =====================================================================
// E8 — Payment Proof
// =====================================================================

type PaymentProof struct {
	ID               uuid.UUID
	InvoicePaymentID uuid.UUID
	FileURL          string
	FileName         string
	FileSizeBytes    int64
	ContentType      string
	UploadedBy       *uuid.UUID
	Notes            string
	CreatedAt        time.Time
}

func NewPaymentProof(
	paymentID uuid.UUID,
	fileURL, fileName, contentType string, fileSize int64,
) (*PaymentProof, error) {
	if fileURL == "" {
		return nil, derrors.Validation("payment_proof.url_required", "file_url is required")
	}
	if contentType == "" {
		contentType = "application/pdf"
	}
	return &PaymentProof{
		ID:               uuid.New(),
		InvoicePaymentID: paymentID,
		FileURL:          fileURL,
		FileName:         fileName,
		FileSizeBytes:    fileSize,
		ContentType:      contentType,
		CreatedAt:        time.Now().UTC(),
	}, nil
}

// =====================================================================
// E9 — EWO checklist
// =====================================================================

type EWOChecklistItemStatus string

const (
	EWOChecklistPending    EWOChecklistItemStatus = "pending"
	EWOChecklistInProgress EWOChecklistItemStatus = "in_progress"
	EWOChecklistCompleted  EWOChecklistItemStatus = "completed"
	EWOChecklistSkipped    EWOChecklistItemStatus = "skipped"
)

type EWOChecklistItem struct {
	ID          uuid.UUID
	EWOID       uuid.UUID
	SeqNo       int
	Label       string
	Description string
	Status      EWOChecklistItemStatus
	CompletedAt *time.Time
	CompletedBy *uuid.UUID
	Notes       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func NewEWOChecklistItem(ewoID uuid.UUID, seq int, label, description string) (*EWOChecklistItem, error) {
	if seq < 1 {
		return nil, derrors.Validation("ewo_checklist.seq_invalid", "seq_no must be >= 1")
	}
	if label == "" {
		return nil, derrors.Validation("ewo_checklist.label_required", "label is required")
	}
	now := time.Now().UTC()
	return &EWOChecklistItem{
		ID:          uuid.New(),
		EWOID:       ewoID,
		SeqNo:       seq,
		Label:       label,
		Description: description,
		Status:      EWOChecklistPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// SetStatus updates the item with a new status. Completion stamps the
// timestamp and clears it when reverting (admin override scenario).
func (it *EWOChecklistItem) SetStatus(status EWOChecklistItemStatus, completedBy *uuid.UUID) error {
	switch status {
	case EWOChecklistPending, EWOChecklistInProgress, EWOChecklistCompleted, EWOChecklistSkipped:
	default:
		return derrors.Validation("ewo_checklist.status_invalid", "unknown status: "+string(status))
	}
	it.Status = status
	if status == EWOChecklistCompleted {
		now := time.Now().UTC()
		it.CompletedAt = &now
		it.CompletedBy = completedBy
	} else {
		it.CompletedAt = nil
		it.CompletedBy = nil
	}
	return nil
}

// EWOProgress computes %completed across a checklist.
func EWOProgress(items []EWOChecklistItem) float64 {
	if len(items) == 0 {
		return 0
	}
	done := 0
	for _, it := range items {
		if it.Status == EWOChecklistCompleted || it.Status == EWOChecklistSkipped {
			done++
		}
	}
	return float64(done) / float64(len(items)) * 100.0
}

// =====================================================================
// E11 — Projects, Sites, Services
// =====================================================================

type ProjectStatus string

const (
	ProjectStatusPlanning   ProjectStatus = "planning"
	ProjectStatusInProgress ProjectStatus = "in_progress"
	ProjectStatusCompleted  ProjectStatus = "completed"
	ProjectStatusCancelled  ProjectStatus = "cancelled"
)

type Project struct {
	ID                    uuid.UUID
	ProjectNumber         string
	QuotationID           uuid.UUID
	OpportunityID         uuid.UUID
	BOQVersionID          uuid.UUID
	Status                ProjectStatus
	StartedAt             *time.Time
	CompletedAt           *time.Time
	CancelledAt           *time.Time
	CancelReason          string
	ProjectManagerUserID  *uuid.UUID
	Notes                 string
	Revision              int
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type ProjectSiteStatus string

const (
	SiteStatusPending    ProjectSiteStatus = "pending"
	SiteStatusInProgress ProjectSiteStatus = "in_progress"
	SiteStatusActive     ProjectSiteStatus = "active"
	SiteStatusCancelled  ProjectSiteStatus = "cancelled"
)

type ProjectSite struct {
	ID          uuid.UUID
	ProjectID   uuid.UUID
	SiteCode    string
	SiteName    string
	Address     string
	Lat         *float64
	Lng         *float64
	PICName     string
	PICPhone    string
	Status      ProjectSiteStatus
	ActivatedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type EnterpriseServiceStatus string

const (
	SvcStatusPending      EnterpriseServiceStatus = "pending"
	SvcStatusProvisioning EnterpriseServiceStatus = "provisioning"
	SvcStatusActive       EnterpriseServiceStatus = "active"
	SvcStatusSuspended    EnterpriseServiceStatus = "suspended"
	SvcStatusTerminated   EnterpriseServiceStatus = "terminated"
)

type EnterpriseService struct {
	ID            uuid.UUID
	ProjectSiteID uuid.UUID
	BOQLineID     *uuid.UUID
	ServiceCode   string
	ServiceName   string
	Status        EnterpriseServiceStatus
	ActivatedAt   *time.Time
	TerminatedAt  *time.Time
	Notes         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func NewProject(quotationID, opportunityID, boqVersionID uuid.UUID, projectNumber string) (*Project, error) {
	if projectNumber == "" {
		return nil, derrors.Validation("project.number_required", "project_number is required")
	}
	now := time.Now().UTC()
	return &Project{
		ID:            uuid.New(),
		ProjectNumber: projectNumber,
		QuotationID:   quotationID,
		OpportunityID: opportunityID,
		BOQVersionID:  boqVersionID,
		Status:        ProjectStatusPlanning,
		Revision:      1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func NewProjectSite(projectID uuid.UUID, code, name string) (*ProjectSite, error) {
	if code == "" || name == "" {
		return nil, derrors.Validation("project_site.required", "site_code + site_name are required")
	}
	now := time.Now().UTC()
	return &ProjectSite{
		ID:        uuid.New(),
		ProjectID: projectID,
		SiteCode:  code,
		SiteName:  name,
		Status:    SiteStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func NewEnterpriseService(siteID uuid.UUID, code, name string) (*EnterpriseService, error) {
	if code == "" || name == "" {
		return nil, derrors.Validation("enterprise_service.required", "service_code + service_name are required")
	}
	now := time.Now().UTC()
	return &EnterpriseService{
		ID:            uuid.New(),
		ProjectSiteID: siteID,
		ServiceCode:   code,
		ServiceName:   name,
		Status:        SvcStatusPending,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func GenerateProjectNumber(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return fmt.Sprintf("PRJ-%s-%s", now.UTC().Format("20060102"), uuid.New().String()[:8])
}

// =====================================================================
// E12 — RFQ
// =====================================================================

type RFQStatus string

const (
	RFQStatusOpen       RFQStatus = "open"
	RFQStatusInProgress RFQStatus = "in_progress"
	RFQStatusFulfilled  RFQStatus = "fulfilled"
	RFQStatusCancelled  RFQStatus = "cancelled"
)

type RFQ struct {
	ID               uuid.UUID
	RFQNumber        string
	OpportunityID    uuid.UUID
	Status           RFQStatus
	RequestedBy      *uuid.UUID
	AssignedTo       *uuid.UUID
	Requirements     string
	Constraints      string
	DeadlineAt       *time.Time
	FulfilledAt      *time.Time
	FulfilledBOQID   *uuid.UUID
	CancelledAt      *time.Time
	CancelReason     string
	Notes            string
	Revision         int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func NewRFQ(opportunityID uuid.UUID, rfqNumber, requirements string) (*RFQ, error) {
	if rfqNumber == "" {
		return nil, derrors.Validation("rfq.number_required", "rfq_number is required")
	}
	now := time.Now().UTC()
	return &RFQ{
		ID:            uuid.New(),
		RFQNumber:     rfqNumber,
		OpportunityID: opportunityID,
		Status:        RFQStatusOpen,
		Requirements:  requirements,
		Revision:      1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (r *RFQ) Assign(userID uuid.UUID) error {
	if r.Status == RFQStatusFulfilled || r.Status == RFQStatusCancelled {
		return derrors.Conflict("rfq.terminal", "cannot reassign a terminal RFQ")
	}
	r.AssignedTo = &userID
	if r.Status == RFQStatusOpen {
		r.Status = RFQStatusInProgress
	}
	return nil
}

func (r *RFQ) Fulfill(boqID uuid.UUID) error {
	if r.Status == RFQStatusFulfilled {
		return derrors.Conflict("rfq.already_fulfilled", "RFQ is already fulfilled")
	}
	if r.Status == RFQStatusCancelled {
		return derrors.Conflict("rfq.cancelled", "cannot fulfill a cancelled RFQ")
	}
	now := time.Now().UTC()
	r.Status = RFQStatusFulfilled
	r.FulfilledAt = &now
	r.FulfilledBOQID = &boqID
	return nil
}

func (r *RFQ) Cancel(reason string) error {
	if r.Status == RFQStatusFulfilled {
		return derrors.Conflict("rfq.already_fulfilled", "cannot cancel a fulfilled RFQ")
	}
	if r.Status == RFQStatusCancelled {
		return derrors.Conflict("rfq.already_cancelled", "RFQ already cancelled")
	}
	if reason == "" {
		return derrors.Validation("rfq.cancel_reason_required", "reason is required")
	}
	now := time.Now().UTC()
	r.Status = RFQStatusCancelled
	r.CancelledAt = &now
	r.CancelReason = reason
	return nil
}

func GenerateRFQNumber(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return fmt.Sprintf("RFQ-%s-%s", now.UTC().Format("20060102"), uuid.New().String()[:8])
}

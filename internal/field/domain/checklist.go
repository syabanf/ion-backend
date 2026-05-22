package domain

import (
	"time"

	"github.com/google/uuid"
)

type ItemType string

const (
	ItemTypePhoto       ItemType = "photo"
	ItemTypeText        ItemType = "text"
	ItemTypeNumber      ItemType = "number"
	ItemTypeCheckbox    ItemType = "checkbox"
	ItemTypeQRScan      ItemType = "qr_scan"
	ItemTypeSignature   ItemType = "signature"
	ItemTypeGPSLocation ItemType = "gps_location"
)

// ChecklistTemplate is the per-(wo_type, product_type) blueprint a WO
// loads at dispatch time. Round 1: blueprint is global and immutable from
// the app — only DBA-level edits touch it.
type ChecklistTemplate struct {
	ID                  uuid.UUID
	WOType              WOType
	ProductType         string
	MaintenanceSubtype  string
	MinPhotosRequired   int
	GPSStampOnPhotos    bool
	Active              bool
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type ChecklistTemplateItem struct {
	ID                uuid.UUID
	TemplateID        uuid.UUID
	ItemOrder         int
	ItemType          ItemType
	Label             string
	Required          bool
	PhotoTag          string
	GPSRequired       bool
	MinAccuracyMeters *int
}

// ChecklistResponse — one per (wo_id, template_item_id) thanks to the
// UNIQUE constraint. Resubmits overwrite the row via the service.
type ChecklistResponse struct {
	ID             uuid.UUID
	WOID           uuid.UUID
	TemplateItemID uuid.UUID
	ResponseText   string
	FileURL        string
	GPSLat         *float64
	GPSLng         *float64
	GPSAccuracyM   *float64
	SubmittedBy    *uuid.UUID
	SubmittedAt    time.Time
}

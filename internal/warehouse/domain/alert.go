package domain

import (
	"time"

	"github.com/google/uuid"
)

// StockAlert is one below-threshold row surfaced to the alerts API.
// We compute the alert at read time (no separate alerts table); the
// shape here is the read-projection the service returns.
//
// PRD §M3 calls for parent-branch escalation: when a sub_area warehouse
// goes below threshold, the Area+Regional should also see it. We do
// that by attaching `escalation_path` to each alert — the chain of
// branch_ids from the warehouse's branch up to its regional root.
// The frontend filters by membership: a user assigned to branch B sees
// an alert if B is in the path.
type StockAlert struct {
	WarehouseID      uuid.UUID
	WarehouseCode    string
	WarehouseName    string
	BranchID         *uuid.UUID
	BranchCode       string
	BranchName       string
	BranchLevel      string
	StockItemID      uuid.UUID
	StockItemSKU     string
	StockItemName    string
	Unit             string
	Quantity         float64
	MinThreshold     float64
	Shortfall        float64 // min_threshold - quantity (always > 0 for alerts)
	EscalationPath   []uuid.UUID

	// Wave 88 — populated when a stock_alert_states row exists for
	// this (warehouse, item) pair. Nil before the first cron tick
	// after the alert opens; the frontend treats absence as "newly
	// opened".
	OpenSince          *time.Time
	CurrentLevel       AlertLevel
	LastEscalatedAt    *time.Time
}

// AlertLevel mirrors warehouse.stock_alert_states.current_level. The
// cron worker bumps it up the branch chain when the time budget at
// the current level expires.
type AlertLevel string

const (
	AlertLevelSubArea  AlertLevel = "sub_area"
	AlertLevelArea     AlertLevel = "area"
	AlertLevelRegional AlertLevel = "regional"
)

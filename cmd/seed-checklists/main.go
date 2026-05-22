// seed-checklists — populate WO checklist templates beyond the one
// baked into the M5 migration.
//
// Migration 0008 seeds the `new_installation × broadband` template
// (7 items). Phase 1 also needs three more, called out in the PRD:
//
//   - maintenance × broadband × hardware_swap   (5 items)
//   - maintenance × broadband × signal_issue    (5 items)
//   - termination × broadband                   (4 items)
//
// Without these, work orders of those types load with an empty
// checklist and the Technical App can't enforce the mandatory
// proof-of-work items. This cmd is idempotent — it checks for an
// existing (wo_type, product_type, maintenance_subtype) row first
// and skips it if found.
//
// Usage:
//
//   make seed-checklists
//   # or directly:
//   go run ./cmd/seed-checklists
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/ion-core/backend/pkg/database"
)

// item describes one checklist step for a template.
type item struct {
	Order       int
	Type        string // photo | text | number | checkbox | qr_scan | signature | gps_location
	Label       string
	Required    bool
	PhotoTag    string // optional, only relevant when Type=photo
	GPSRequired bool
	MinAccM     int // 0 = NULL
}

// template is the full row + its items.
type template struct {
	WOType            string // new_installation | maintenance | termination
	ProductType       string
	MaintenanceSub    string // empty for non-maintenance
	MinPhotos         int
	GPSStampOnPhotos  bool
	Items             []item
}

// roster of templates this seeder owns. The migration already created
// `new_installation × broadband`, so it's NOT here — if you re-seed
// you don't want a duplicate. Add new templates by appending.
var roster = []template{
	{
		WOType:           "maintenance",
		ProductType:      "broadband",
		MaintenanceSub:   "hardware_swap",
		MinPhotos:        3,
		GPSStampOnPhotos: true,
		Items: []item{
			{Order: 1, Type: "photo", Label: "Failed unit — before removal", Required: true, PhotoTag: "before"},
			{Order: 2, Type: "qr_scan", Label: "Scan failed unit serial number QR", Required: true},
			{Order: 3, Type: "qr_scan", Label: "Scan replacement unit serial number QR", Required: true},
			{Order: 4, Type: "photo", Label: "Replacement unit installed — LED status", Required: true, PhotoTag: "after"},
			{Order: 5, Type: "number", Label: "Signal strength after swap (dBm)", Required: true},
			{Order: 6, Type: "text", Label: "Reason for swap (e.g. dead device, RMA)", Required: false},
			{Order: 7, Type: "signature", Label: "Customer signature on completion", Required: true},
		},
	},
	{
		WOType:           "maintenance",
		ProductType:      "broadband",
		MaintenanceSub:   "signal_issue",
		MinPhotos:        2,
		GPSStampOnPhotos: true,
		Items: []item{
			{Order: 1, Type: "number", Label: "Signal strength before intervention (dBm)", Required: true},
			{Order: 2, Type: "photo", Label: "ONT box + cable termination", Required: true, PhotoTag: "before"},
			{Order: 3, Type: "text", Label: "Root cause finding (cable cut, splitter, ODP port, …)", Required: true},
			{Order: 4, Type: "text", Label: "Action taken", Required: true},
			{Order: 5, Type: "number", Label: "Signal strength after intervention (dBm)", Required: true},
			{Order: 6, Type: "photo", Label: "ONT LED status after fix", Required: true, PhotoTag: "after"},
			{Order: 7, Type: "signature", Label: "Customer signature on completion", Required: true},
		},
	},
	{
		WOType:           "termination",
		ProductType:      "broadband",
		MaintenanceSub:   "",
		MinPhotos:        2,
		GPSStampOnPhotos: true,
		Items: []item{
			{Order: 1, Type: "photo", Label: "ONT installed at customer site — before removal", Required: true, PhotoTag: "before"},
			{Order: 2, Type: "qr_scan", Label: "Scan retrieved ONT serial number QR", Required: true},
			{Order: 3, Type: "photo", Label: "Cable removed / capped at ODP", Required: true, PhotoTag: "after"},
			{Order: 4, Type: "checkbox", Label: "Device returned to warehouse", Required: true},
			{Order: 5, Type: "signature", Label: "Customer signature acknowledging retrieval", Required: true},
		},
	},
}

func main() {
	_ = godotenv.Load(".env", "../.env", "../../.env")

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatalf("DATABASE_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(dbURL))
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	created, skipped := 0, 0
	for _, tpl := range roster {
		key := fmt.Sprintf("%s × %s%s",
			tpl.WOType, tpl.ProductType,
			func() string {
				if tpl.MaintenanceSub == "" {
					return ""
				}
				return " × " + tpl.MaintenanceSub
			}(),
		)

		exists, err := templateExists(ctx, pool, tpl)
		if err != nil {
			log.Fatalf("check %s: %v", key, err)
		}
		if exists {
			fmt.Printf("  · %s already exists — skipping\n", key)
			skipped++
			continue
		}

		id, err := insertTemplate(ctx, pool, tpl)
		if err != nil {
			log.Fatalf("insert %s: %v", key, err)
		}
		if err := insertItems(ctx, pool, id, tpl.Items); err != nil {
			log.Fatalf("insert items for %s: %v", key, err)
		}
		fmt.Printf("  ✓ %s — %d items\n", key, len(tpl.Items))
		created++
	}

	fmt.Println()
	fmt.Printf("Done — created %d, skipped %d.\n", created, skipped)
}

// templateExists matches on (wo_type, product_type, maintenance_subtype).
// The DB has a UNIQUE constraint on this tuple but Postgres treats two
// NULLs as distinct, so we can't rely on ON CONFLICT alone — we query
// with `IS NOT DISTINCT FROM` to compare NULLs as equal.
func templateExists(ctx context.Context, pool *pgxpool.Pool, tpl template) (bool, error) {
	var sub interface{}
	if tpl.MaintenanceSub == "" {
		sub = nil
	} else {
		sub = tpl.MaintenanceSub
	}
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT id FROM field.wo_checklist_templates
		WHERE wo_type = $1
		  AND product_type = $2
		  AND maintenance_subtype IS NOT DISTINCT FROM $3
		LIMIT 1
	`, tpl.WOType, tpl.ProductType, sub).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func insertTemplate(ctx context.Context, pool *pgxpool.Pool, tpl template) (uuid.UUID, error) {
	var sub interface{}
	if tpl.MaintenanceSub == "" {
		sub = nil
	} else {
		sub = tpl.MaintenanceSub
	}
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO field.wo_checklist_templates
			(wo_type, product_type, maintenance_subtype, min_photos_required, gps_stamp_on_photos)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, tpl.WOType, tpl.ProductType, sub, tpl.MinPhotos, tpl.GPSStampOnPhotos).Scan(&id)
	return id, err
}

func insertItems(ctx context.Context, pool *pgxpool.Pool, templateID uuid.UUID, items []item) error {
	for _, it := range items {
		var photoTag interface{}
		if it.PhotoTag == "" {
			photoTag = nil
		} else {
			photoTag = it.PhotoTag
		}
		var minAcc interface{}
		if it.MinAccM == 0 {
			minAcc = nil
		} else {
			minAcc = it.MinAccM
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO field.wo_checklist_template_items
				(template_id, item_order, item_type, label, required,
				 photo_tag, gps_required, min_accuracy_meters)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, templateID, it.Order, it.Type, it.Label, it.Required,
			photoTag, it.GPSRequired, minAcc)
		if err != nil {
			return err
		}
	}
	return nil
}

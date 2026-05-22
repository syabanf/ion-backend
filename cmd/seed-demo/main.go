// seed-demo — provision one user per role for click-around demos.
//
// Idempotent. For each demo role this command:
//   1. Looks up the role in identity.roles (skips if not seeded).
//   2. If a user with the demo email exists → grant the role (no-op if
//      already granted).
//   3. Otherwise creates a fresh user with the demo email + the role.
//
// All users share the same demo password so a presenter doesn't have
// to memorise eight different secrets. Password is intentionally
// long-but-memorable to satisfy the min-length validator while still
// being safe to print on a slide:
//
//   IonDemo!2026Tour
//
// This binary is for local + staging only. Never run against prod —
// shipping known passwords for privileged roles into a production
// database is the kind of thing that ends careers.
//
// Usage:
//
//   make seed-demo
//   # or directly:
//   go run ./cmd/seed-demo
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"

	"github.com/ion-core/backend/internal/identity/adapter/postgres"
	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/pkg/database"
)

const demoPassword = "IonDemo!2026Tour" // 16 chars — exceeds min(12).

// demoUser describes one click-around persona.
type demoUser struct {
	Role       string
	Email      string
	FullName   string
	EmployeeID string // must be unique — identity.users.employee_id has a UNIQUE constraint.
	// SalesType is required for the sales_rep role; nil for everyone else.
	SalesType *string
	// TechGrade is required for the technician role; nil for everyone else.
	TechGrade *string
}

func sptr(s string) *string { return &s }

var demoRoster = []demoUser{
	{Role: "operations_admin", EmployeeID: "DEMO-OPS", Email: "ops@ion.local", FullName: "Demo Ops Admin"},
	{Role: "product_admin", EmployeeID: "DEMO-PROD", Email: "product@ion.local", FullName: "Demo Product Admin"},
	{Role: "finance_admin", EmployeeID: "DEMO-FIN-ADMIN", Email: "fin-admin@ion.local", FullName: "Demo Finance Admin"},
	{Role: "finance_staff", EmployeeID: "DEMO-FIN-STAFF", Email: "fin-staff@ion.local", FullName: "Demo Finance Staff"},
	{Role: "finance_manager", EmployeeID: "DEMO-FIN-MGR", Email: "fin-mgr@ion.local", FullName: "Demo Finance Manager"},
	{Role: "sales_rep", EmployeeID: "DEMO-SALES", Email: "sales@ion.local", FullName: "Demo Sales Rep", SalesType: sptr("broadband")},
	{Role: "sales_manager", EmployeeID: "DEMO-SALES-MGR", Email: "sales-mgr@ion.local", FullName: "Demo Sales Manager"},
	{Role: "noc", EmployeeID: "DEMO-NOC", Email: "noc@ion.local", FullName: "Demo NOC Operator"},
	{Role: "warehouse_staff", EmployeeID: "DEMO-WH", Email: "wh@ion.local", FullName: "Demo Warehouse Staff"},
	{Role: "warehouse_manager", EmployeeID: "DEMO-WH-MGR", Email: "wh-mgr@ion.local", FullName: "Demo Warehouse Manager"},
	{Role: "team_leader", EmployeeID: "DEMO-TL", Email: "tl@ion.local", FullName: "Demo Team Leader"},
	{Role: "technician", EmployeeID: "DEMO-TECH", Email: "tech@ion.local", FullName: "Demo Technician", TechGrade: sptr("senior")},
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

	hasher := postgres.NewBcryptHasher()
	userRepo := postgres.NewUserRepository(pool)

	hash, err := hasher.Hash(demoPassword)
	if err != nil {
		log.Fatalf("hash demo password: %v", err)
	}

	created, granted, skipped := 0, 0, 0
	for _, d := range demoRoster {
		var roleID uuid.UUID
		err := pool.QueryRow(ctx,
			`SELECT id FROM identity.roles WHERE name = $1`, d.Role,
		).Scan(&roleID)
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Printf("  · role %-20q not seeded — skipping\n", d.Role)
			skipped++
			continue
		}
		if err != nil {
			log.Fatalf("look up role %s: %v", d.Role, err)
		}

		email := strings.ToLower(d.Email)

		// Existing user? Grant the role if missing and move on.
		var existingID uuid.UUID
		err = pool.QueryRow(ctx,
			`SELECT id FROM identity.users WHERE email = $1`, email,
		).Scan(&existingID)
		if err == nil {
			if _, err := pool.Exec(ctx, `
				INSERT INTO identity.user_roles (user_id, role_id, assigned_at)
				VALUES ($1, $2, NOW())
				ON CONFLICT (user_id, role_id) DO NOTHING
			`, existingID, roleID); err != nil {
				log.Fatalf("grant %s: %v", d.Role, err)
			}
			fmt.Printf("  ✓ %-20s %s (granted to existing user)\n", d.Role, email)
			granted++
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Fatalf("check existing email %s: %v", email, err)
		}

		// Fresh user. EmployeeID must be unique (DB constraint); the
		// roster gives each persona a stable DEMO-* tag so this stays
		// reproducible across re-runs.
		u, err := domain.NewUser(d.EmployeeID, d.FullName, email, "", hash)
		if err != nil {
			log.Fatalf("build user %s: %v", d.Role, err)
		}
		var st *domain.SalesType
		if d.SalesType != nil {
			v := domain.SalesType(*d.SalesType)
			st = &v
		}
		var tg *domain.TechnicianGrade
		if d.TechGrade != nil {
			v := domain.TechnicianGrade(*d.TechGrade)
			tg = &v
		}
		if err := userRepo.Create(ctx, u, []string{d.Role}, st, tg); err != nil {
			log.Fatalf("create %s: %v", d.Role, err)
		}
		fmt.Printf("  ✓ %-20s %s (created)\n", d.Role, email)
		created++
	}

	fmt.Println()
	fmt.Printf("Done — created %d, granted %d, skipped %d.\n", created, granted, skipped)
	fmt.Printf("Demo password (all accounts): %s\n", demoPassword)
}

// seed — bootstrap CLI for the initial super_admin user.
//
// Idempotent. Three cases, in order:
//   1. A user already exists with the super_admin role  → no-op (exit 0).
//   2. A user with SEED_ADMIN_EMAIL exists but lacks the role → grant it.
//   3. No matching user → create one with the role.
//
// Reads from environment (or .env in the working / parent / grandparent
// directory). Required:
//
//   DATABASE_URL
//   SEED_ADMIN_EMAIL
//   SEED_ADMIN_PASSWORD       (minimum 12 chars)
//
// Optional:
//
//   SEED_ADMIN_FULL_NAME      default "Super Admin"
//   SEED_ADMIN_EMPLOYEE_ID    default ""
//   SEED_ADMIN_PHONE          default ""
//
// Usage:
//
//   go run ./cmd/seed
//   make seed
//
// Safe to run repeatedly — useful in CI bootstrap or as part of a "first
// deploy" sequence after migrations.
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
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/database"
)

const (
	envDBURL    = "DATABASE_URL"
	envEmail    = "SEED_ADMIN_EMAIL"
	envPassword = "SEED_ADMIN_PASSWORD"
	envFullName = "SEED_ADMIN_FULL_NAME"
	envEmpID    = "SEED_ADMIN_EMPLOYEE_ID"
	envPhone    = "SEED_ADMIN_PHONE"

	defaultFullName = "Super Admin"
	minPasswordLen  = 12
	roleName        = "super_admin"
)

func main() {
	// .env is optional — env vars from the host take precedence anyway.
	_ = godotenv.Load(".env", "../.env", "../../.env")

	dbURL := mustEnv(envDBURL)
	email := strings.ToLower(strings.TrimSpace(mustEnv(envEmail)))
	password := mustEnv(envPassword)
	fullName := envOrDefault(envFullName, defaultFullName)
	employeeID := os.Getenv(envEmpID)
	phone := os.Getenv(envPhone)

	if !strings.Contains(email, "@") {
		log.Fatalf("%s is not a valid email", envEmail)
	}
	if len(password) < minPasswordLen {
		log.Fatalf("%s must be at least %d characters", envPassword, minPasswordLen)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.New(ctx, database.DefaultConfig(dbURL))
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	// --- 0. Find the super_admin role. It must have been seeded by migration 0001.
	var roleID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM identity.roles WHERE name = $1`, roleName).Scan(&roleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			log.Fatalf("role %q not found — did you run migrations? (make migrate-up)", roleName)
		}
		log.Fatalf("look up role: %v", err)
	}

	// --- 1. Is there ALREADY a super_admin user? If so, we're done.
	var (
		existingID    uuid.UUID
		existingEmail string
	)
	err = pool.QueryRow(ctx, `
		SELECT u.id, u.email
		FROM identity.users u
		JOIN identity.user_roles ur ON ur.user_id = u.id
		WHERE ur.role_id = $1
		ORDER BY u.created_at
		LIMIT 1
	`, roleID).Scan(&existingID, &existingEmail)
	if err == nil {
		fmt.Printf("✓ super_admin already exists: %s (id %s) — nothing to do.\n",
			existingEmail, existingID)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		log.Fatalf("check existing super_admin: %v", err)
	}

	// --- 2. Does a user with this email already exist? Grant the role.
	var existingByEmail uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM identity.users WHERE email = $1`, email).Scan(&existingByEmail)
	if err == nil {
		if _, err := pool.Exec(ctx, `
			INSERT INTO identity.user_roles (user_id, role_id, assigned_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (user_id, role_id) DO NOTHING
		`, existingByEmail, roleID); err != nil {
			log.Fatalf("grant role: %v", err)
		}
		fmt.Printf("✓ Granted super_admin to existing user %s (id %s)\n", email, existingByEmail)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		log.Fatalf("check existing email: %v", err)
	}

	// --- 3. Create the user fresh, with the super_admin role atomically.
	hasher := postgres.NewBcryptHasher()
	hash, err := hasher.Hash(password)
	if err != nil {
		log.Fatalf("hash password: %v", err)
	}
	// Sanity check the hasher matches the verifier we use elsewhere.
	if err := auth.ComparePassword(hash, password); err != nil {
		log.Fatalf("hash self-check failed: %v", err)
	}

	u, err := domain.NewUser(employeeID, fullName, email, phone, hash)
	if err != nil {
		log.Fatalf("build user: %v", err)
	}

	userRepo := postgres.NewUserRepository(pool)
	// super_admin doesn't need sales_type or technician_grade; pass nil.
	if err := userRepo.Create(ctx, u, []string{roleName}, nil, nil); err != nil {
		log.Fatalf("create user: %v", err)
	}

	fmt.Printf("✓ Created super_admin %s (id %s)\n", email, u.ID)
	fmt.Println("  You can now log in at /login with the credentials above.")
}

// --- env helpers ---

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

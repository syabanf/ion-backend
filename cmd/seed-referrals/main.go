// seed-referrals — populate crm.referrals + billing.referral_rewards
// with a small demo set so the Referral Rewards page has content to
// show in a fresh environment.
//
// What it does:
//
//   1. Picks the first N active customers from crm.customers.
//   2. Pairs them up: customer[1] was referred by customer[0],
//      customer[3] was referred by customer[2], etc.
//   3. INSERTs into crm.referrals (status=rewarded) and
//      billing.referral_rewards (status=accrued, amount=fixed demo
//      value).
//
// Idempotent: a UNIQUE on referee_customer_id keeps it from creating
// duplicate referrals. Re-running reports "already linked" and moves
// on.
//
// This cmd directly hits the DB — same pattern as seed-checklists —
// because the backend has no referral-mutation endpoints yet.
//
// Usage:
//
//   make seed-referrals
//   # or directly:
//   go run ./cmd/seed-referrals
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

// Demo reward amount per referral — flat number for now.
// Production should compute via the referee's plan monthly price.
const demoRewardAmount = 50_000 // Rp 50,000

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

	customers, err := pickCustomers(ctx, pool, 6)
	if err != nil {
		log.Fatalf("pick customers: %v", err)
	}
	if len(customers) < 2 {
		fmt.Printf("Need at least 2 customers in crm.customers to seed referrals (have %d). Skipping.\n",
			len(customers))
		return
	}

	created, skipped := 0, 0
	// Pair (0→1), (2→3), (4→5), …
	for i := 0; i+1 < len(customers); i += 2 {
		referrer := customers[i]
		referee := customers[i+1]

		exists, err := referralExists(ctx, pool, referee)
		if err != nil {
			log.Fatalf("check referral for %s: %v", referee, err)
		}
		if exists {
			fmt.Printf("  · referee %s already linked — skipping\n", referee)
			skipped++
			continue
		}

		referralID, err := insertReferral(ctx, pool, referrer, referee)
		if err != nil {
			log.Fatalf("insert referral %s→%s: %v", referrer, referee, err)
		}
		if err := insertReward(ctx, pool, referralID, referrer, referee); err != nil {
			log.Fatalf("insert reward for %s: %v", referralID, err)
		}
		fmt.Printf("  ✓ %s → %s (reward Rp %d)\n", referrer, referee, demoRewardAmount)
		created++
	}

	fmt.Println()
	fmt.Printf("Done — created %d, skipped %d (used %d customers).\n",
		created, skipped, len(customers))
}

// pickCustomers returns the first N customers ordered by created_at
// ascending. We prefer "active" status when available so the rewards
// look plausible — they'd normally accrue when a referee's first OTC
// is paid, which only happens to active customers.
func pickCustomers(ctx context.Context, pool *pgxpool.Pool, n int) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, `
		SELECT id FROM crm.customers
		ORDER BY (status = 'active') DESC, created_at ASC
		LIMIT $1
	`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

func referralExists(ctx context.Context, pool *pgxpool.Pool, refereeID uuid.UUID) (bool, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx,
		`SELECT id FROM crm.referrals WHERE referee_customer_id = $1`,
		refereeID,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func insertReferral(ctx context.Context, pool *pgxpool.Pool, referrerID, refereeID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO crm.referrals
			(referrer_customer_id, referee_customer_id, status, rewarded_at, notes)
		VALUES ($1, $2, 'rewarded', NOW(), 'seed-referrals demo data')
		RETURNING id
	`, referrerID, refereeID).Scan(&id)
	return id, err
}

func insertReward(ctx context.Context, pool *pgxpool.Pool, referralID, referrerID, refereeID uuid.UUID) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO billing.referral_rewards
			(referral_id, referrer_customer_id, referee_customer_id,
			 amount, status, notes)
		VALUES ($1, $2, $3, $4, 'accrued', 'seed-referrals demo data')
	`, referralID, referrerID, refereeID, demoRewardAmount)
	return err
}

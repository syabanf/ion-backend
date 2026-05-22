// Package database provides a Postgres connection pool using pgx.
//
// One pool per service. Bounded contexts receive *pgxpool.Pool through
// their postgres adapter — never directly.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// DefaultConfig returns sensible pool defaults for a single service.
func DefaultConfig(url string) Config {
	return Config{
		URL:             url,
		MaxConns:        25,
		MinConns:        2,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
	}
}

// New creates and verifies a Postgres connection pool. Caller owns Close().
func New(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	pcfg.MaxConns = cfg.MaxConns
	pcfg.MinConns = cfg.MinConns
	pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
}

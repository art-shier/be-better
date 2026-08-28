package database

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"dayorder.local/api/internal/config"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Pinger interface {
	Ping(context.Context) error
}

func BuildPoolConfig(databaseConfig config.DatabaseConfig) (*pgxpool.Config, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseConfig.URL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL pool configuration: %w", err)
	}
	poolConfig.MaxConns = databaseConfig.MaxConns
	poolConfig.MinConns = databaseConfig.MinConns
	poolConfig.MaxConnLifetime = databaseConfig.MaxConnLifetime
	poolConfig.MaxConnIdleTime = databaseConfig.MaxConnIdleTime
	if poolConfig.ConnConfig.RuntimeParams == nil {
		poolConfig.ConnConfig.RuntimeParams = make(map[string]string)
	}
	poolConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"
	poolConfig.ConnConfig.RuntimeParams["statement_timeout"] = milliseconds(databaseConfig.StatementTimeout)
	poolConfig.ConnConfig.RuntimeParams["lock_timeout"] = milliseconds(databaseConfig.LockTimeout)
	poolConfig.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = milliseconds(databaseConfig.IdleTransactionTimeout)
	return poolConfig, nil
}

func Open(ctx context.Context, databaseConfig config.DatabaseConfig) (*pgxpool.Pool, error) {
	poolConfig, err := BuildPoolConfig(databaseConfig)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err = Ping(ctx, pool, databaseConfig.HealthTimeout); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return pool, nil
}

func Ping(ctx context.Context, pinger Pinger, timeout time.Duration) error {
	healthContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return pinger.Ping(healthContext)
}

func milliseconds(value time.Duration) string {
	return strconv.FormatInt(value.Milliseconds(), 10)
}

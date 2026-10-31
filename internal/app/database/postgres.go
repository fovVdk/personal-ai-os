// Package database owns PostgreSQL connection lifecycle and migrations.
// 中文：database 包负责 PostgreSQL 连接生命周期和迁移接入。
// English: This adapter owns PostgreSQL pool creation, health verification, and connection limits.
// 中文：本适配器负责创建 PostgreSQL 连接池、验证数据库可用性并限制连接资源。
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open creates and verifies a PostgreSQL connection pool.
// 中文：Open 创建 PostgreSQL 连接池，并在返回前验证数据库连通性。
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	config.MaxConns = 10
	config.MinConns = 1
	config.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return pool, nil
}

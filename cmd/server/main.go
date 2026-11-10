package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/searaaman/wallet-transfer/internal/handler"
	"github.com/searaaman/wallet-transfer/internal/repository"
	"github.com/searaaman/wallet-transfer/internal/service"
	"github.com/searaaman/wallet-transfer/migrations"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	dsn := getenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5433/wallet")
	addr := getenv("ADDR", ":8080")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		slog.Error("connect", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, migrations.Init); err != nil {
		slog.Error("migrate", "error", err)
		os.Exit(1)
	}

	h := handler.New(service.New(repository.NewStore(pool)))
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, h); err != nil {
		slog.Error("server", "error", err)
		os.Exit(1)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

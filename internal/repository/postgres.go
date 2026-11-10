package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/searaaman/wallet-transfer/internal/domain"
)

// db is satisfied by both *pgxpool.Pool and pgx.Tx, so the same queries run inside or outside a transaction.
type db interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Queries struct{ db db }

type Store struct {
	Queries
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{Queries: Queries{db: pool}, pool: pool}
}

// InTx runs fn in one transaction: commit if fn returns nil, roll back otherwise.
func (s *Store) InTx(ctx context.Context, fn func(q Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(Queries{db: tx})
	})
}

const transferColumns = `id, idempotency_key, from_wallet_id, to_wallet_id, amount, status, coalesce(failure_reason, ''), created_at`

func scanTransfer(row pgx.Row) (domain.Transfer, error) {
	var t domain.Transfer
	err := row.Scan(&t.ID, &t.IdempotencyKey, &t.FromWalletID, &t.ToWalletID, &t.Amount, &t.Status, &t.FailureReason, &t.CreatedAt)
	return t, err
}

// FindTransferByKey returns nil, nil when no transfer has this key.
func (q Queries) FindTransferByKey(ctx context.Context, key string) (*domain.Transfer, error) {
	t, err := scanTransfer(q.db.QueryRow(ctx, `SELECT `+transferColumns+` FROM transfers WHERE idempotency_key = $1`, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find transfer: %w", err)
	}
	return &t, nil
}

// LockWallets locks the given wallets with FOR UPDATE in ID order (fixed order prevents deadlocks).
// Missing wallets are simply absent from the result.
func (q Queries) LockWallets(ctx context.Context, ids ...string) (map[string]domain.Wallet, error) {
	rows, err := q.db.Query(ctx, `SELECT id, balance FROM wallets WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return nil, fmt.Errorf("lock wallets: %w", err)
	}
	wallets, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Wallet, error) {
		var w domain.Wallet
		return w, r.Scan(&w.ID, &w.Balance)
	})
	if err != nil {
		return nil, fmt.Errorf("lock wallets: %w", err)
	}
	out := make(map[string]domain.Wallet, len(wallets))
	for _, w := range wallets {
		out[w.ID] = w
	}
	return out, nil
}

// InsertTransfer inserts t and fills its ID and CreatedAt. It returns false if the
// idempotency key already exists (another request got there first).
func (q Queries) InsertTransfer(ctx context.Context, t *domain.Transfer) (bool, error) {
	err := q.db.QueryRow(ctx,
		`INSERT INTO transfers (idempotency_key, from_wallet_id, to_wallet_id, amount, status)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (idempotency_key) DO NOTHING
		 RETURNING id, created_at`,
		t.IdempotencyKey, t.FromWalletID, t.ToWalletID, t.Amount, t.Status,
	).Scan(&t.ID, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert transfer: %w", err)
	}
	return true, nil
}

// UpdateTransferStatus moves a PENDING transfer to its new status. The WHERE clause makes
// the database refuse any transition out of a terminal state.
func (q Queries) UpdateTransferStatus(ctx context.Context, t domain.Transfer) error {
	tag, err := q.db.Exec(ctx,
		`UPDATE transfers SET status = $2, failure_reason = NULLIF($3, ''), updated_at = now()
		 WHERE id = $1 AND status = 'PENDING'`,
		t.ID, t.Status, t.FailureReason)
	if err != nil {
		return fmt.Errorf("update transfer status: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrInvalidTransition
	}
	return nil
}

func (q Queries) AddBalance(ctx context.Context, walletID string, delta int64) error {
	if _, err := q.db.Exec(ctx, `UPDATE wallets SET balance = balance + $2 WHERE id = $1`, walletID, delta); err != nil {
		return fmt.Errorf("update balance: %w", err)
	}
	return nil
}

func (q Queries) InsertLedgerEntry(ctx context.Context, transferID, walletID, entryType string, amount int64) error {
	if _, err := q.db.Exec(ctx,
		`INSERT INTO ledger_entries (transfer_id, wallet_id, entry_type, amount) VALUES ($1, $2, $3, $4)`,
		transferID, walletID, entryType, amount); err != nil {
		return fmt.Errorf("insert ledger entry: %w", err)
	}
	return nil
}

func (q Queries) GetWallet(ctx context.Context, id string) (domain.Wallet, error) {
	var w domain.Wallet
	err := q.db.QueryRow(ctx, `SELECT id, balance FROM wallets WHERE id = $1`, id).Scan(&w.ID, &w.Balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, domain.ErrWalletNotFound
	}
	if err != nil {
		return w, fmt.Errorf("get wallet: %w", err)
	}
	return w, nil
}

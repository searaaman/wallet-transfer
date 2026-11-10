package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/searaaman/wallet-transfer/internal/domain"
	"github.com/searaaman/wallet-transfer/internal/repository"
)

type TransferService struct {
	store *repository.Store
}

func New(store *repository.Store) *TransferService {
	return &TransferService{store: store}
}

// errLostRace rolls back a transaction whose idempotency key was committed by a concurrent request.
var errLostRace = errors.New("idempotency key taken by concurrent request")

// Transfer executes a transfer exactly once per idempotency key. Replays return the stored
// transfer; a FAILED transfer is a normal result, not an error.
func (s *TransferService) Transfer(ctx context.Context, req domain.TransferRequest) (domain.Transfer, error) {
	start := time.Now()
	if err := req.Validate(); err != nil {
		return domain.Transfer{}, err
	}

	if t, err := s.replay(ctx, req); t != nil || err != nil {
		return deref(t), err
	}

	t := domain.Transfer{
		IdempotencyKey: req.IdempotencyKey,
		FromWalletID:   req.FromWalletID,
		ToWalletID:     req.ToWalletID,
		Amount:         req.Amount,
		Status:         domain.StatusPending,
	}
	err := s.store.InTx(ctx, func(q repository.Queries) error {
		wallets, err := q.LockWallets(ctx, req.FromWalletID, req.ToWalletID)
		if err != nil {
			return err
		}
		if len(wallets) != 2 {
			return domain.ErrWalletNotFound
		}

		inserted, err := q.InsertTransfer(ctx, &t)
		if err != nil {
			return err
		}
		if !inserted {
			return errLostRace
		}

		if wallets[req.FromWalletID].Balance < req.Amount {
			if err := t.MarkFailed(domain.ReasonInsufficientFunds); err != nil {
				return err
			}
			return q.UpdateTransferStatus(ctx, t)
		}

		if err := q.AddBalance(ctx, req.FromWalletID, -req.Amount); err != nil {
			return err
		}
		if err := q.AddBalance(ctx, req.ToWalletID, req.Amount); err != nil {
			return err
		}
		if err := q.InsertLedgerEntry(ctx, t.ID, req.FromWalletID, domain.EntryDebit, req.Amount); err != nil {
			return err
		}
		if err := q.InsertLedgerEntry(ctx, t.ID, req.ToWalletID, domain.EntryCredit, req.Amount); err != nil {
			return err
		}
		if err := t.MarkProcessed(); err != nil {
			return err
		}
		return q.UpdateTransferStatus(ctx, t)
	})

	if errors.Is(err, errLostRace) {
		t, err := s.replay(ctx, req)
		return deref(t), err
	}
	if err != nil {
		slog.Error("transfer failed", "idempotency_key", req.IdempotencyKey, "error", err)
		return domain.Transfer{}, err
	}
	slog.Info("transfer", "idempotency_key", t.IdempotencyKey, "transfer_id", t.ID, "status", t.Status,
		"failure_reason", t.FailureReason, "duration_ms", time.Since(start).Milliseconds())
	return t, nil
}

// replay returns the stored transfer for req's key, nil if there is none, or
// ErrIdempotencyMismatch if the key was used for a different request.
func (s *TransferService) replay(ctx context.Context, req domain.TransferRequest) (*domain.Transfer, error) {
	t, err := s.store.FindTransferByKey(ctx, req.IdempotencyKey)
	if err != nil || t == nil {
		return nil, err
	}
	if !t.SameRequest(req) {
		return nil, domain.ErrIdempotencyMismatch
	}
	return t, nil
}

func deref(t *domain.Transfer) domain.Transfer {
	if t == nil {
		return domain.Transfer{}
	}
	return *t
}

func (s *TransferService) GetWallet(ctx context.Context, id string) (domain.Wallet, error) {
	return s.store.GetWallet(ctx, id)
}

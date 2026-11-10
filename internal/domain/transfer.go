package domain

import (
	"errors"
	"time"
)

type Status string

const (
	StatusPending   Status = "PENDING"
	StatusProcessed Status = "PROCESSED"
	StatusFailed    Status = "FAILED"
)

const ReasonInsufficientFunds = "insufficient_funds"

const (
	EntryDebit  = "DEBIT"
	EntryCredit = "CREDIT"
)

var (
	ErrInvalidRequest      = errors.New("invalid request")
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrIdempotencyMismatch = errors.New("idempotency key reused with a different request")
	ErrInvalidTransition   = errors.New("invalid status transition")
)

type Wallet struct {
	ID      string
	Balance int64
}

type TransferRequest struct {
	IdempotencyKey string
	FromWalletID   string
	ToWalletID     string
	Amount         int64
}

func (r TransferRequest) Validate() error {
	switch {
	case r.IdempotencyKey == "":
		return errors.Join(ErrInvalidRequest, errors.New("idempotencyKey is required"))
	case r.FromWalletID == "" || r.ToWalletID == "":
		return errors.Join(ErrInvalidRequest, errors.New("fromWalletId and toWalletId are required"))
	case r.FromWalletID == r.ToWalletID:
		return errors.Join(ErrInvalidRequest, errors.New("fromWalletId and toWalletId must differ"))
	case r.Amount <= 0:
		return errors.Join(ErrInvalidRequest, errors.New("amount must be positive"))
	}
	return nil
}

type Transfer struct {
	ID             string
	IdempotencyKey string
	FromWalletID   string
	ToWalletID     string
	Amount         int64
	Status         Status
	FailureReason  string
	CreatedAt      time.Time
}

// SameRequest reports whether a stored transfer was created by an identical request.
func (t Transfer) SameRequest(r TransferRequest) bool {
	return t.FromWalletID == r.FromWalletID && t.ToWalletID == r.ToWalletID && t.Amount == r.Amount
}

func (t *Transfer) MarkProcessed() error {
	if t.Status != StatusPending {
		return ErrInvalidTransition
	}
	t.Status = StatusProcessed
	return nil
}

func (t *Transfer) MarkFailed(reason string) error {
	if t.Status != StatusPending {
		return ErrInvalidTransition
	}
	t.Status = StatusFailed
	t.FailureReason = reason
	return nil
}

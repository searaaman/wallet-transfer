package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/searaaman/wallet-transfer/internal/domain"
	"github.com/searaaman/wallet-transfer/internal/service"
)

type transferRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	FromWalletID   string `json:"fromWalletId"`
	ToWalletID     string `json:"toWalletId"`
	Amount         int64  `json:"amount"`
}

type transferResponse struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotencyKey"`
	FromWalletID   string    `json:"fromWalletId"`
	ToWalletID     string    `json:"toWalletId"`
	Amount         int64     `json:"amount"`
	Status         string    `json:"status"`
	FailureReason  string    `json:"failureReason"`
	CreatedAt      time.Time `json:"createdAt"`
}

type walletResponse struct {
	ID      string `json:"id"`
	Balance int64  `json:"balance"`
}

func New(svc *service.TransferService) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /transfers", func(w http.ResponseWriter, r *http.Request) {
		var req transferRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		t, err := svc.Transfer(r.Context(), domain.TransferRequest(req))
		if err != nil {
			writeError(w, err)
			return
		}
		code := http.StatusCreated
		if t.Status == domain.StatusFailed {
			code = http.StatusUnprocessableEntity
		}
		writeJSON(w, code, transferResponse{
			ID: t.ID, IdempotencyKey: t.IdempotencyKey, FromWalletID: t.FromWalletID, ToWalletID: t.ToWalletID,
			Amount: t.Amount, Status: string(t.Status), FailureReason: t.FailureReason, CreatedAt: t.CreatedAt,
		})
	})

	mux.HandleFunc("GET /wallets/{id}", func(w http.ResponseWriter, r *http.Request) {
		wallet, err := svc.GetWallet(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, walletResponse{ID: wallet.ID, Balance: wallet.Balance})
	})

	return mux
}

func writeError(w http.ResponseWriter, err error) {
	code, msg := http.StatusInternalServerError, "internal error"
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		code, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, domain.ErrWalletNotFound):
		code, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, domain.ErrIdempotencyMismatch):
		code, msg = http.StatusConflict, err.Error()
	}
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

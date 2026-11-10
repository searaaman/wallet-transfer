package domain

import (
	"errors"
	"testing"
)

func TestOnlyPendingTransfersCanChangeState(t *testing.T) {
	for _, s := range []Status{StatusProcessed, StatusFailed} {
		tr := Transfer{Status: s}
		if err := tr.MarkProcessed(); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s -> PROCESSED: got %v, want ErrInvalidTransition", s, err)
		}
		if err := tr.MarkFailed("x"); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s -> FAILED: got %v, want ErrInvalidTransition", s, err)
		}
	}

	tr := Transfer{Status: StatusPending}
	if err := tr.MarkFailed(ReasonInsufficientFunds); err != nil || tr.Status != StatusFailed {
		t.Errorf("PENDING -> FAILED: got %v status=%s", err, tr.Status)
	}
}

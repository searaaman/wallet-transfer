package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/searaaman/wallet-transfer/internal/handler"
	"github.com/searaaman/wallet-transfer/internal/repository"
	"github.com/searaaman/wallet-transfer/internal/service"
	"github.com/searaaman/wallet-transfer/migrations"
)

type transfer struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	FailureReason string `json:"failureReason"`
}

type env struct {
	t    *testing.T
	srv  *httptest.Server
	pool *pgxpool.Pool
}

// setup gives each test an empty database and a running server. Wallets are seeded as id -> balance.
func setup(t *testing.T, wallets map[string]int64) *env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5433/wallet"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, migrations.Init); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE ledger_entries, transfers, wallets`); err != nil {
		t.Fatal(err)
	}
	for id, bal := range wallets {
		if _, err := pool.Exec(ctx, `INSERT INTO wallets (id, balance) VALUES ($1, $2)`, id, bal); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(handler.New(service.New(repository.NewStore(pool))))
	t.Cleanup(srv.Close)
	e := &env{t: t, srv: srv, pool: pool}
	t.Cleanup(e.assertLedgerConsistent)
	return e
}

func (e *env) post(key, from, to string, amount int64) (int, transfer) {
	body, _ := json.Marshal(map[string]any{"idempotencyKey": key, "fromWalletId": from, "toWalletId": to, "amount": amount})
	return e.postRaw(body)
}

func (e *env) postRaw(body []byte) (int, transfer) {
	res, err := http.Post(e.srv.URL+"/transfers", "application/json", bytes.NewReader(body))
	if err != nil {
		e.t.Error(err)
		return 0, transfer{}
	}
	defer res.Body.Close()
	var tr transfer
	_ = json.NewDecoder(res.Body).Decode(&tr)
	return res.StatusCode, tr
}

func (e *env) balance(id string) int64 {
	res, err := http.Get(e.srv.URL + "/wallets/" + id)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var w struct{ Balance int64 }
	_ = json.NewDecoder(res.Body).Decode(&w)
	return w.Balance
}

func (e *env) count(sql string) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// assertLedgerConsistent runs after every test: the ledger nets to zero, every PROCESSED transfer
// has exactly one DEBIT and one CREDIT, nothing else has entries, and no wallet is negative.
func (e *env) assertLedgerConsistent() {
	e.t.Helper()
	if n := e.count(`SELECT coalesce(sum(CASE entry_type WHEN 'CREDIT' THEN amount ELSE -amount END), 0) FROM ledger_entries`); n != 0 {
		e.t.Errorf("ledger nets to %d, want 0", n)
	}
	if n := e.count(`SELECT count(*) FROM transfers t WHERE (status = 'PROCESSED') <>
		(SELECT count(*) = 2 FROM ledger_entries l WHERE l.transfer_id = t.id)`); n != 0 {
		e.t.Errorf("%d transfers with wrong ledger entries", n)
	}
	if n := e.count(`SELECT count(*) FROM wallets WHERE balance < 0`); n != 0 {
		e.t.Errorf("%d wallets with negative balance", n)
	}
}

func TestTransferMovesMoney(t *testing.T) {
	e := setup(t, map[string]int64{"a": 1000, "b": 0})

	code, tr := e.post("k1", "a", "b", 300)

	if code != http.StatusCreated || tr.Status != "PROCESSED" {
		t.Fatalf("got %d %+v, want 201 PROCESSED", code, tr)
	}
	if a, b := e.balance("a"), e.balance("b"); a != 700 || b != 300 {
		t.Errorf("balances a=%d b=%d, want 700 300", a, b)
	}
	if n := e.count(`SELECT count(*) FROM ledger_entries`); n != 2 {
		t.Errorf("%d ledger entries, want 2", n)
	}
}

func TestRetryReturnsOriginalAndMovesMoneyOnce(t *testing.T) {
	e := setup(t, map[string]int64{"a": 1000, "b": 0})

	_, first := e.post("k1", "a", "b", 300)
	code, second := e.post("k1", "a", "b", 300)

	if code != http.StatusCreated || second.ID != first.ID {
		t.Fatalf("retry got %d id=%s, want 201 id=%s", code, second.ID, first.ID)
	}
	if a := e.balance("a"); a != 700 {
		t.Errorf("balance a=%d, want 700 (debited once)", a)
	}
	if n := e.count(`SELECT count(*) FROM transfers`); n != 1 {
		t.Errorf("%d transfers, want 1", n)
	}
}

func TestSameKeyDifferentBodyIsConflict(t *testing.T) {
	e := setup(t, map[string]int64{"a": 1000, "b": 0})

	e.post("k1", "a", "b", 300)
	code, _ := e.post("k1", "a", "b", 500)

	if code != http.StatusConflict {
		t.Fatalf("got %d, want 409", code)
	}
	if a := e.balance("a"); a != 700 {
		t.Errorf("balance a=%d, want 700", a)
	}
}

func TestInsufficientFundsIsStoredAsFailed(t *testing.T) {
	e := setup(t, map[string]int64{"a": 100, "b": 0})

	code, tr := e.post("k1", "a", "b", 500)
	if code != http.StatusUnprocessableEntity || tr.Status != "FAILED" || tr.FailureReason != "insufficient_funds" {
		t.Fatalf("got %d %+v, want 422 FAILED insufficient_funds", code, tr)
	}

	// Top up, then retry the same key: the original FAILED result is returned, no money moves.
	if _, err := e.pool.Exec(context.Background(), `UPDATE wallets SET balance = 1000 WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	code, retry := e.post("k1", "a", "b", 500)
	if code != http.StatusUnprocessableEntity || retry.ID != tr.ID || retry.Status != "FAILED" {
		t.Fatalf("retry got %d %+v, want original FAILED transfer", code, retry)
	}
	if b := e.balance("b"); b != 0 {
		t.Errorf("balance b=%d, want 0", b)
	}
}

func TestUnknownWalletIsNotFound(t *testing.T) {
	e := setup(t, map[string]int64{"a": 1000})

	code, _ := e.post("k1", "a", "nope", 100)

	if code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
	if n := e.count(`SELECT count(*) FROM transfers`); n != 0 {
		t.Errorf("%d transfers stored, want 0", n)
	}
}

func TestInvalidRequestsAreRejected(t *testing.T) {
	e := setup(t, map[string]int64{"a": 1000, "b": 0})

	cases := map[string]string{
		"bad json":       `{`,
		"missing key":    `{"fromWalletId":"a","toWalletId":"b","amount":1}`,
		"zero amount":    `{"idempotencyKey":"k","fromWalletId":"a","toWalletId":"b","amount":0}`,
		"negative":       `{"idempotencyKey":"k","fromWalletId":"a","toWalletId":"b","amount":-5}`,
		"same wallet":    `{"idempotencyKey":"k","fromWalletId":"a","toWalletId":"a","amount":1}`,
		"float amount":   `{"idempotencyKey":"k","fromWalletId":"a","toWalletId":"b","amount":1.5}`,
		"unknown field":  `{"idempotencyKey":"k","fromWalletId":"a","toWalletId":"b","amount":1,"x":1}`,
		"missing wallet": `{"idempotencyKey":"k","toWalletId":"b","amount":1}`,
	}
	for name, body := range cases {
		if code, _ := e.postRaw([]byte(body)); code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, code)
		}
	}
	if n := e.count(`SELECT count(*) FROM transfers`); n != 0 {
		t.Errorf("%d transfers stored, want 0", n)
	}
}

func TestConcurrentDebitsNeverOverspend(t *testing.T) {
	e := setup(t, map[string]int64{"a": 1000, "b": 0})

	const n = 50 // 50 x 100 against a balance of 1000: exactly 10 can succeed
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = e.post(fmt.Sprintf("k%d", i), "a", "b", 100)
		}()
	}
	wg.Wait()

	processed := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			processed++
		case http.StatusUnprocessableEntity:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if processed != 10 {
		t.Errorf("%d processed, want 10", processed)
	}
	if a, b := e.balance("a"), e.balance("b"); a != 0 || b != 1000 {
		t.Errorf("balances a=%d b=%d, want 0 1000", a, b)
	}
}

func TestConcurrentDuplicatesCreateOneTransfer(t *testing.T) {
	e := setup(t, map[string]int64{"a": 1000, "b": 0})

	const n = 20
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, tr := e.post("same-key", "a", "b", 100)
			ids[i] = tr.ID
		}()
	}
	wg.Wait()

	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("got different transfer ids %v, want all equal", ids)
		}
	}
	if a := e.balance("a"); a != 900 {
		t.Errorf("balance a=%d, want 900", a)
	}
}

func TestOppositeTransfersDoNotDeadlock(t *testing.T) {
	e := setup(t, map[string]int64{"a": 1000, "b": 1000})

	const n = 40
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			from, to := "a", "b"
			if i%2 == 1 {
				from, to = "b", "a"
			}
			if code, _ := e.post(fmt.Sprintf("k%d", i), from, to, 10); code != http.StatusCreated {
				t.Errorf("transfer %d: got %d, want 201", i, code)
			}
		}()
	}
	wg.Wait()

	if a, b := e.balance("a"), e.balance("b"); a != 1000 || b != 1000 {
		t.Errorf("balances a=%d b=%d, want 1000 1000", a, b)
	}
}

# Design: Wallet Transfer Service

## Problem

Move money between two wallets so that:
- a retried or duplicated request never moves money twice,
- concurrent transfers never overspend a wallet,
- every transfer is recorded in a double-entry ledger that always balances.

## API contract

### `POST /transfers`

Request:
```json
{ "idempotencyKey": "abc123", "fromWalletId": "wallet_1", "toWalletId": "wallet_2", "amount": 100 }
```
`amount` is an integer in the smallest currency unit (paise). Floats are never used for money.

Response body (all outcomes that create a transfer):
```json
{
  "id": "6f1c...", "idempotencyKey": "abc123",
  "fromWalletId": "wallet_1", "toWalletId": "wallet_2", "amount": 100,
  "status": "PROCESSED", "failureReason": "", "createdAt": "2026-11-10T10:00:00Z"
}
```

| Case | Status | Body |
|---|---|---|
| Transfer processed | `201 Created` | transfer, `status: PROCESSED` |
| Insufficient balance | `422 Unprocessable Entity` | transfer, `status: FAILED`, `failureReason: insufficient_funds` |
| Same key, same body (retry) | same code as the first call | the original transfer, unchanged |
| Same key, different body | `409 Conflict` | `{"error": ...}` |
| Invalid input (missing key, amount <= 0, from == to, bad JSON) | `400 Bad Request` | `{"error": ...}` |
| Unknown wallet | `404 Not Found` | `{"error": ...}` |

### `GET /wallets/{id}`

Returns `{ "id": "wallet_1", "balance": 900 }` or 404.

## Expected behaviour

1. Validate the input. Invalid requests are rejected before anything is stored.
2. If a transfer with this `idempotencyKey` already exists, return it (or 409 if the body differs).
3. Otherwise, in **one database transaction**:
   1. lock both wallets (`SELECT ... FOR UPDATE`, in ID order),
   2. insert the transfer as `PENDING`,
   3. if the balance is too low, mark it `FAILED` (`insufficient_funds`),
   4. otherwise debit, credit, write the DEBIT and CREDIT ledger entries, and mark it `PROCESSED`,
   5. commit.

Because all of this is one transaction, no reader ever sees a half-done transfer.

## Schema

See `migrations/001_init.sql`.

- `wallets(id, balance, created_at)`, with `CHECK (balance >= 0)`.
- `transfers(id, idempotency_key UNIQUE, from_wallet_id FK, to_wallet_id FK, amount, status, failure_reason, created_at, updated_at)`
  - `CHECK amount > 0`
  - `CHECK from <> to`
  - `CHECK status IN (...)`
  - `CHECK` that the failure reason is set only when the status is FAILED
- `ledger_entries(id, transfer_id FK, wallet_id FK, entry_type, amount, created_at)`
  - `CHECK amount > 0`
  - `UNIQUE (transfer_id, entry_type)`, so a transfer can never have two debits or two credits
  - index on `wallet_id` for history and balance audits.

The database enforces the invariants as well, so a bug in the code can't break them:
- no negative balance,
- no duplicate key,
- no third ledger entry,
- no unknown status.

## Idempotency

- `transfers.idempotency_key` is `UNIQUE`, and the transfers table doubles as the idempotency record.
- **Fast path:** look up the key first. If found, compare `from`, `to` and `amount`. If they match, return the stored transfer; if not, return 409.
- **Race path:** two requests with the same new key can both miss the lookup.
  - Both then insert with `INSERT ... ON CONFLICT (idempotency_key) DO NOTHING`.
  - The unique index makes the second insert wait until the first transaction commits, and then it inserts nothing.
  - The second request rolls back and returns the row the first one committed.
  - Money moves exactly once.
- **Failed transfers are stored too**, so a retry of an insufficient-balance transfer gets the same FAILED answer. It won't succeed later just because the wallet was topped up in between. A new attempt needs a new key.
- Validation errors and unknown wallets are **not** stored. They are deterministic, so a retry gets the same answer anyway.

## Retries and states

- `PENDING -> PROCESSED` and `PENDING -> FAILED` are the only transitions.
  - In code they live in `domain.Transfer`.
  - In the database they are guarded by `UPDATE ... WHERE status = 'PENDING'`, so a terminal transfer can never change state again.
- Client retries are safe because of the idempotency key.
- A crash before commit rolls everything back, including the PENDING row, so the client's retry starts clean.

## Consistency and concurrency

**Strategy: pessimistic row locks inside a READ COMMITTED transaction.**

- `SELECT ... FROM wallets WHERE id IN (a, b) ORDER BY id FOR UPDATE` locks both wallets.
  - A second transfer touching either wallet waits until the first commits.
  - It then reads the fresh balance, so double spending is impossible.
- **Deadlock prevention:** locks are always taken in ID order.
  - A→B and B→A both lock `min(a,b)` first, so neither can hold one lock while waiting for the other.
- **Why not optimistic locking?** Hot wallets would see many version conflicts and retries.
  - Row locks make conflicting transfers simply queue.
  - Transfers on unrelated wallets still run in parallel.
- **Why not SERIALIZABLE?** It would also work, but every serialization failure needs a retry loop. Explicit locks are simpler and their behaviour is easier to reason about.
- `CHECK (balance >= 0)` is the last line of defence.

## Failure modes

| Failure | Result |
|---|---|
| Insufficient balance | FAILED transfer stored, 422 |
| Unknown wallet | 404, nothing stored |
| Duplicate request (sequential or concurrent) | original transfer returned, no new money movement |
| Key reused with a different body | 409, nothing changed |
| DB error or crash mid-transfer | transaction rolled back, nothing stored, 500; the client retries with the same key |
| Deadlock | prevented by lock ordering |

## Observability

- Structured logs (`log/slog`, JSON): one line per transfer with the key, transfer ID, status, failure reason and duration, plus a line for each error.
- Not done (out of scope): metrics (transfer count by status, latency histogram) and request tracing.

## Testing strategy

Integration tests run against a real PostgreSQL, because the guarantees come from the database.
- Transfer moves money; the ledger has exactly one DEBIT and one CREDIT.
- Idempotency: replay returns the same transfer and moves money once; a different body returns 409.
- Concurrent duplicates: N goroutines send the same key, and exactly one transfer is created.
- Concurrent spending: N goroutines debit one wallet, and exactly as many succeed as the balance allows. The balance ends at the expected value and is never negative.
- Opposite-direction concurrent transfers (A→B and B→A) finish without deadlock.
- Failures: insufficient funds (FAILED, stored, replay stays FAILED), unknown wallet, validation errors.
- Ledger invariant, checked after every test:
  - credits minus debits across all entries is 0,
  - every PROCESSED transfer has exactly 2 entries and no other transfer has any,
  - no wallet is negative.

## Assumptions and trade-offs

- Single currency; amounts are integers in the smallest unit.
- Wallets are created outside this API (seeded with SQL); there's no wallet-creation endpoint.
- No separate `idempotency_records` table. The unique key on `transfers` does the job and saves a second write and a second table to keep in sync.
- A stored balance (rather than one derived from the ledger) keeps reads O(1). The ledger is still the audit trail, and the tests check the two agree.
- Keys never expire. A real system would add a TTL or scope keys per client.

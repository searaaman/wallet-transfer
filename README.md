# wallet-transfer

Wallet-to-wallet transfer service in Go and PostgreSQL. Transfers are idempotent and recorded in a double-entry ledger, and they stay correct under concurrent requests.

The design (API contract, failure modes, concurrency and idempotency strategy) is in [DESIGN.md](DESIGN.md).

## Run

```sh
docker run -d --name wallet-pg -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=wallet -p 5433:5432 postgres:16
go run ./cmd/server          # applies migrations/001_init.sql on start, listens on :8080
```

`DATABASE_URL` and `ADDR` override the defaults.

Seed two wallets and make a transfer:

```sh
psql postgres://postgres:postgres@localhost:5433/wallet \
  -c "INSERT INTO wallets (id, balance) VALUES ('wallet_1', 1000), ('wallet_2', 0)"

curl -s localhost:8080/transfers -d '{"idempotencyKey":"abc123","fromWalletId":"wallet_1","toWalletId":"wallet_2","amount":100}'
curl -s localhost:8080/wallets/wallet_1
```

## Test

The tests need the Postgres above (or set `TEST_DATABASE_URL`). They truncate the tables.

```sh
go test -race ./...
```

## Layout

| Path | Layer |
|---|---|
| `internal/handler` | HTTP: decode and validate the JSON shape, map errors to status codes |
| `internal/service` | transfer workflow, idempotency, transaction boundary |
| `internal/repository` | SQL only |
| `internal/domain` | entities, validation rules, state transitions |
| `migrations` | schema |

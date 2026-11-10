CREATE TABLE IF NOT EXISTS wallets (
    id         TEXT PRIMARY KEY,
    balance    BIGINT NOT NULL CHECK (balance >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS transfers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key TEXT NOT NULL UNIQUE,
    from_wallet_id  TEXT NOT NULL REFERENCES wallets(id),
    to_wallet_id    TEXT NOT NULL REFERENCES wallets(id),
    amount          BIGINT NOT NULL CHECK (amount > 0),
    status          TEXT NOT NULL CHECK (status IN ('PENDING', 'PROCESSED', 'FAILED')),
    failure_reason  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_wallet_id <> to_wallet_id),
    CHECK ((status = 'FAILED') = (failure_reason IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS ledger_entries (
    id          BIGSERIAL PRIMARY KEY,
    transfer_id UUID NOT NULL REFERENCES transfers(id),
    wallet_id   TEXT NOT NULL REFERENCES wallets(id),
    entry_type  TEXT NOT NULL CHECK (entry_type IN ('DEBIT', 'CREDIT')),
    amount      BIGINT NOT NULL CHECK (amount > 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (transfer_id, entry_type)
);

CREATE INDEX IF NOT EXISTS ledger_entries_wallet_id_idx ON ledger_entries (wallet_id);

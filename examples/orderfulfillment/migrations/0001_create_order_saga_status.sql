-- Migration 0001: create order_saga_status read model table for the
-- orderfulfillment example's saga-journal CQRS projection.
--
-- This table is the durable read model populated by the saga-journal Tailer
-- (projection Apply handler HandleOrderEvent). Each row holds the latest
-- folded order status derived from saga journal events for that order.
--
-- Upsert semantics: the Tailer calls PGReadModel.Upsert inside the Tailer's
-- ambient transaction (same tx as AdvanceIfOwner), so status advances and
-- checkpoint advances commit atomically.

-- +goose Up
CREATE TABLE order_saga_status (
    order_id   TEXT        PRIMARY KEY,
    status     TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS order_saga_status;

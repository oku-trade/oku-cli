-- +goose Up
-- Durable sequential execution cursor.
ALTER TABLE onchain_intents ADD COLUMN next_step INTEGER NOT NULL DEFAULT 0;
ALTER TABLE onchain_intents ADD COLUMN execution_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE onchain_intents ADD COLUMN step_inflight INTEGER NOT NULL DEFAULT 0;
ALTER TABLE onchain_intents ADD COLUMN approval_skipped INTEGER NOT NULL DEFAULT 0;
-- +goose Down
ALTER TABLE onchain_intents DROP COLUMN approval_skipped;
ALTER TABLE onchain_intents DROP COLUMN step_inflight;
ALTER TABLE onchain_intents DROP COLUMN execution_mode;
ALTER TABLE onchain_intents DROP COLUMN next_step;

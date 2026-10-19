-- +goose Up
-- Batch metadata for atomic wallet_sendCalls executions: the agent nonce and
-- block recorded before submission, plus Privy's batch transaction ID.
ALTER TABLE onchain_intents ADD COLUMN batch_json TEXT;
-- +goose Down
ALTER TABLE onchain_intents DROP COLUMN batch_json;

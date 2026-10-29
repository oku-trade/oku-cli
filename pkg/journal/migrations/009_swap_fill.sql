-- +goose Up
-- Actual swap fill read from the confirmed receipts: the amount the recipient
-- received and the gas paid. Kept outside intent_json so digests are unchanged.
ALTER TABLE onchain_intents ADD COLUMN fill_json TEXT;
-- +goose Down
ALTER TABLE onchain_intents DROP COLUMN fill_json;

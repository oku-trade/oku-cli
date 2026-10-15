-- +goose Up
CREATE TABLE onchain_intents (
 profile TEXT NOT NULL, intent_id TEXT NOT NULL, agent TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('send','deposit','swap','bridge')),
 chain_id INTEGER NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL,
 token TEXT NOT NULL, amount TEXT NOT NULL, digest TEXT NOT NULL,
 intent_json TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('prepared','sending','sent','confirmed','failed','unknown')),
 tx_hashes TEXT NOT NULL DEFAULT '[]', backend_order_id TEXT,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(profile,intent_id)
);
CREATE INDEX onchain_agent_state ON onchain_intents(agent,state);
-- +goose Down
DROP INDEX onchain_agent_state;
DROP TABLE onchain_intents;

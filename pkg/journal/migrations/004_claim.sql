-- +goose Up
CREATE TABLE onchain_intents_new (
 profile TEXT NOT NULL, intent_id TEXT NOT NULL, agent TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('send','deposit','swap','bridge','claim')),
 chain_id INTEGER NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL,
 token TEXT NOT NULL, amount TEXT NOT NULL, digest TEXT NOT NULL,
 intent_json TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('prepared','sending','sent','confirmed','failed','unknown')),
 tx_hashes TEXT NOT NULL DEFAULT '[]', backend_order_id TEXT,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, batch_json TEXT,
 PRIMARY KEY(profile,intent_id)
);
INSERT INTO onchain_intents_new SELECT profile,intent_id,agent,kind,chain_id,sender,recipient,token,amount,digest,intent_json,state,tx_hashes,backend_order_id,created_at,updated_at,batch_json FROM onchain_intents;
DROP TABLE onchain_intents;
ALTER TABLE onchain_intents_new RENAME TO onchain_intents;
CREATE INDEX onchain_agent_state ON onchain_intents(agent,state);
-- +goose Down
CREATE TABLE onchain_intents_old (
 profile TEXT NOT NULL, intent_id TEXT NOT NULL, agent TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('send','deposit','swap','bridge')),
 chain_id INTEGER NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL,
 token TEXT NOT NULL, amount TEXT NOT NULL, digest TEXT NOT NULL,
 intent_json TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('prepared','sending','sent','confirmed','failed','unknown')),
 tx_hashes TEXT NOT NULL DEFAULT '[]', backend_order_id TEXT,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, batch_json TEXT,
 PRIMARY KEY(profile,intent_id)
);
INSERT INTO onchain_intents_old SELECT * FROM onchain_intents WHERE kind!='claim';
DROP TABLE onchain_intents;
ALTER TABLE onchain_intents_old RENAME TO onchain_intents;
CREATE INDEX onchain_agent_state ON onchain_intents(agent,state);

-- +goose Up
-- Lending intents: earn (vault deposit/withdraw), borrow (Morpho Blue and
-- Euler collateral/debt actions) and loop (leveraged open via the backend).
-- 'lp' is listed so this applies the same whether or not 010_lp ran first.
CREATE TABLE onchain_intents_new (
 profile TEXT NOT NULL, intent_id TEXT NOT NULL, agent TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('send','deposit','swap','bridge','claim','lp','earn','borrow','loop')),
 chain_id INTEGER NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL,
 token TEXT NOT NULL, amount TEXT NOT NULL, digest TEXT NOT NULL,
 intent_json TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('prepared','sending','sent','confirmed','failed','unknown','submitted')),
 tx_hashes TEXT NOT NULL DEFAULT '[]', backend_order_id TEXT,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, batch_json TEXT,
 next_step INTEGER NOT NULL DEFAULT 0, execution_mode TEXT NOT NULL DEFAULT '',
 step_inflight INTEGER NOT NULL DEFAULT 0, approval_skipped INTEGER NOT NULL DEFAULT 0,
 fill_json TEXT,
 PRIMARY KEY(profile,intent_id)
);
INSERT INTO onchain_intents_new SELECT profile,intent_id,agent,kind,chain_id,sender,recipient,token,amount,digest,intent_json,state,tx_hashes,backend_order_id,created_at,updated_at,batch_json,next_step,execution_mode,step_inflight,approval_skipped,fill_json FROM onchain_intents;
DROP TABLE onchain_intents;
ALTER TABLE onchain_intents_new RENAME TO onchain_intents;
CREATE INDEX onchain_agent_state ON onchain_intents(agent,state);
CREATE INDEX onchain_agent_chain_state ON onchain_intents(agent,chain_id,state);
CREATE UNIQUE INDEX onchain_claim_parent ON onchain_intents(profile,json_extract(intent_json,'$.parent_intent')) WHERE kind='claim' AND state!='failed';
-- +goose Down
CREATE TABLE onchain_intents_old (
 profile TEXT NOT NULL, intent_id TEXT NOT NULL, agent TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('send','deposit','swap','bridge','claim','lp')),
 chain_id INTEGER NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL,
 token TEXT NOT NULL, amount TEXT NOT NULL, digest TEXT NOT NULL,
 intent_json TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('prepared','sending','sent','confirmed','failed','unknown','submitted')),
 tx_hashes TEXT NOT NULL DEFAULT '[]', backend_order_id TEXT,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, batch_json TEXT,
 next_step INTEGER NOT NULL DEFAULT 0, execution_mode TEXT NOT NULL DEFAULT '',
 step_inflight INTEGER NOT NULL DEFAULT 0, approval_skipped INTEGER NOT NULL DEFAULT 0,
 fill_json TEXT,
 PRIMARY KEY(profile,intent_id)
);
INSERT INTO onchain_intents_old SELECT * FROM onchain_intents WHERE kind NOT IN ('earn','borrow','loop');
DROP TABLE onchain_intents;
ALTER TABLE onchain_intents_old RENAME TO onchain_intents;
CREATE INDEX onchain_agent_state ON onchain_intents(agent,state);
CREATE INDEX onchain_agent_chain_state ON onchain_intents(agent,chain_id,state);
CREATE UNIQUE INDEX onchain_claim_parent ON onchain_intents(profile,json_extract(intent_json,'$.parent_intent')) WHERE kind='claim' AND state!='failed';

-- +goose Up
CREATE INDEX onchain_agent_chain_state ON onchain_intents(agent,chain_id,state);
CREATE UNIQUE INDEX onchain_claim_parent ON onchain_intents(profile,json_extract(intent_json,'$.parent_intent')) WHERE kind='claim';
-- +goose Down
DROP INDEX onchain_claim_parent;
DROP INDEX onchain_agent_chain_state;

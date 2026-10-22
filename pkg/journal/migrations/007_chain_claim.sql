-- +goose Up
CREATE INDEX onchain_agent_chain_state ON onchain_intents(agent,chain_id,state);
-- Older journals allowed a new claim after a failed one, so a parent can
-- already have several claims. Keep those rows (history) and enforce
-- uniqueness only for claims that are not failed, which is what
-- ClaimForBridge previously allowed at most one of.
CREATE UNIQUE INDEX onchain_claim_parent ON onchain_intents(profile,json_extract(intent_json,'$.parent_intent')) WHERE kind='claim' AND state!='failed';
-- +goose Down
DROP INDEX onchain_claim_parent;
DROP INDEX onchain_agent_chain_state;

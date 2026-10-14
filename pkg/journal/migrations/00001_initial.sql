-- +goose Up
CREATE TABLE records (
 profile TEXT NOT NULL, intent_id TEXT NOT NULL, account TEXT NOT NULL, network TEXT NOT NULL,
 origin TEXT NOT NULL, signer TEXT NOT NULL DEFAULT '', digest TEXT NOT NULL,
 intent_json TEXT NOT NULL, state TEXT NOT NULL, action_id TEXT NOT NULL DEFAULT '',
 action_json TEXT NOT NULL DEFAULT '', cloid TEXT NOT NULL DEFAULT '', nonce INTEGER NOT NULL DEFAULT 0,
 server_state TEXT NOT NULL DEFAULT '', order_id INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(profile,intent_id)
);
CREATE INDEX records_account ON records(network,account,state);
CREATE TABLE transitions (
 profile TEXT NOT NULL, intent_id TEXT NOT NULL, sequence INTEGER NOT NULL,
 at TEXT NOT NULL, state TEXT NOT NULL, note TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(profile,intent_id,sequence),
 FOREIGN KEY(profile,intent_id) REFERENCES records(profile,intent_id) ON DELETE RESTRICT
);
CREATE TABLE nonces (signer TEXT PRIMARY KEY, last_nonce INTEGER NOT NULL CHECK(last_nonce > 0));

-- +goose Down
DROP TABLE nonces;
DROP TABLE transitions;
DROP TABLE records;

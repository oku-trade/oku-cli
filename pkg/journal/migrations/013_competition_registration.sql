-- +goose Up
CREATE TABLE competition_registration (
 profile TEXT NOT NULL, network TEXT NOT NULL, account TEXT NOT NULL, origin TEXT NOT NULL,
 agent TEXT NOT NULL, message TEXT NOT NULL, challenge_token TEXT NOT NULL, expires_at TEXT NOT NULL,
 PRIMARY KEY(profile,network,account,origin,agent)
);

-- +goose Down
DROP TABLE competition_registration;

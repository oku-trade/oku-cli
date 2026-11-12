-- +goose Up
CREATE TABLE chases (
    profile TEXT NOT NULL,
    intent_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (profile, intent_id),
    FOREIGN KEY (profile, intent_id) REFERENCES records(profile, intent_id)
);
CREATE TABLE chase_children (
    profile TEXT NOT NULL,
    child_id TEXT NOT NULL,
    parent_id TEXT NOT NULL,
    PRIMARY KEY (profile, child_id),
    FOREIGN KEY (profile, child_id) REFERENCES records(profile, intent_id),
    FOREIGN KEY (profile, parent_id) REFERENCES chases(profile, intent_id)
);

-- +goose Down
DROP TABLE chase_children;
DROP TABLE chases;

CREATE TABLE IF NOT EXISTS hash_crack_cache (
    hash_type VARCHAR(16) NOT NULL,
    mode VARCHAR(32) NOT NULL,
    hash_value VARCHAR(128) NOT NULL,
    engine VARCHAR(32) NOT NULL,
    ruleset VARCHAR(64) NOT NULL,
    cracked BOOLEAN NOT NULL DEFAULT FALSE,
    plaintext TEXT NULL,
    timed_out BOOLEAN NOT NULL DEFAULT FALSE,
    elapsed_ms INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (hash_type, mode, hash_value)
);

ALTER TABLE pastes
    ADD COLUMN burn_after_read BOOLEAN NOT NULL DEFAULT FALSE AFTER size_bytes,
    ADD COLUMN max_views INT NULL AFTER burn_after_read,
    ADD COLUMN view_count INT NOT NULL DEFAULT 0 AFTER max_views;

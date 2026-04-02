CREATE TABLE IF NOT EXISTS pastes (
    id VARCHAR(24) PRIMARY KEY,
    session_token VARCHAR(64) NOT NULL,
    content_kind ENUM('text','json') NOT NULL DEFAULT 'text',
    content LONGTEXT NOT NULL,
    size_bytes INT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NOT NULL,
    INDEX idx_session (session_token),
    INDEX idx_expires (expires_at)
);

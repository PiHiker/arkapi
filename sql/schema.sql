-- ============================================
-- ArkAPI Database Schema
-- Run this once: mysql -u root -p < sql/schema.sql
-- ============================================

CREATE DATABASE IF NOT EXISTS arkapi CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
USE arkapi;

-- Sessions: each funded session gets a token
CREATE TABLE sessions (
    token VARCHAR(64) PRIMARY KEY,
    balance_sats BIGINT NOT NULL DEFAULT 0,
    status ENUM('awaiting_payment','active','expired') DEFAULT 'active',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMP NULL,
    expires_at TIMESTAMP NULL,
    funding_method VARCHAR(16) NULL,
    INDEX idx_status (status),
    INDEX idx_expires (expires_at)
);

-- Call log: every API call recorded
CREATE TABLE call_log (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    session_token VARCHAR(64) NOT NULL,
    endpoint VARCHAR(100) NOT NULL,
    cost_sats INT NOT NULL,
    response_ms INT NOT NULL DEFAULT 0,
    status_code SMALLINT NOT NULL DEFAULT 200,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_session (session_token),
    INDEX idx_endpoint (endpoint),
    INDEX idx_created (created_at)
);

-- Create your database user separately for your own environment.
-- The helper script in scripts/setup.sh can do that for local deployments.

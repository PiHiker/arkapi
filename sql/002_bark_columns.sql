-- Add columns for bark payment integration
-- Run: sudo mysql arkapi < sql/002_bark_columns.sql

ALTER TABLE sessions ADD COLUMN payment_hash VARCHAR(255) NULL AFTER expires_at;
ALTER TABLE sessions ADD COLUMN lightning_invoice TEXT NULL AFTER payment_hash;
ALTER TABLE sessions ADD INDEX idx_payment_hash (payment_hash);
ALTER TABLE sessions ADD INDEX idx_awaiting (status, payment_hash);

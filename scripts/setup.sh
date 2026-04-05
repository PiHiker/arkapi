#!/bin/bash
# ============================================
# ArkAPI — example server setup script
# Run on your Linux host to get everything ready
# Usage: sudo bash scripts/setup.sh
# ============================================

set -e

write_secret_file() {
    local path="$1"
    local content="$2"
    umask 077
    printf "%s\n" "$content" >"$path"
}

echo ""
echo "=============================="
echo "  ArkAPI Server Setup"
echo "=============================="
echo ""

# --- Check prerequisites ---
echo "Checking prerequisites..."

command -v go >/dev/null 2>&1 || {
    echo "Go not found. Installing..."
    wget -q https://go.dev/dl/go1.22.5.linux-amd64.tar.gz
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf go1.22.5.linux-amd64.tar.gz
    rm go1.22.5.linux-amd64.tar.gz
    echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
    export PATH=$PATH:/usr/local/go/bin
    echo "  Go installed: $(go version)"
}

command -v dig >/dev/null 2>&1 || {
    echo "Installing dig (dnsutils)..."
    sudo apt-get install -y dnsutils
}

command -v whois >/dev/null 2>&1 || {
    echo "Installing whois..."
    sudo apt-get install -y whois
}

echo "  Go:    $(go version 2>/dev/null | head -1)"
echo "  dig:   $(dig -v 2>&1 | head -1)"
echo "  whois: $(whois --version 2>&1 | head -1 || echo 'installed')"
echo "  MySQL: $(mysql --version 2>/dev/null | head -1)"
echo ""

# --- Set up database ---
echo "Setting up MySQL database..."
echo "Enter your MySQL root password:"
read -s MYSQL_ROOT_PASS

# Generate a random password for the arkapi user
ARKAPI_PASS=$(openssl rand -hex 16)

mysql -u root -p"$MYSQL_ROOT_PASS" <<EOF
CREATE DATABASE IF NOT EXISTS arkapi CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE USER IF NOT EXISTS 'arkapi'@'localhost' IDENTIFIED BY '${ARKAPI_PASS}';
GRANT ALL PRIVILEGES ON arkapi.* TO 'arkapi'@'localhost';
FLUSH PRIVILEGES;

USE arkapi;

CREATE TABLE IF NOT EXISTS sessions (
    token VARCHAR(64) PRIMARY KEY,
    balance_sats BIGINT NOT NULL DEFAULT 0,
    status ENUM('awaiting_payment','active','expired') DEFAULT 'active',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMP NULL,
    expires_at TIMESTAMP NULL,
    INDEX idx_status (status),
    INDEX idx_expires (expires_at)
);

CREATE TABLE IF NOT EXISTS call_log (
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

CREATE TABLE IF NOT EXISTS earnings (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    amount_sats BIGINT NOT NULL,
    source_session VARCHAR(64),
    endpoint VARCHAR(100),
    type ENUM('call_revenue','withdrawal') NOT NULL,
    tx_id VARCHAR(255),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
EOF

echo "  Database created"
echo "  User: arkapi"
echo "  Password generated"
echo ""

# --- Build the binary ---
echo "Building ArkAPI..."
cd "$(dirname "$0")/.."
go mod tidy
go build -o arkapi ./cmd/arkapi
echo "  Binary built: ./arkapi"
echo ""

# --- Create systemd service ---
echo "Creating systemd service..."
INSTALL_DIR="${ARKAPI_INSTALL_DIR:-/opt/arkapi}"
sudo mkdir -p "$INSTALL_DIR"
sudo cp arkapi "$INSTALL_DIR/"
TEMP_SECRET_FILE="$(mktemp)"
write_secret_file "$TEMP_SECRET_FILE" "ARKAPI_DB_USER=arkapi
ARKAPI_DB_PASS=$ARKAPI_PASS
ARKAPI_DB_HOST=localhost
ARKAPI_DB_NAME=arkapi
ARKAPI_PORT=${ARKAPI_PORT:-8080}"
sudo install -m 600 "$TEMP_SECRET_FILE" "$INSTALL_DIR/arkapi.env"
rm -f "$TEMP_SECRET_FILE"

sudo tee /etc/systemd/system/arkapi.service > /dev/null <<EOF
[Unit]
Description=ArkAPI API Proxy
After=network.target mysql.service

[Service]
Type=simple
User=www-data
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/arkapi
Restart=always
RestartSec=5
EnvironmentFile=$INSTALL_DIR/arkapi.env

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable arkapi
sudo systemctl start arkapi
echo "  Service created and started"
echo ""

# --- Summary ---
echo "=============================="
echo "  Setup Complete!"
echo "=============================="
echo ""
echo "ArkAPI is running on 127.0.0.1:8080"
echo ""
echo "Next steps:"
echo "  1. Add Apache vhost (see README.md)"
echo "  2. Point your domain or reverse proxy at this server"
echo "  3. Run: bash scripts/test.sh"
echo ""
echo "MySQL credentials:"
echo "  User: arkapi"
echo "  Environment file: $INSTALL_DIR/arkapi.env"
echo "  DB:   arkapi"
echo ""
echo "Manage the service:"
echo "  sudo systemctl status arkapi"
echo "  sudo journalctl -u arkapi -f"
echo ""

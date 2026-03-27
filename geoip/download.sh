#!/bin/bash
# Download DB-IP Lite databases (free, no account required).
# https://db-ip.com/db/lite.php — Creative Commons Attribution 4.0 License.
# Updated monthly. Run via cron on the 2nd of each month.

set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
YEAR_MONTH="$(date +%Y-%m)"

echo "[$(date)] Downloading dbip-city-lite (${YEAR_MONTH})..."
curl -sSL "https://download.db-ip.com/free/dbip-city-lite-${YEAR_MONTH}.mmdb.gz" \
    | gunzip > "$DIR/GeoLite2-City.mmdb.tmp"
mv "$DIR/GeoLite2-City.mmdb.tmp" "$DIR/GeoLite2-City.mmdb"

echo "[$(date)] Downloading dbip-asn-lite (${YEAR_MONTH})..."
curl -sSL "https://download.db-ip.com/free/dbip-asn-lite-${YEAR_MONTH}.mmdb.gz" \
    | gunzip > "$DIR/GeoLite2-ASN.mmdb.tmp"
mv "$DIR/GeoLite2-ASN.mmdb.tmp" "$DIR/GeoLite2-ASN.mmdb"

echo "[$(date)] Restarting arkapi container..."
docker compose up -d --force-recreate arkapi

echo "[$(date)] Done. Files:"
ls -lh "$DIR"/*.mmdb

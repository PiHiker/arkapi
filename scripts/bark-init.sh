#!/bin/bash
set -e

DATADIR="${BARK_DATADIR:-/root/.bark}"
ARK_SERVER="${BARK_ARK_SERVER:-ark.signet.2nd.dev}"
ESPLORA="${BARK_ESPLORA:-esplora.signet.2nd.dev}"

# Check if wallet already exists
if [ ! -f "$DATADIR/db.sqlite" ]; then
    echo "=== No wallet found, creating new signet wallet ==="
    bark create --signet \
        --ark "$ARK_SERVER" \
        --esplora "$ESPLORA" \
        --datadir "$DATADIR"
    echo "=== Wallet created ==="
    echo ""
    echo "=== MERCHANT SEED PHRASE (back this up!) ==="
    cat "$DATADIR/mnemonic"
    echo ""
    echo "============================================="
fi

echo "=== Wallet address ==="
bark --datadir "$DATADIR" address
echo ""

echo "=== Starting barkd daemon ==="
exec barkd --datadir "$DATADIR" --host 0.0.0.0 --port 3000

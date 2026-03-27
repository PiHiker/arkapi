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
    echo "=== SEED PHRASE (back this up) ==="
    cat "$DATADIR/mnemonic"
    echo ""
    echo "================================="
fi

echo "=== Wallet address ==="
bark --datadir "$DATADIR" address
echo ""

echo "=== Seed phrase ==="
cat "$DATADIR/mnemonic"
echo ""
echo "================================="

echo ""
echo "Consumer wallet ready. Container will stay alive for CLI access."
echo "Usage:"
echo "  docker exec bark-consumer bark address"
echo "  docker exec bark-consumer bark balance"
echo "  docker exec bark-consumer bark send <invoice>"
echo ""

# Keep container alive — no daemon needed, just CLI access
exec sleep infinity

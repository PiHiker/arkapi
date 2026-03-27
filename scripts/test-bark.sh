#!/bin/bash
# ============================================
# ArkAPI — Bark Payment Integration Test
# Tests the full Lightning invoice payment flow
# Requires: a funded consumer bark wallet at CONSUMER_DATADIR
# Usage: bash scripts/test-bark.sh
# ============================================

BASE="http://127.0.0.1:8080"
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Consumer wallet — separate bark container simulating a customer
CONSUMER_BARK="sudo docker exec bark-consumer bark"

echo ""
echo "=============================="
echo "  ArkAPI Bark Payment Test"
echo "=============================="
echo ""

# --- Step 1: Create session (should return Lightning invoice) ---
echo -n "1. Create session (bark mode)... "
SESSION=$(curl -s -X POST -H "Content-Type: application/json" \
    -d '{"amount_sats": 1000}' "$BASE/v1/sessions")

TOKEN=$(echo "$SESSION" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
INVOICE=$(echo "$SESSION" | grep -o '"lightning_invoice":"[^"]*"' | cut -d'"' -f4)
STATUS=$(echo "$SESSION" | grep -o '"status":"[^"]*"' | cut -d'"' -f4)

if [ -n "$TOKEN" ] && [ -n "$INVOICE" ] && [ "$STATUS" = "awaiting_payment" ]; then
    echo -e "${GREEN}PASS${NC} — got invoice, status=awaiting_payment"
    echo "   Token: ${TOKEN:0:25}..."
    echo "   Invoice: ${INVOICE:0:40}..."
else
    echo -e "${RED}FAIL${NC}"
    echo "$SESSION"
    exit 1
fi

# --- Step 2: Verify session is not yet active ---
echo -n "2. Verify session not yet active... "
BALANCE=$(curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/balance")
if echo "$BALANCE" | grep -q 'payment not yet received'; then
    echo -e "${GREEN}PASS${NC} — correctly returns 402"
else
    echo -e "${RED}FAIL${NC} — expected 402"
    echo "$BALANCE"
fi

# --- Step 3: Pay the invoice from consumer wallet ---
echo -n "3. Paying invoice from consumer wallet... "
PAY_RESULT=$($CONSUMER_BARK send "$INVOICE" 2>&1)
if [ $? -eq 0 ]; then
    echo -e "${GREEN}PASS${NC} — payment sent"
else
    echo -e "${RED}FAIL${NC} — payment failed"
    echo "$PAY_RESULT"
    echo ""
    echo "Manual payment: Use a signet Lightning wallet to pay:"
    echo "  $INVOICE"
    echo ""
    echo "Then re-run this script's remaining checks manually:"
    echo "  curl -H 'Authorization: Bearer $TOKEN' $BASE/v1/balance"
    exit 1
fi

# --- Step 4: Poll until session activates ---
echo -n "4. Waiting for session activation... "
for i in $(seq 1 30); do
    BALANCE=$(curl -s -H "Authorization: Bearer $TOKEN" "$BASE/v1/balance")
    if echo "$BALANCE" | grep -q '"status":"active"'; then
        SATS=$(echo "$BALANCE" | grep -o '"balance_sats":[0-9]*' | cut -d: -f2)
        echo -e "${GREEN}PASS${NC} — activated with $SATS sats (took ~${i}s)"
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo -e "${RED}FAIL${NC} — timed out after 30s"
        echo "$BALANCE"
        exit 1
    fi
    sleep 1
done

# --- Step 5: Make an API call ---
echo -n "5. API call (DNS lookup)... "
DNS=$(curl -s -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{"domain":"example.com"}' "$BASE/api/dns-lookup")
if echo "$DNS" | grep -q '"success":true'; then
    REMAINING=$(echo "$DNS" | grep -o '"balance_remaining":[0-9]*' | cut -d: -f2)
    echo -e "${GREEN}PASS${NC} — balance after call: $REMAINING sats"
else
    echo -e "${RED}FAIL${NC}"
    echo "$DNS"
fi

echo ""
echo "=============================="
echo "  Bark payment test complete"
echo "=============================="
echo ""
echo "Session token: $TOKEN"
echo ""

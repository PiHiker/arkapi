#!/bin/bash
# ============================================
# ArkAPI — Quick test script
# Run this after starting the proxy to verify everything works
# Usage: bash scripts/test.sh
# ============================================

BASE="http://127.0.0.1:8080"
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo ""
echo "=============================="
echo "  ArkAPI Test Suite"
echo "=============================="
echo ""

# --- Health check ---
echo -n "Health check... "
HEALTH=$(curl -s "$BASE/health")
if echo "$HEALTH" | grep -q '"status":"ok"'; then
    echo -e "${GREEN}PASS${NC}"
else
    echo -e "${RED}FAIL${NC} — is the proxy running on port 8080?"
    echo "$HEALTH"
    exit 1
fi

# --- Catalog ---
echo -n "API catalog... "
CATALOG=$(curl -s "$BASE/v1/catalog")
if echo "$CATALOG" | grep -q 'dns-lookup'; then
    echo -e "${GREEN}PASS${NC}"
else
    echo -e "${RED}FAIL${NC}"
    echo "$CATALOG"
fi

# --- Create session ---
echo -n "Create session... "
SESSION=$(curl -s -X POST "$BASE/v1/sessions")
TOKEN=$(echo "$SESSION" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
if [ -n "$TOKEN" ]; then
    echo -e "${GREEN}PASS${NC} — token: ${TOKEN:0:20}..."
else
    echo -e "${RED}FAIL${NC}"
    echo "$SESSION"
    exit 1
fi

AUTH="Authorization: Bearer $TOKEN"

# --- Check balance ---
echo -n "Check balance... "
BALANCE=$(curl -s -H "$AUTH" "$BASE/v1/balance")
if echo "$BALANCE" | grep -q '"balance_sats":10000'; then
    echo -e "${GREEN}PASS${NC} — 10,000 sats"
else
    echo -e "${RED}FAIL${NC}"
    echo "$BALANCE"
fi

# --- DNS Lookup ---
echo -n "DNS lookup (example.com)... "
DNS=$(curl -s -H "$AUTH" -H "Content-Type: application/json" \
    -d '{"domain":"example.com"}' "$BASE/api/dns-lookup")
if echo "$DNS" | grep -q '"success":true'; then
    COST=$(echo "$DNS" | grep -o '"cost_sats":[0-9]*' | cut -d: -f2)
    REMAINING=$(echo "$DNS" | grep -o '"balance_remaining":[0-9]*' | cut -d: -f2)
    echo -e "${GREEN}PASS${NC} — cost: ${COST} sats, remaining: ${REMAINING}"
else
    echo -e "${YELLOW}WARN${NC} — may need 'dig' installed"
    echo "$DNS" | head -1
fi

# --- SSL Check ---
echo -n "SSL check (google.com)... "
SSL=$(curl -s -H "$AUTH" -H "Content-Type: application/json" \
    -d '{"domain":"google.com"}' "$BASE/api/ssl-check")
if echo "$SSL" | grep -q '"success":true'; then
    DAYS=$(echo "$SSL" | grep -o '"days_remaining":[0-9]*' | cut -d: -f2)
    echo -e "${GREEN}PASS${NC} — cert expires in ${DAYS} days"
else
    echo -e "${RED}FAIL${NC}"
    echo "$SSL" | head -1
fi

# --- Headers ---
echo -n "Headers check (google.com)... "
HDRS=$(curl -s -H "$AUTH" -H "Content-Type: application/json" \
    -d '{"url":"https://google.com"}' "$BASE/api/headers")
if echo "$HDRS" | grep -q '"success":true'; then
    GRADE=$(echo "$HDRS" | grep -o '"grade":"[^"]*"' | cut -d'"' -f4)
    echo -e "${GREEN}PASS${NC} — security grade: ${GRADE}"
else
    echo -e "${RED}FAIL${NC}"
    echo "$HDRS" | head -1
fi

# --- Weather ---
echo -n "Weather (New York)... "
WX=$(curl -s -H "$AUTH" -H "Content-Type: application/json" \
    -d '{"city":"New York"}' "$BASE/api/weather")
if echo "$WX" | grep -q '"success":true'; then
    echo -e "${GREEN}PASS${NC}"
else
    echo -e "${YELLOW}WARN${NC} — needs outbound HTTP to api.open-meteo.com"
    echo "$WX" | head -1
fi

# --- IP Lookup ---
echo -n "IP lookup (8.8.8.8)... "
IP=$(curl -s -H "$AUTH" -H "Content-Type: application/json" \
    -d '{"ip":"8.8.8.8"}' "$BASE/api/ip-lookup")
if echo "$IP" | grep -q '"success":true'; then
    CITY=$(echo "$IP" | grep -o '"city":"[^"]*"' | cut -d'"' -f4)
    echo -e "${GREEN}PASS${NC} — location: ${CITY}"
else
    echo -e "${YELLOW}WARN${NC} — needs outbound HTTP to ip-api.com"
    echo "$IP" | head -1
fi

# --- WHOIS ---
echo -n "WHOIS (google.com)... "
WHOIS=$(curl -s -H "$AUTH" -H "Content-Type: application/json" \
    -d '{"domain":"google.com"}' "$BASE/api/whois")
if echo "$WHOIS" | grep -q '"success":true'; then
    REG=$(echo "$WHOIS" | grep -o '"registrar":"[^"]*"' | cut -d'"' -f4)
    echo -e "${GREEN}PASS${NC} — registrar: ${REG:0:30}"
else
    echo -e "${YELLOW}WARN${NC} — may need 'whois' installed"
    echo "$WHOIS" | head -1
fi

# --- Final balance ---
echo ""
echo -n "Final balance... "
FINAL=$(curl -s -H "$AUTH" "$BASE/v1/balance")
REMAINING=$(echo "$FINAL" | grep -o '"balance_sats":[0-9]*' | cut -d: -f2)
SPENT=$((10000 - REMAINING))
echo -e "${GREEN}${REMAINING} sats remaining${NC} (spent ${SPENT} sats across all calls)"

echo ""
echo "=============================="
echo "  All tests complete"
echo "=============================="
echo ""
echo "Your session token for manual testing:"
echo "  $TOKEN"
echo ""
echo "Try it yourself:"
echo "  curl -H 'Authorization: Bearer $TOKEN' \\"
echo "       -d '{\"domain\":\"yoursite.com\"}' \\"
echo "       $BASE/api/dns-lookup"
echo ""

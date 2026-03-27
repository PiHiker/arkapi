package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// BitcoinAddressRequest is what the consumer sends
type BitcoinAddressRequest struct {
	Address string `json:"address"`
}

// BitcoinAddressBalance holds on-chain balance data
type BitcoinAddressBalance struct {
	ConfirmedSats   int64 `json:"confirmed_sats"`
	UnconfirmedSats int64 `json:"unconfirmed_sats"`
	TxCount         int   `json:"tx_count"`
}

// BitcoinAddressResponse is what we return
type BitcoinAddressResponse struct {
	Address     string                 `json:"address"`
	Valid       bool                   `json:"valid"`
	Type        string                 `json:"type,omitempty"`
	Network     string                 `json:"network,omitempty"`
	Format      string                 `json:"format,omitempty"`
	Description string                 `json:"description,omitempty"`
	Balance     *BitcoinAddressBalance `json:"balance"`
	Cached      bool                   `json:"cached"`
}

// --- Cache ---

const btcAddrCacheTTL = 1 * time.Hour

type btcAddrCacheEntry struct {
	response  *BitcoinAddressResponse
	expiresAt time.Time
}

var btcAddrCache = struct {
	mu    sync.RWMutex
	items map[string]btcAddrCacheEntry
}{
	items: make(map[string]btcAddrCacheEntry),
}

func getCachedBtcAddr(address string) *BitcoinAddressResponse {
	btcAddrCache.mu.RLock()
	entry, ok := btcAddrCache.items[address]
	btcAddrCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.Balance != nil {
		b := *entry.response.Balance
		clone.Balance = &b
	}
	clone.Cached = true
	return &clone
}

func setCachedBtcAddr(address string, resp *BitcoinAddressResponse) {
	clone := *resp
	if resp.Balance != nil {
		b := *resp.Balance
		clone.Balance = &b
	}
	clone.Cached = false
	btcAddrCache.mu.Lock()
	btcAddrCache.items[address] = btcAddrCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(btcAddrCacheTTL),
	}
	btcAddrCache.mu.Unlock()
}

// --- Address validation ---

type addrInfo struct {
	typ         string // p2pkh, p2sh, p2wpkh, p2wsh, p2tr
	network     string // mainnet
	format      string // base58, bech32, bech32m
	description string
}

func validateBitcoinAddress(address string) *addrInfo {
	if len(address) < 14 || len(address) > 90 {
		return nil
	}

	// Bech32/Bech32m addresses
	lower := strings.ToLower(address)
	if strings.HasPrefix(lower, "bc1") {
		if address != lower && address != strings.ToUpper(address) {
			return nil
		}
		return validateBech32Address(address, lower)
	}

	// Base58 addresses
	return validateBase58Address(address)
}

func validateBech32Address(address, lower string) *addrInfo {
	network := "mainnet"

	// Decode bech32 to check validity
	hrp, data, bech32Version := bech32Decode(lower)
	if hrp == "" || data == nil {
		return nil
	}

	// HRP must match
	if network == "mainnet" && hrp != "bc" {
		return nil
	}
	if len(data) < 1 {
		return nil
	}

	witnessVersion := data[0]
	witnessProgram := bech32ConvertBits(data[1:], 5, 8, false)
	if witnessProgram == nil {
		return nil
	}

	progLen := len(witnessProgram)

	switch witnessVersion {
	case 0:
		if bech32Version != 1 {
			return nil // v0 must use bech32, not bech32m
		}
		if progLen == 20 {
			return &addrInfo{"p2wpkh", network, "bech32", "Native SegWit v0 (P2WPKH)"}
		}
		if progLen == 32 {
			return &addrInfo{"p2wsh", network, "bech32", "Native SegWit v0 (P2WSH)"}
		}
		return nil
	case 1:
		if bech32Version != 2 {
			return nil // v1 must use bech32m
		}
		if progLen == 32 {
			return &addrInfo{"p2tr", network, "bech32m", "Taproot (P2TR)"}
		}
		return nil
	default:
		// Future witness versions (2-16)
		if witnessVersion > 16 {
			return nil
		}
		if bech32Version != 2 {
			return nil
		}
		if progLen < 2 || progLen > 40 {
			return nil
		}
		return &addrInfo{
			fmt.Sprintf("witness_v%d", witnessVersion),
			network,
			"bech32m",
			fmt.Sprintf("Witness v%d program", witnessVersion),
		}
	}
}

func validateBase58Address(address string) *addrInfo {
	decoded := base58CheckDecode(address)
	if decoded == nil || len(decoded) != 21 {
		return nil
	}

	version := decoded[0]
	switch version {
	case 0x00:
		return &addrInfo{"p2pkh", "mainnet", "base58", "Legacy (P2PKH)"}
	case 0x05:
		return &addrInfo{"p2sh", "mainnet", "base58", "Script Hash (P2SH)"}
	default:
		return nil
	}
}

// --- Base58Check decoding ---

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58CheckDecode(address string) []byte {
	// Decode base58
	result := make([]byte, 0, 25)
	for _, c := range address {
		idx := strings.IndexRune(base58Alphabet, c)
		if idx < 0 {
			return nil
		}
		carry := idx
		for i := range result {
			carry += int(result[i]) * 58
			result[i] = byte(carry & 0xff)
			carry >>= 8
		}
		for carry > 0 {
			result = append(result, byte(carry&0xff))
			carry >>= 8
		}
	}

	// Handle leading '1's (leading zeros in base58)
	for _, c := range address {
		if c != '1' {
			break
		}
		result = append(result, 0)
	}

	// Reverse
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	if len(result) < 4 {
		return nil
	}

	// Verify checksum (last 4 bytes)
	payload := result[:len(result)-4]
	checksum := result[len(result)-4:]
	hash := doubleSHA256(payload)
	if hash[0] != checksum[0] || hash[1] != checksum[1] || hash[2] != checksum[2] || hash[3] != checksum[3] {
		return nil
	}

	return payload
}

// --- Bech32 decoding ---

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

func bech32Decode(addr string) (string, []byte, int) {
	// Find the last '1' separator
	sepIdx := strings.LastIndex(addr, "1")
	if sepIdx < 1 || sepIdx+7 > len(addr) {
		return "", nil, 0
	}

	hrp := addr[:sepIdx]
	dataStr := addr[sepIdx+1:]

	if len(dataStr) < 6 {
		return "", nil, 0
	}

	data := make([]byte, len(dataStr))
	for i, c := range dataStr {
		idx := strings.IndexRune(bech32Charset, c)
		if idx < 0 {
			return "", nil, 0
		}
		data[i] = byte(idx)
	}

	// Verify checksum — try bech32 first, then bech32m
	if bech32VerifyChecksum(hrp, data, 1) {
		return hrp, data[:len(data)-6], 1
	}
	if bech32VerifyChecksum(hrp, data, 0x2bc830a3) {
		return hrp, data[:len(data)-6], 2
	}

	return "", nil, 0
}

func bech32VerifyChecksum(hrp string, data []byte, constant uint32) bool {
	values := bech32HRPExpand(hrp)
	values = append(values, data...)
	return bech32Polymod(values) == constant
}

func bech32HRPExpand(hrp string) []byte {
	result := make([]byte, 0, len(hrp)*2+1)
	for _, c := range hrp {
		result = append(result, byte(c>>5))
	}
	result = append(result, 0)
	for _, c := range hrp {
		result = append(result, byte(c&31))
	}
	return result
}

func bech32Polymod(values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (top>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

func bech32ConvertBits(data []byte, fromBits, toBits int, pad bool) []byte {
	acc := 0
	bits := 0
	maxV := (1 << toBits) - 1
	var result []byte
	for _, v := range data {
		acc = (acc << fromBits) | int(v)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			result = append(result, byte((acc>>bits)&maxV))
		}
	}
	if pad {
		if bits > 0 {
			result = append(result, byte((acc<<(toBits-bits))&maxV))
		}
	} else if bits >= fromBits || (acc<<(toBits-bits))&maxV != 0 {
		return nil
	}
	return result
}

func doubleSHA256(data []byte) [32]byte {
	first := sha256.Sum256(data)
	return sha256.Sum256(first[:])
}

// --- Mempool.space balance lookup ---

type mempoolAddressResponse struct {
	ChainStats struct {
		FundedSats int64 `json:"funded_txo_sum"`
		SpentSats  int64 `json:"spent_txo_sum"`
		TxCount    int   `json:"tx_count"`
	} `json:"chain_stats"`
	MempoolStats struct {
		FundedSats int64 `json:"funded_txo_sum"`
		SpentSats  int64 `json:"spent_txo_sum"`
	} `json:"mempool_stats"`
}

func lookupBalance(address, network string) *BitcoinAddressBalance {
	var baseURL string
	switch network {
	case "mainnet":
		baseURL = "https://mempool.space/api"
	default:
		return nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(baseURL + "/address/" + address)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil
	}

	var data mempoolAddressResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	confirmed := data.ChainStats.FundedSats - data.ChainStats.SpentSats
	unconfirmed := data.MempoolStats.FundedSats - data.MempoolStats.SpentSats

	return &BitcoinAddressBalance{
		ConfirmedSats:   confirmed,
		UnconfirmedSats: unconfirmed,
		TxCount:         data.ChainStats.TxCount,
	}
}

// --- Handler ---

// BitcoinAddressValidate handles /api/bitcoin-address
// Cost: 3 sats
func (h *Handler) BitcoinAddressValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req BitcoinAddressRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"address\": \"bc1q...\"}"})
		return
	}

	req.Address = strings.TrimSpace(req.Address)
	if req.Address == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}

	h.executeHandler(w, r, "/api/bitcoin-address", h.Cfg.BitcoinAddressCostSats, func() (interface{}, error) {
		// Check cache first
		if cached := getCachedBtcAddr(req.Address); cached != nil {
			return cached, nil
		}

		// Validate the address
		info := validateBitcoinAddress(req.Address)
		resp := &BitcoinAddressResponse{
			Address: req.Address,
			Valid:   info != nil,
			Cached:  false,
		}

		if info == nil {
			// Invalid address — still return a result, just with valid=false
			return resp, nil
		}

		resp.Type = info.typ
		resp.Network = info.network
		resp.Format = info.format
		resp.Description = info.description

		// Look up balance from mempool.space
		resp.Balance = lookupBalance(req.Address, info.network)

		// Cache the result
		setCachedBtcAddr(req.Address, resp)

		return resp, nil
	})
}

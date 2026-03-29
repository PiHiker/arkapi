package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PiHiker/arkapi/internal/database"
)

type HashCrackRequest struct {
	Hash string `json:"hash"`
	Type string `json:"type"`
	Mode string `json:"mode,omitempty"`
}

type HashCrackResponse struct {
	Hash      string `json:"hash"`
	Type      string `json:"type"`
	Mode      string `json:"mode"`
	Engine    string `json:"engine"`
	Ruleset   string `json:"ruleset"`
	Cracked   bool   `json:"cracked"`
	Plaintext string `json:"plaintext,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	ElapsedMs int    `json:"elapsed_ms"`
}

type hashCrackServiceRequest struct {
	Hash       string `json:"hash"`
	Format     string `json:"format"`
	Type       string `json:"type"`
	Mode       string `json:"mode"`
	MaxSeconds int    `json:"max_seconds"`
}

type hashCrackServiceResponse struct {
	Hash      string `json:"hash"`
	Type      string `json:"type"`
	Mode      string `json:"mode"`
	Engine    string `json:"engine"`
	Ruleset   string `json:"ruleset"`
	Cracked   bool   `json:"cracked"`
	Plaintext string `json:"plaintext,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	ElapsedMs int    `json:"elapsed_ms"`
}

type hashCrackFormat struct {
	Length int
	John   string
}

var hashCrackFormats = map[string]hashCrackFormat{
	"md5":    {Length: 32, John: "raw-md5"},
	"sha1":   {Length: 40, John: "raw-sha1"},
	"sha256": {Length: 64, John: "raw-sha256"},
	"ntlm":   {Length: 32, John: "nt"},
}

var hashCrackHexRegex = regexp.MustCompile(`\A[0-9a-fA-F]+\z`)

var hashCrackCache = struct {
	mu    sync.RWMutex
	items map[string]HashCrackResponse
}{
	items: make(map[string]HashCrackResponse),
}

func (h *Handler) HashCrack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req HashCrackRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"hash\": \"...\", \"type\": \"md5\", \"mode\": \"fasttrack\"}"})
		return
	}

	req.Hash = strings.ToLower(strings.TrimSpace(req.Hash))
	req.Type = normalizeHashCrackType(req.Type)
	req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
	if req.Mode == "" {
		req.Mode = "fasttrack"
	}
	if req.Mode != "fasttrack" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported mode — use fasttrack"})
		return
	}
	if err := validateHashCrackInput(req.Hash, req.Type); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h.executeHandler(w, r, "/api/hash-crack", h.Cfg.HashCrackCostSats, func() (interface{}, error) {
		return h.doHashCrack(req.Hash, req.Type, req.Mode)
	})
}

func normalizeHashCrackType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "md5":
		return "md5"
	case "sha1", "sha-1":
		return "sha1"
	case "sha256", "sha-256":
		return "sha256"
	case "nt", "ntlm":
		return "ntlm"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func validateHashCrackInput(hash, hashType string) error {
	if hash == "" {
		return fmt.Errorf("hash is required")
	}
	format, ok := hashCrackFormats[hashType]
	if !ok {
		return fmt.Errorf("unsupported hash type — use md5, sha1, sha256, or ntlm")
	}
	if !hashCrackHexRegex.MatchString(hash) {
		return fmt.Errorf("hash must be hexadecimal")
	}
	if len(hash) != format.Length {
		return fmt.Errorf("invalid %s hash length", hashType)
	}
	return nil
}

func (h *Handler) doHashCrack(hash, hashType, mode string) (*HashCrackResponse, error) {
	cacheKey := strings.Join([]string{hashType, mode, hash}, "|")
	if cached := getCachedHashCrack(cacheKey); cached != nil {
		return cached, nil
	}
	if cached, err := h.DB.GetHashCrackCache(hashType, mode, hash); err == nil && cached != nil {
		result := &HashCrackResponse{
			Hash:      cached.Hash,
			Type:      cached.Type,
			Mode:      cached.Mode,
			Engine:    cached.Engine,
			Ruleset:   cached.Ruleset,
			Cracked:   cached.Cracked,
			Plaintext: cached.Plaintext,
			TimedOut:  cached.TimedOut,
			ElapsedMs: cached.ElapsedMs,
		}
		setCachedHashCrack(cacheKey, result)
		return result, nil
	}

	format := hashCrackFormats[hashType]
	payload := hashCrackServiceRequest{
		Hash:       hash,
		Type:       hashType,
		Format:     format.John,
		Mode:       mode,
		MaxSeconds: h.Cfg.HashCrackMaxSeconds,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal hash crack request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, h.Cfg.HashCrackServiceURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build hash crack request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(h.Cfg.HashCrackServiceToken); token != "" {
		httpReq.Header.Set("X-Hash-Crack-Token", token)
	}

	client := &http.Client{Timeout: time.Duration(h.Cfg.HashCrackMaxSeconds+5) * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("hash crack backend request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read hash crack response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(bodyBytes))
		if message == "" {
			message = fmt.Sprintf("hash crack backend returned status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s", message)
	}

	var parsed hashCrackServiceResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, fmt.Errorf("decode hash crack response: %w", err)
	}

	result := &HashCrackResponse{
		Hash:      parsed.Hash,
		Type:      parsed.Type,
		Mode:      parsed.Mode,
		Engine:    parsed.Engine,
		Ruleset:   parsed.Ruleset,
		Cracked:   parsed.Cracked,
		Plaintext: parsed.Plaintext,
		TimedOut:  parsed.TimedOut,
		ElapsedMs: parsed.ElapsedMs,
	}
	setCachedHashCrack(cacheKey, result)
	_ = h.DB.PutHashCrackCache(database.HashCrackCacheEntry{
		Hash:      result.Hash,
		Type:      result.Type,
		Mode:      result.Mode,
		Engine:    result.Engine,
		Ruleset:   result.Ruleset,
		Cracked:   result.Cracked,
		Plaintext: result.Plaintext,
		TimedOut:  result.TimedOut,
		ElapsedMs: result.ElapsedMs,
	})
	return result, nil
}

func getCachedHashCrack(key string) *HashCrackResponse {
	hashCrackCache.mu.RLock()
	defer hashCrackCache.mu.RUnlock()
	entry, ok := hashCrackCache.items[key]
	if !ok {
		return nil
	}
	cloned := entry
	return &cloned
}

func setCachedHashCrack(key string, value *HashCrackResponse) {
	if value == nil {
		return
	}
	hashCrackCache.mu.Lock()
	hashCrackCache.items[key] = *value
	hashCrackCache.mu.Unlock()
}

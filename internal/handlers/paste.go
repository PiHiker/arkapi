package handlers

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PiHiker/arkapi/internal/database"
	"github.com/PiHiker/arkapi/internal/middleware"
)

type PasteCreateRequest struct {
	Content    *string         `json:"content,omitempty"`
	JSON       json.RawMessage `json:"json,omitempty"`
	TTLSeconds int             `json:"ttl_seconds,omitempty"`
}

type PasteCreateResponse struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	ContentKind string `json:"content_kind"`
	SizeBytes   int    `json:"size_bytes"`
	TTLSeconds  int    `json:"ttl_seconds"`
	ExpiresAt   string `json:"expires_at"`
}

type PasteReadResponse struct {
	ID          string `json:"id"`
	ContentKind string `json:"content_kind"`
	Content     string `json:"content"`
	SizeBytes   int    `json:"size_bytes"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at"`
}

const minPasteTTLSeconds = 60

func (h *Handler) PasteCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req PasteCreateRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"content\":\"notes\",\"ttl_seconds\":3600} or {\"json\":{\"step\":\"extract\"}}"})
		return
	}

	contentKind, content, ttlSeconds, err := h.preparePasteRequest(req)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	token := middleware.GetToken(r)
	h.executeHandler(w, r, "/api/paste", h.Cfg.PasteCostSats, func() (interface{}, error) {
		h.DB.DeleteExpiredPastes()

		pasteID, err := generatePasteID()
		if err != nil {
			return nil, fmt.Errorf("failed to create paste id: %w", err)
		}

		expiresAt := time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second)
		if err := h.DB.CreatePaste(database.PasteEntry{
			ID:           pasteID,
			SessionToken: token,
			ContentKind:  contentKind,
			Content:      content,
			SizeBytes:    len(content),
			ExpiresAt:    expiresAt,
		}); err != nil {
			return nil, err
		}

		baseURL := strings.TrimRight(h.Cfg.PublicBaseURL, "/")
		return PasteCreateResponse{
			ID:          pasteID,
			URL:         baseURL + "/v1/p/" + pasteID,
			ContentKind: contentKind,
			SizeBytes:   len(content),
			TTLSeconds:  ttlSeconds,
			ExpiresAt:   expiresAt.Format(time.RFC3339),
		}, nil
	})
}

func (h *Handler) PasteGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET"})
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/v1/p/")
	id = strings.TrimSpace(id)
	if !isValidPasteID(id) {
		sendPasteJSON(w, http.StatusNotFound, map[string]string{"error": "paste not found"})
		return
	}

	h.DB.DeleteExpiredPastes()
	paste, err := h.DB.GetPaste(id)
	if err != nil {
		sendPasteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if paste == nil {
		sendPasteJSON(w, http.StatusNotFound, map[string]string{"error": "paste not found"})
		return
	}

	sendPasteJSON(w, http.StatusOK, PasteReadResponse{
		ID:          paste.ID,
		ContentKind: paste.ContentKind,
		Content:     paste.Content,
		SizeBytes:   paste.SizeBytes,
		CreatedAt:   paste.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:   paste.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) preparePasteRequest(req PasteCreateRequest) (string, string, int, error) {
	hasContent := req.Content != nil
	jsonPayload := bytes.TrimSpace(req.JSON)
	hasJSON := len(jsonPayload) > 0 && string(jsonPayload) != "null"

	if hasContent == hasJSON {
		return "", "", 0, fmt.Errorf("send exactly one of content or json")
	}

	ttlSeconds := req.TTLSeconds
	if ttlSeconds == 0 {
		ttlSeconds = h.Cfg.PasteDefaultTTLSeconds
	}
	if ttlSeconds < minPasteTTLSeconds {
		return "", "", 0, fmt.Errorf("ttl_seconds must be at least %d", minPasteTTLSeconds)
	}
	if ttlSeconds > h.Cfg.PasteMaxTTLSeconds {
		return "", "", 0, fmt.Errorf("ttl_seconds exceeds maximum of %d", h.Cfg.PasteMaxTTLSeconds)
	}

	if hasContent {
		content := strings.TrimSpace(*req.Content)
		if content == "" {
			return "", "", 0, fmt.Errorf("content is required")
		}
		if !utf8.ValidString(content) {
			return "", "", 0, fmt.Errorf("content must be valid UTF-8 text")
		}
		if strings.ContainsRune(content, '\x00') {
			return "", "", 0, fmt.Errorf("content may not contain NUL bytes")
		}
		if len(content) > h.Cfg.PasteMaxBytes {
			return "", "", 0, fmt.Errorf("content exceeds maximum of %d bytes", h.Cfg.PasteMaxBytes)
		}
		if looksLikeExecutablePaste(content) {
			return "", "", 0, fmt.Errorf("content looks like executable or web-shell material and is not allowed")
		}
		return "text", content, ttlSeconds, nil
	}

	if !json.Valid(jsonPayload) {
		return "", "", 0, fmt.Errorf("json must be valid JSON")
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, jsonPayload); err != nil {
		return "", "", 0, fmt.Errorf("json must be valid JSON")
	}
	content := compact.String()
	if content == "" {
		return "", "", 0, fmt.Errorf("json is required")
	}
	if len(content) > h.Cfg.PasteMaxBytes {
		return "", "", 0, fmt.Errorf("json exceeds maximum of %d bytes", h.Cfg.PasteMaxBytes)
	}

	return "json", content, ttlSeconds, nil
}

func generatePasteID() (string, error) {
	buf := make([]byte, 7)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)), nil
}

func looksLikeExecutablePaste(content string) bool {
	normalized := strings.ToLower(strings.TrimSpace(content))
	suspiciousFragments := []string{
		"#!",
		"<?php",
		"<script",
		"<%",
		"shell_exec(",
		"system($_",
		"exec($_",
		"passthru($_",
	}
	for _, fragment := range suspiciousFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func isValidPasteID(id string) bool {
	if len(id) != 12 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
			return false
		}
	}
	return true
}

func sendPasteJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, noarchive, nosnippet")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}

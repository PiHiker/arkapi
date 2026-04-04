package handlers

import (
	"bytes"
	"crypto/rand"
	"database/sql"
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
	Content       *string         `json:"content,omitempty"`
	JSON          json.RawMessage `json:"json,omitempty"`
	TTLSeconds    int             `json:"ttl_seconds,omitempty"`
	BurnAfterRead bool            `json:"burn_after_read,omitempty"`
	MaxViews      *int            `json:"max_views,omitempty"`
}

type PasteCreateResponse struct {
	ID             string `json:"id"`
	URL            string `json:"url"`
	ContentKind    string `json:"content_kind"`
	SizeBytes      int    `json:"size_bytes"`
	TTLSeconds     int    `json:"ttl_seconds"`
	BurnAfterRead  bool   `json:"burn_after_read"`
	MaxViews       *int   `json:"max_views,omitempty"`
	ViewsRemaining *int   `json:"views_remaining,omitempty"`
	ExpiresAt      string `json:"expires_at"`
}

type PasteReadResponse struct {
	ID             string `json:"id"`
	ContentKind    string `json:"content_kind"`
	Content        string `json:"content"`
	SizeBytes      int    `json:"size_bytes"`
	BurnAfterRead  bool   `json:"burn_after_read"`
	MaxViews       *int   `json:"max_views,omitempty"`
	ViewCount      int    `json:"view_count"`
	ViewsRemaining *int   `json:"views_remaining,omitempty"`
	CreatedAt      string `json:"created_at"`
	ExpiresAt      string `json:"expires_at"`
}

const minPasteTTLSeconds = 60
const maxActivePastesPerSession = 100
const maxPasteViews = 100

type preparedPasteRequest struct {
	ContentKind   string
	Content       string
	TTLSeconds    int
	BurnAfterRead bool
	MaxViews      *int
}

func (h *Handler) PasteCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req PasteCreateRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"content\":\"Lorem ipsum dolor sit amet.\",\"ttl_seconds\":3600} or {\"json\":{\"title\":\"Example note\",\"body\":\"Lorem ipsum dolor sit amet.\"}}"})
		return
	}

	prepared, err := h.preparePasteRequest(req)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	token := middleware.GetToken(r)
	h.DB.DeleteExpiredPastes()
	activeCount, err := h.DB.CountActivePastesForSession(token)
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check active pastes"})
		return
	}
	if activeCount >= maxActivePastesPerSession {
		sendJSON(w, http.StatusTooManyRequests, map[string]string{"error": "active paste limit reached for this session"})
		return
	}

	h.executeHandler(w, r, "/api/paste", h.Cfg.PasteCostSats, func() (interface{}, error) {
		pasteID, err := generatePasteID()
		if err != nil {
			return nil, fmt.Errorf("failed to create paste id: %w", err)
		}

		expiresAt := time.Now().UTC().Add(time.Duration(prepared.TTLSeconds) * time.Second)
		if err := h.DB.CreatePaste(database.PasteEntry{
			ID:            pasteID,
			SessionToken:  token,
			ContentKind:   prepared.ContentKind,
			Content:       prepared.Content,
			SizeBytes:     len(prepared.Content),
			BurnAfterRead: prepared.BurnAfterRead,
			MaxViews:      nullableInt(prepared.MaxViews),
			ViewCount:     0,
			ExpiresAt:     expiresAt,
		}); err != nil {
			return nil, err
		}

		baseURL := strings.TrimRight(h.Cfg.PublicBaseURL, "/")
		return PasteCreateResponse{
			ID:             pasteID,
			URL:            baseURL + "/v1/p/" + pasteID,
			ContentKind:    prepared.ContentKind,
			SizeBytes:      len(prepared.Content),
			TTLSeconds:     prepared.TTLSeconds,
			BurnAfterRead:  prepared.BurnAfterRead,
			MaxViews:       prepared.MaxViews,
			ViewsRemaining: copyIntPtr(prepared.MaxViews),
			ExpiresAt:      expiresAt.Format(time.RFC3339),
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
	paste, err := h.DB.ConsumePaste(id)
	if err != nil {
		sendPasteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if paste == nil {
		sendPasteJSON(w, http.StatusNotFound, map[string]string{"error": "paste not found"})
		return
	}

	sendPasteJSON(w, http.StatusOK, PasteReadResponse{
		ID:             paste.ID,
		ContentKind:    paste.ContentKind,
		Content:        paste.Content,
		SizeBytes:      paste.SizeBytes,
		BurnAfterRead:  paste.BurnAfterRead,
		MaxViews:       intPtrFromNull(paste.MaxViews),
		ViewCount:      paste.ViewCount,
		ViewsRemaining: viewsRemaining(paste.ViewCount, paste.MaxViews),
		CreatedAt:      paste.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:      paste.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) preparePasteRequest(req PasteCreateRequest) (*preparedPasteRequest, error) {
	hasContent := req.Content != nil
	jsonPayload := bytes.TrimSpace(req.JSON)
	hasJSON := len(jsonPayload) > 0 && string(jsonPayload) != "null"

	if hasContent == hasJSON {
		return nil, fmt.Errorf("send exactly one of content or json")
	}

	ttlSeconds := req.TTLSeconds
	if ttlSeconds == 0 {
		ttlSeconds = h.Cfg.PasteDefaultTTLSeconds
	}
	if ttlSeconds < minPasteTTLSeconds {
		return nil, fmt.Errorf("ttl_seconds must be at least %d", minPasteTTLSeconds)
	}
	if ttlSeconds > h.Cfg.PasteMaxTTLSeconds {
		return nil, fmt.Errorf("ttl_seconds exceeds maximum of %d", h.Cfg.PasteMaxTTLSeconds)
	}

	maxViews, err := normalizePasteMaxViews(req.BurnAfterRead, req.MaxViews)
	if err != nil {
		return nil, err
	}

	if hasContent {
		content := strings.TrimSpace(*req.Content)
		if content == "" {
			return nil, fmt.Errorf("content is required")
		}
		if !utf8.ValidString(content) {
			return nil, fmt.Errorf("content must be valid UTF-8 text")
		}
		if strings.ContainsRune(content, '\x00') {
			return nil, fmt.Errorf("content may not contain NUL bytes")
		}
		if len(content) > h.Cfg.PasteMaxBytes {
			return nil, fmt.Errorf("content exceeds maximum of %d bytes", h.Cfg.PasteMaxBytes)
		}
		if looksLikeExecutablePaste(content) {
			return nil, fmt.Errorf("content looks like executable or web-shell material and is not allowed")
		}
		return &preparedPasteRequest{
			ContentKind:   "text",
			Content:       content,
			TTLSeconds:    ttlSeconds,
			BurnAfterRead: req.BurnAfterRead,
			MaxViews:      maxViews,
		}, nil
	}

	if !json.Valid(jsonPayload) {
		return nil, fmt.Errorf("json must be valid JSON")
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, jsonPayload); err != nil {
		return nil, fmt.Errorf("json must be valid JSON")
	}
	content := compact.String()
	if content == "" {
		return nil, fmt.Errorf("json is required")
	}
	if len(content) > h.Cfg.PasteMaxBytes {
		return nil, fmt.Errorf("json exceeds maximum of %d bytes", h.Cfg.PasteMaxBytes)
	}

	return &preparedPasteRequest{
		ContentKind:   "json",
		Content:       content,
		TTLSeconds:    ttlSeconds,
		BurnAfterRead: req.BurnAfterRead,
		MaxViews:      maxViews,
	}, nil
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

func normalizePasteMaxViews(burnAfterRead bool, maxViews *int) (*int, error) {
	if burnAfterRead {
		if maxViews != nil && *maxViews != 1 {
			return nil, fmt.Errorf("burn_after_read may only be used with max_views set to 1")
		}
		one := 1
		return &one, nil
	}
	if maxViews == nil {
		return nil, nil
	}
	if *maxViews < 1 {
		return nil, fmt.Errorf("max_views must be at least 1")
	}
	if *maxViews > maxPasteViews {
		return nil, fmt.Errorf("max_views exceeds maximum of %d", maxPasteViews)
	}
	value := *maxViews
	return &value, nil
}

func nullableInt(value *int) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*value), Valid: true}
}

func intPtrFromNull(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	v := int(value.Int64)
	return &v
}

func copyIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}

func viewsRemaining(viewCount int, maxViews sql.NullInt64) *int {
	if !maxViews.Valid {
		return nil
	}
	remaining := int(maxViews.Int64) - viewCount
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
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

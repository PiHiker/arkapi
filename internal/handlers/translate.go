package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type TranslateRequest struct {
	Text           string `json:"text"`
	TargetLanguage string `json:"target_language,omitempty"`
	SourceLanguage string `json:"source_language,omitempty"`
}

type TranslateResponse struct {
	Text               string `json:"text"`
	TranslatedText     string `json:"translated_text"`
	TargetLanguage     string `json:"target_language"`
	SourceLanguage     string `json:"source_language"`
	DetectedLanguage   string `json:"detected_language,omitempty"`
	DetectedConfidence int    `json:"detected_confidence,omitempty"`
}

type libreTranslateRequest struct {
	Q      string `json:"q"`
	Source string `json:"source"`
	Target string `json:"target"`
	Format string `json:"format"`
}

type libreTranslateResponse struct {
	TranslatedText string `json:"translatedText"`
	DetectedLanguage struct {
		Language   string `json:"language"`
		Confidence float64 `json:"confidence"`
	} `json:"detectedLanguage"`
	Error string `json:"error"`
}

type translateCacheEntry struct {
	response  *TranslateResponse
	expiresAt time.Time
}

const translateCacheTTL = 24 * time.Hour

var (
	translateCache = struct {
		mu    sync.RWMutex
		items map[string]translateCacheEntry
	}{
		items: make(map[string]translateCacheEntry),
	}
	languageCodePattern = regexp.MustCompile(`^[a-z]{2,3}(?:-[a-z]{2,4})?$`)
)

func (h *Handler) Translate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req TranslateRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"text\": \"Bonjour\", \"target_language\": \"en\"}"})
		return
	}

	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}
	if len(req.Text) > 5000 {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "text too long (max 5000 characters)"})
		return
	}

	target := normalizeLanguageCode(req.TargetLanguage, "en")
	source := normalizeSourceLanguageCode(req.SourceLanguage)
	if !isValidLanguageCode(target) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid target_language"})
		return
	}
	if source != "auto" && !isValidLanguageCode(source) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid source_language"})
		return
	}

	h.executeHandler(w, r, "/api/translate", h.Cfg.TranslateCostSats, func() (interface{}, error) {
		return h.doTranslate(req.Text, source, target)
	})
}

func (h *Handler) doTranslate(text, source, target string) (*TranslateResponse, error) {
	cacheKey := source + "|" + target + "|" + strings.ToLower(strings.TrimSpace(text))
	if cached := getCachedTranslation(cacheKey); cached != nil {
		return cached, nil
	}

	payload := libreTranslateRequest{
		Q:      text,
		Source: source,
		Target: target,
		Format: "text",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal translate request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, h.Cfg.TranslateServiceURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build translate request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ArkAPI/1.0 (+https://arkapi.dev)")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("translation request failed: %w", err)
	}
	defer resp.Body.Close()

	var ltResp libreTranslateResponse
	if err := json.NewDecoder(resp.Body).Decode(&ltResp); err != nil {
		return nil, fmt.Errorf("decode translation response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if ltResp.Error != "" {
			return nil, fmt.Errorf("translation service returned %d: %s", resp.StatusCode, ltResp.Error)
		}
		return nil, fmt.Errorf("translation service returned %d", resp.StatusCode)
	}
	if strings.TrimSpace(ltResp.TranslatedText) == "" {
		return nil, fmt.Errorf("translation service returned empty text")
	}

	result := &TranslateResponse{
		Text:            text,
		TranslatedText:  ltResp.TranslatedText,
		TargetLanguage:  target,
		SourceLanguage:  source,
	}
	if source == "auto" && ltResp.DetectedLanguage.Language != "" {
		result.DetectedLanguage = ltResp.DetectedLanguage.Language
		result.DetectedConfidence = int(ltResp.DetectedLanguage.Confidence)
		result.SourceLanguage = ltResp.DetectedLanguage.Language
	}

	setCachedTranslation(cacheKey, result)
	return result, nil
}

func normalizeLanguageCode(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}

func normalizeSourceLanguageCode(value string) string {
	value = normalizeLanguageCode(value, "auto")
	if value == "" {
		return "auto"
	}
	return value
}

func isValidLanguageCode(value string) bool {
	return languageCodePattern.MatchString(value)
}

func getCachedTranslation(key string) *TranslateResponse {
	translateCache.mu.RLock()
	entry, ok := translateCache.items[key]
	translateCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	return &clone
}

func setCachedTranslation(key string, response *TranslateResponse) {
	clone := *response
	translateCache.mu.Lock()
	translateCache.items[key] = translateCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(translateCacheTTL),
	}
	translateCache.mu.Unlock()
}

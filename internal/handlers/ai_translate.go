package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	publicAITranslateModel       = "arkapi-translate-v1"
	aiTranslateCacheTTL          = 24 * time.Hour
	defaultAITranslateStyle      = "natural"
	defaultAITranslateSystemPrompt = "You are ArkAPI AI Translate. Translate the user's text into the requested target language and return strict JSON only. The JSON object must contain exactly these keys: translated_text and detected_language. translated_text must contain only the translated text with no commentary. detected_language must be the best short language code for the source text, such as en, fr, es, or de. Do not wrap the JSON in markdown fences. Do not add explanations."
)

type AITranslateRequest struct {
	Text           string `json:"text"`
	TargetLanguage string `json:"target_language,omitempty"`
	SourceLanguage string `json:"source_language,omitempty"`
	Style          string `json:"style,omitempty"`
}

type AITranslateResponse struct {
	Text             string       `json:"text"`
	TranslatedText   string       `json:"translated_text"`
	TargetLanguage   string       `json:"target_language"`
	SourceLanguage   string       `json:"source_language"`
	DetectedLanguage string       `json:"detected_language,omitempty"`
	Style            string       `json:"style"`
	Model            string       `json:"model"`
	Usage            *AIChatUsage `json:"usage,omitempty"`
}

type aiTranslateModelResponse struct {
	TranslatedText   string `json:"translated_text"`
	DetectedLanguage string `json:"detected_language"`
}

type aiTranslateCacheEntry struct {
	response  *AITranslateResponse
	expiresAt time.Time
}

var aiTranslateCache = struct {
	mu    sync.RWMutex
	items map[string]aiTranslateCacheEntry
}{
	items: make(map[string]aiTranslateCacheEntry),
}

func (h *Handler) AITranslate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req AITranslateRequest
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

	style := normalizeAITranslateStyle(req.Style)
	if !isValidAITranslateStyle(style) {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid style"})
		return
	}

	h.executeHandler(w, r, "/api/ai-translate", h.Cfg.AITranslateCostSats, func() (interface{}, error) {
		return h.doAITranslate(req.Text, source, target, style)
	})
}

func (h *Handler) doAITranslate(text, source, target, style string) (*AITranslateResponse, error) {
	if h.Cfg.CloudflareAIAccountID == "" || h.Cfg.CloudflareAIToken == "" || h.Cfg.CloudflareAIModel == "" {
		return nil, fmt.Errorf("ai translate backend is not configured")
	}

	cacheKey := strings.Join([]string{source, target, style, strings.ToLower(strings.TrimSpace(text))}, "|")
	if cached := getCachedAITranslate(cacheKey); cached != nil {
		return cached, nil
	}

	userPayload := fmt.Sprintf("Translate the following text.\nsource_language=%s\ntarget_language=%s\nstyle=%s\ntext=%s", source, target, style, text)
	fullMessages := []AIChatMessage{
		{Role: "system", Content: defaultAITranslateSystemPrompt},
		{Role: "user", Content: userPayload},
	}

	payload := cloudflareAIRequest{Messages: fullMessages}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal ai translate request: %w", err)
	}

	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/ai/run/%s", h.Cfg.CloudflareAIAccountID, h.Cfg.CloudflareAIModel)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build ai translate request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+h.Cfg.CloudflareAIToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "ArkAPI/1.0 (+https://arkapi.dev)")

	client := &http.Client{Timeout: time.Duration(h.Cfg.CloudflareAITimeoutSeconds) * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai translate backend request failed: %w", err)
	}
	defer resp.Body.Close()

	var cfResp cloudflareAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfResp); err != nil {
		return nil, fmt.Errorf("decode ai translate response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !cfResp.Success {
		if len(cfResp.Errors) > 0 {
			return nil, fmt.Errorf("ai translate backend error %d: %s", cfResp.Errors[0].Code, cfResp.Errors[0].Message)
		}
		return nil, fmt.Errorf("ai translate backend returned status %d", resp.StatusCode)
	}

	modelText := strings.TrimSpace(cfResp.Result.Response)
	if modelText == "" {
		return nil, fmt.Errorf("ai translate backend returned empty response")
	}

	parsed := aiTranslateModelResponse{}
	clean := stripJSONCodeFences(modelText)
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		parsed.TranslatedText = clean
	}

	parsed.TranslatedText = strings.TrimSpace(parsed.TranslatedText)
	if parsed.TranslatedText == "" {
		return nil, fmt.Errorf("ai translate backend returned empty translation")
	}

	result := &AITranslateResponse{
		Text:             text,
		TranslatedText:   parsed.TranslatedText,
		TargetLanguage:   target,
		SourceLanguage:   source,
		DetectedLanguage: normalizeLanguageCode(parsed.DetectedLanguage, ""),
		Style:            style,
		Model:            publicAITranslateModel,
		Usage: &AIChatUsage{
			PromptTokens:     cfResp.Result.Usage.PromptTokens,
			CompletionTokens: cfResp.Result.Usage.CompletionTokens,
			TotalTokens:      cfResp.Result.Usage.TotalTokens,
		},
	}
	if source == "auto" && result.DetectedLanguage != "" {
		result.SourceLanguage = result.DetectedLanguage
	}

	setCachedAITranslate(cacheKey, result)
	return result, nil
}

func normalizeAITranslateStyle(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultAITranslateStyle
	}
	return value
}

func isValidAITranslateStyle(value string) bool {
	switch value {
	case "literal", "natural", "polished":
		return true
	default:
		return false
	}
}

func stripJSONCodeFences(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "```") {
		value = strings.TrimPrefix(value, "```json")
		value = strings.TrimPrefix(value, "```")
		value = strings.TrimSuffix(value, "```")
	}
	return strings.TrimSpace(value)
}

func getCachedAITranslate(key string) *AITranslateResponse {
	aiTranslateCache.mu.RLock()
	entry, ok := aiTranslateCache.items[key]
	aiTranslateCache.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) || entry.response == nil {
		return nil
	}
	clone := *entry.response
	if entry.response.Usage != nil {
		usageClone := *entry.response.Usage
		clone.Usage = &usageClone
	}
	return &clone
}

func setCachedAITranslate(key string, response *AITranslateResponse) {
	clone := *response
	if response.Usage != nil {
		usageClone := *response.Usage
		clone.Usage = &usageClone
	}
	aiTranslateCache.mu.Lock()
	aiTranslateCache.items[key] = aiTranslateCacheEntry{
		response:  &clone,
		expiresAt: time.Now().Add(aiTranslateCacheTTL),
	}
	aiTranslateCache.mu.Unlock()
}

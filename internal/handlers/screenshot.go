package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PiHiker/arkapi/internal/config"
)

type ScreenshotRequest struct {
	URL string `json:"url"`
}

type ScreenshotResponse struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	MimeType    string `json:"mime_type"`
	DownloadURL string `json:"download_url"`
	ExpiresAt   string `json:"expires_at"`
}

func (h *Handler) Screenshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req ScreenshotRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"url\": \"https://example.com\"}"})
		return
	}

	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		req.URL = "https://" + req.URL
	}
	if _, err := validateSafeURL(req.URL); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h.executeHandler(w, r, "/api/screenshot", h.Cfg.ScreenshotCostSats, func() (interface{}, error) {
		return doScreenshot(h.Cfg, req.URL)
	})
}

func doScreenshot(cfg config.Config, targetURL string) (*ScreenshotResponse, error) {
	payload, err := json.Marshal(map[string]string{"url": targetURL})
	if err != nil {
		return nil, fmt.Errorf("failed to encode screenshot request: %w", err)
	}

	timeout := time.Duration(cfg.ScreenshotMaxWaitSeconds) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodPost, cfg.ScreenshotServiceURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to build screenshot request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Screenshot-Token", cfg.ScreenshotServiceToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("screenshot request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("screenshot service returned %d: %s", resp.StatusCode, msg)
	}

	imageBytes, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read screenshot image: %w", err)
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/png"
	}
	finalURL := strings.TrimSpace(resp.Header.Get("X-Final-Url"))
	if finalURL == "" {
		finalURL = targetURL
	}

	downloadID, err := storeGeneratedImage(imageBytes, mimeType, time.Duration(cfg.ImageDownloadTTLSeconds)*time.Second)
	if err != nil {
		return nil, err
	}

	expiresAt := time.Now().Add(time.Duration(cfg.ImageDownloadTTLSeconds) * time.Second).UTC()
	baseURL := strings.TrimRight(cfg.PublicBaseURL, "/")
	if baseURL == "" {
		baseURL = "https://arkapi.dev"
	}

	return &ScreenshotResponse{
		URL:         targetURL,
		FinalURL:    finalURL,
		Width:       1440,
		Height:      900,
		MimeType:    mimeType,
		DownloadURL: baseURL + "/v1/downloads/" + downloadID,
		ExpiresAt:   expiresAt.Format(time.RFC3339),
	}, nil
}

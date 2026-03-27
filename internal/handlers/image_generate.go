package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PiHiker/arkapi/internal/config"
)

type ImageGenerateRequest struct {
	Prompt string `json:"prompt"`
}

type ImageGenerateResponse struct {
	Prompt      string `json:"prompt"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Steps       int    `json:"steps"`
	Seed        int64  `json:"seed"`
	MimeType    string `json:"mime_type"`
	DownloadURL string `json:"download_url"`
	ExpiresAt   string `json:"expires_at"`
}

type imageDownload struct {
	Content    []byte
	MimeType   string
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

const maxImageDownloads = 100 // cap in-memory image store to prevent OOM

var (
	imageDownloadsMu sync.Mutex
	imageDownloads   = map[string]imageDownload{}
)

func (h *Handler) ImageGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req ImageGenerateRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"prompt\": \"cinematic AI control room\"}"})
		return
	}

	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is required"})
		return
	}
	if len(req.Prompt) > 1000 {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is too long"})
		return
	}
	req = ImageGenerateRequest{Prompt: req.Prompt}

	h.executeHandler(w, r, "/api/image-generate", h.Cfg.ComfyImageCostSats, func() (interface{}, error) {
		return doImageGenerate(h.Cfg, req)
	})
}

func doImageGenerate(cfg config.Config, req ImageGenerateRequest) (*ImageGenerateResponse, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	clientID := fmt.Sprintf("arkapi-image-%d", time.Now().UnixNano())
	filenamePrefix := fmt.Sprintf("arkapi-%d", time.Now().UnixNano())
	seed := time.Now().UnixNano()

	payload := map[string]interface{}{
		"client_id": clientID,
		"prompt": map[string]interface{}{
			"4": map[string]interface{}{
				"class_type": "CheckpointLoaderSimple",
				"inputs": map[string]interface{}{
					"ckpt_name": cfg.ComfyModelName,
				},
			},
			"6": map[string]interface{}{
				"class_type": "CLIPTextEncode",
				"inputs": map[string]interface{}{
					"text": req.Prompt,
					"clip": []interface{}{"4", 1},
				},
			},
			"7": map[string]interface{}{
				"class_type": "CLIPTextEncode",
				"inputs": map[string]interface{}{
					"text": "text, letters, words, logo, watermark, signature, caption, label, ui, screenshot, lowres, blurry, distorted, deformed, bad anatomy, extra limbs, cropped, out of frame, duplicate, noisy, jpeg artifacts",
					"clip": []interface{}{"4", 1},
				},
			},
			"5": map[string]interface{}{
				"class_type": "EmptyLatentImage",
				"inputs": map[string]interface{}{
					"width":      512,
					"height":     512,
					"batch_size": 1,
				},
			},
			"3": map[string]interface{}{
				"class_type": "KSampler",
				"inputs": map[string]interface{}{
					"model":        []interface{}{"4", 0},
					"seed":         seed,
					"steps":        12,
					"cfg":          6.5,
					"sampler_name": "euler",
					"scheduler":    "karras",
					"positive":     []interface{}{"6", 0},
					"negative":     []interface{}{"7", 0},
					"latent_image": []interface{}{"5", 0},
					"denoise":      1.0,
				},
			},
			"8": map[string]interface{}{
				"class_type": "VAEDecode",
				"inputs": map[string]interface{}{
					"samples": []interface{}{"3", 0},
					"vae":     []interface{}{"4", 2},
				},
			},
			"9": map[string]interface{}{
				"class_type": "SaveImage",
				"inputs": map[string]interface{}{
					"images":          []interface{}{"8", 0},
					"filename_prefix": filenamePrefix,
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode comfy request: %w", err)
	}

	promptResp, err := client.Post(strings.TrimRight(cfg.ComfyBaseURL, "/")+"/prompt", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("comfy request failed: %w", err)
	}
	defer promptResp.Body.Close()
	if promptResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(promptResp.Body, 4096))
		return nil, fmt.Errorf("comfy returned status %d: %s", promptResp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var queued struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.NewDecoder(promptResp.Body).Decode(&queued); err != nil {
		return nil, fmt.Errorf("failed to parse comfy response: %w", err)
	}
	if queued.PromptID == "" {
		return nil, fmt.Errorf("comfy did not return a prompt_id")
	}

	type comfyHistory struct {
		Outputs map[string]struct {
			Images []struct {
				Filename  string `json:"filename"`
				Subfolder string `json:"subfolder"`
				Type      string `json:"type"`
			} `json:"images"`
		} `json:"outputs"`
	}

	var imageRef struct {
		Filename  string
		Subfolder string
		Type      string
	}

	deadline := time.Now().Add(time.Duration(cfg.ComfyMaxWaitSeconds) * time.Second)
	historyURL := strings.TrimRight(cfg.ComfyBaseURL, "/") + "/history/" + url.PathEscape(queued.PromptID)

	for time.Now().Before(deadline) {
		resp, err := client.Get(historyURL)
		if err == nil {
			var hist map[string]comfyHistory
			if err := json.NewDecoder(resp.Body).Decode(&hist); err == nil {
				if item, ok := hist[queued.PromptID]; ok {
					for _, output := range item.Outputs {
						if len(output.Images) > 0 {
							imageRef.Filename = output.Images[0].Filename
							imageRef.Subfolder = output.Images[0].Subfolder
							imageRef.Type = output.Images[0].Type
							break
						}
					}
				}
			}
			resp.Body.Close()
		}
		if imageRef.Filename != "" {
			break
		}
		time.Sleep(1 * time.Second)
	}

	if imageRef.Filename == "" {
		return nil, fmt.Errorf("timed out waiting for comfy image output")
	}

	viewURL := strings.TrimRight(cfg.ComfyBaseURL, "/") + "/view?filename=" + url.QueryEscape(imageRef.Filename) +
		"&subfolder=" + url.QueryEscape(imageRef.Subfolder) +
		"&type=" + url.QueryEscape(imageRef.Type)
	imageResp, err := client.Get(viewURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download comfy image: %w", err)
	}
	defer imageResp.Body.Close()
	if imageResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("comfy image download returned status %d", imageResp.StatusCode)
	}
	imageBytes, err := io.ReadAll(io.LimitReader(imageResp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read comfy image: %w", err)
	}
	mimeType := imageResp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/png"
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

	return &ImageGenerateResponse{
		Prompt:      req.Prompt,
		Width:       512,
		Height:      512,
		Steps:       12,
		Seed:        seed,
		MimeType:    mimeType,
		DownloadURL: baseURL + "/v1/downloads/" + downloadID,
		ExpiresAt:   expiresAt.Format(time.RFC3339),
	}, nil
}

func (h *Handler) DownloadImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/v1/downloads/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}

	item, ok := getGeneratedImage(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", item.MimeType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(item.Content)))
	w.Header().Set("Cache-Control", "private, max-age=0, no-store")
	w.Header().Set("Content-Disposition", "inline; filename=\"arkapi-image.png\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(item.Content)
}

func storeGeneratedImage(content []byte, mimeType string, ttl time.Duration) (string, error) {
	id, err := randomHex(16)
	if err != nil {
		return "", fmt.Errorf("failed to create download id: %w", err)
	}
	now := time.Now().UTC()
	imageDownloadsMu.Lock()
	// Evict expired entries first
	for k, v := range imageDownloads {
		if now.After(v.ExpiresAt) {
			delete(imageDownloads, k)
		}
	}
	// If still at capacity, evict the oldest entry
	for len(imageDownloads) >= maxImageDownloads {
		var oldestID string
		var oldestTime time.Time
		for k, v := range imageDownloads {
			if oldestID == "" || v.CreatedAt.Before(oldestTime) {
				oldestID = k
				oldestTime = v.CreatedAt
			}
		}
		if oldestID != "" {
			delete(imageDownloads, oldestID)
		}
	}
	imageDownloads[id] = imageDownload{
		Content:   append([]byte(nil), content...),
		MimeType:  mimeType,
		ExpiresAt: now.Add(ttl),
		CreatedAt: now,
	}
	imageDownloadsMu.Unlock()
	return id, nil
}

func getGeneratedImage(id string) (imageDownload, bool) {
	imageDownloadsMu.Lock()
	defer imageDownloadsMu.Unlock()
	item, ok := imageDownloads[id]
	if !ok {
		return imageDownload{}, false
	}
	if time.Now().After(item.ExpiresAt) {
		delete(imageDownloads, id)
		return imageDownload{}, false
	}
	return item, true
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func init() {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			imageDownloadsMu.Lock()
			for id, item := range imageDownloads {
				if now.After(item.ExpiresAt) {
					delete(imageDownloads, id)
				}
			}
			imageDownloadsMu.Unlock()
		}
	}()
}

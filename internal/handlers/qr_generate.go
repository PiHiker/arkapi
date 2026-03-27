package handlers

import (
	"encoding/base64"
	"fmt"
	"net/http"

	qrcode "github.com/skip2/go-qrcode"
)

// QRRequest is what the consumer sends
type QRRequest struct {
	Data string `json:"data"`          // The text/URL to encode
	Size int    `json:"size,omitempty"` // Image size in pixels (default 256, max 1024)
}

// QRResponse is what we return
type QRResponse struct {
	Data     string `json:"data"`      // The input data echoed back
	Format   string `json:"format"`    // "png"
	Size     int    `json:"size"`      // Actual size used
	ImageB64 string `json:"image_b64"` // Base64-encoded PNG image
	DataURI  string `json:"data_uri"`  // Ready-to-use data URI for <img src="">
}

// QRGenerate handles /api/qr-generate
// Cost: 2 sats
func (h *Handler) QRGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req QRRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"data\": \"https://example.com\"}"})
		return
	}

	if req.Data == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "data is required"})
		return
	}

	// QR codes can encode up to ~4296 alphanumeric chars; cap at 2048 to be safe
	if len(req.Data) > 2048 {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "data too long (max 2048 characters)"})
		return
	}

	// Default and clamp size
	size := req.Size
	if size <= 0 {
		size = 256
	}
	if size < 64 {
		size = 64
	}
	if size > 1024 {
		size = 1024
	}

	h.executeHandler(w, r, "/api/qr-generate", h.Cfg.QRGenerateCostSats, func() (interface{}, error) {
		return doQRGenerate(req.Data, size)
	})
}

func doQRGenerate(data string, size int) (*QRResponse, error) {
	// Generate PNG QR code
	png, err := qrcode.Encode(data, qrcode.Medium, size)
	if err != nil {
		return nil, fmt.Errorf("qr encode: %w", err)
	}

	b64 := base64.StdEncoding.EncodeToString(png)

	return &QRResponse{
		Data:     data,
		Format:   "png",
		Size:     size,
		ImageB64: b64,
		DataURI:  "data:image/png;base64," + b64,
	}, nil
}

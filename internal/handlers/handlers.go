// Package handlers contains all API endpoint handlers.
// Each handler follows the same pattern:
//   1. Parse the request
//   2. Do the work (run a command, call an API, etc.)
//   3. Deduct sats
//   4. Return JSON response
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/PiHiker/arkapi/internal/bark"
	"github.com/PiHiker/arkapi/internal/config"
	"github.com/PiHiker/arkapi/internal/database"
	"github.com/PiHiker/arkapi/internal/middleware"
)

// Handler wraps the database connection so all handlers can use it
type Handler struct {
	DB   *database.DB
	Cfg  config.Config
	Bark *bark.Client
	Geo  *GeoReaders
}

// New creates a new Handler with database access and optional bark client
func New(db *database.DB, cfg config.Config, barkClient *bark.Client, geo *GeoReaders) *Handler {
	return &Handler{DB: db, Cfg: cfg, Bark: barkClient, Geo: geo}
}

// APIResponse is the standard response wrapper for all endpoints
type APIResponse struct {
	Success          bool        `json:"success"`
	Data             interface{} `json:"data,omitempty"`
	Error            string      `json:"error,omitempty"`
	CostSats         int         `json:"cost_sats"`
	BalanceRemaining int64       `json:"balance_remaining"`
	ResponseMs       int         `json:"response_ms"`
	Endpoint         string      `json:"endpoint"`
}

// executeHandler is the shared logic for all paid API endpoints.
// It deducts sats first (preventing free work via concurrent low-balance
// requests), runs the work, and refunds on failure.
func (h *Handler) executeHandler(w http.ResponseWriter, r *http.Request, endpoint string, costSats int, workFn func() (interface{}, error)) {
	start := time.Now()
	token := middleware.GetToken(r)

	// Deduct sats BEFORE doing work to prevent race conditions
	newBalance, err := h.DB.DeductSats(token, costSats, h.Cfg.SessionTTLHours)
	if err != nil {
		elapsed := int(time.Since(start).Milliseconds())
		sendJSON(w, http.StatusPaymentRequired, APIResponse{
			Success:    false,
			Error:      fmt.Sprintf("insufficient balance: need %d sats", costSats),
			CostSats:   0,
			Endpoint:   endpoint,
			ResponseMs: elapsed,
		})
		return
	}

	// Do the actual work
	data, err := workFn()
	elapsed := int(time.Since(start).Milliseconds())

	if err != nil {
		// Work failed — refund the sats
		h.DB.RefundSats(token, costSats)
		h.DB.LogCall(database.CallLog{
			SessionToken: token,
			Endpoint:     endpoint,
			CostSats:     0,
			ResponseMs:   elapsed,
			StatusCode:   500,
		})
		log.Printf("[%s] workFn error: %v", endpoint, err)
		sendJSON(w, http.StatusInternalServerError, APIResponse{
			Success:    false,
			Error:      "internal error processing request",
			CostSats:   0,
			Endpoint:   endpoint,
			ResponseMs: elapsed,
		})
		return
	}

	// Log the successful call
	h.DB.LogCall(database.CallLog{
		SessionToken: token,
		Endpoint:     endpoint,
		CostSats:     costSats,
		ResponseMs:   elapsed,
		StatusCode:   200,
	})

	// Send success response
	sendJSON(w, http.StatusOK, APIResponse{
		Success:          true,
		Data:             data,
		CostSats:         costSats,
		BalanceRemaining: newBalance,
		ResponseMs:       elapsed,
		Endpoint:         endpoint,
	})
}

// sendJSON writes a JSON response with the given status code
func sendJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}

// parseBody reads and decodes JSON from the request body.
// Usage: var req MyStruct; if err := parseBody(r, &req); err != nil { ... }
func parseBody(w http.ResponseWriter, r *http.Request, v interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	defer r.Body.Close()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("extraneous data after JSON object")
	}
	return fmt.Errorf("extraneous data after JSON object")
}

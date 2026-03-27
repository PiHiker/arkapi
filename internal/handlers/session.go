package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"

	"github.com/PiHiker/arkapi/internal/middleware"
)

// SessionRequest is the optional body for session creation
type SessionRequest struct {
	AmountSats int64 `json:"amount_sats,omitempty"`
}

// SessionResponse is returned when creating a session in test mode
type SessionResponse struct {
	Token       string `json:"token"`
	BalanceSats int64  `json:"balance_sats"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

// BarkSessionResponse is returned when creating a session in bark mode
type BarkSessionResponse struct {
	Token      string      `json:"token"`
	Funding    FundingInfo `json:"funding"`
	AmountSats int64       `json:"amount_sats"`
	BalanceSats int64      `json:"balance_sats"`
	Status     string      `json:"status"`
	ExpiresIn  int         `json:"expires_in"`
}

// FundingInfo contains payment details for the consumer
type FundingInfo struct {
	LightningInvoice string `json:"lightning_invoice"`
	ArkAddress       string `json:"ark_address"`
	PaymentHash      string `json:"payment_hash"`
}

// CreateSession handles POST /v1/sessions
func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	token, err := generateToken()
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}

	if h.Cfg.PaymentMode == "bark" {
		h.createBarkSession(w, r, token)
		return
	}

	// Test mode — instant funded session
	testBalance := h.Cfg.DefaultBalanceSats
	if err := h.DB.CreateSession(token, testBalance, h.Cfg.SessionTTLHours); err != nil {
		sendJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create session"})
		return
	}

	sendJSON(w, http.StatusCreated, SessionResponse{
		Token:       token,
		BalanceSats: testBalance,
		Status:      "active",
		Message:     "session created with test balance (signet mode)",
	})
}

// createBarkSession creates a session that requires real Lightning payment.
func (h *Handler) createBarkSession(w http.ResponseWriter, r *http.Request, token string) {
	if h.Bark == nil {
		sendJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bark wallet not configured"})
		return
	}

	// Parse optional amount from body
	var req SessionRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := parseBody(w, r, &req); err != nil {
			sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
	}
	if req.AmountSats <= 0 {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "amount_sats must be greater than 0"})
		return
	}
	if req.AmountSats > 1000000 {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "amount_sats exceeds maximum (1,000,000)"})
		return
	}

	// Generate Lightning invoice via barkd
	invoice, err := h.Bark.CreateInvoice(req.AmountSats)
	if err != nil {
		log.Printf("bark: failed to create invoice: %v", err)
		sendJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "failed to generate Lightning invoice"})
		return
	}

	// Get Ark address for direct payment option
	arkAddr, err := h.Bark.GetAddress()
	if err != nil {
		log.Printf("bark: failed to get address: %v", err)
		arkAddr = "" // Non-fatal — Lightning is the primary path
	}

	// Create session in DB with awaiting_payment status
	if err := h.DB.CreateAwaitingSession(token, invoice.PaymentHash, invoice.Invoice, arkAddr, h.Cfg.SessionTTLHours); err != nil {
		sendJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create session"})
		return
	}

	sendJSON(w, http.StatusCreated, BarkSessionResponse{
		Token: token,
		Funding: FundingInfo{
			LightningInvoice: invoice.Invoice,
			ArkAddress:       arkAddr,
			PaymentHash:      invoice.PaymentHash,
		},
		AmountSats:  req.AmountSats,
		BalanceSats: 0,
		Status:      "awaiting_payment",
		ExpiresIn:   h.Cfg.SessionTTLHours * 3600,
	})
}

// BalanceCheck handles GET /v1/balance
func (h *Handler) BalanceCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET"})
		return
	}

	session := middleware.GetSession(r)
	if session == nil {
		sendJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid session"})
		return
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"token":        session.Token,
		"balance_sats": session.BalanceSats,
		"status":       session.Status,
	})
}

func generateToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "ak_" + hex.EncodeToString(bytes), nil
}

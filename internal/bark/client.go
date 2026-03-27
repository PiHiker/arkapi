// Package bark provides a client for the barkd REST API.
// barkd is Second's Bark wallet daemon implementing the Ark protocol.
package bark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to the barkd REST API over the internal Docker network.
type Client struct {
	baseURL    string
	authToken  string
	httpClient *http.Client
}

// NewClient creates a new bark client.
func NewClient(baseURL, authToken string) *Client {
	return &Client{
		baseURL:   baseURL,
		authToken: authToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Invoice represents a Lightning invoice created by barkd.
type Invoice struct {
	Invoice     string `json:"invoice"`
	PaymentHash string `json:"payment_hash"`
	AmountSats  int64  `json:"amount_sat"`
}

// InvoiceStatus represents the status of a Lightning receive.
type InvoiceStatus struct {
	AmountSats         int64       `json:"amount_sat"`
	Invoice            string      `json:"invoice"`
	PaymentHash        string      `json:"payment_hash"`
	PaymentPreimage    string      `json:"payment_preimage"`
	HtlcVtxos         interface{} `json:"htlc_vtxos"`
	PreimageRevealedAt *string     `json:"preimage_revealed_at"`
	FinishedAt         *string     `json:"finished_at"`
}

// IsPaid returns true if the invoice has been settled.
func (s *InvoiceStatus) IsPaid() bool {
	return s.FinishedAt != nil && *s.FinishedAt != ""
}

// Balance represents the wallet balance from barkd.
type Balance struct {
	SpendableSat                int64 `json:"spendable_sat"`
	PendingInRoundSat           int64 `json:"pending_in_round_sat"`
	PendingLightningSendSat     int64 `json:"pending_lightning_send_sat"`
	ClaimableLightningReceiveSat int64 `json:"claimable_lightning_receive_sat"`
	PendingBoardSat             int64 `json:"pending_board_sat"`
}

// CreateInvoice generates a Lightning invoice for receiving payment.
func (c *Client) CreateInvoice(amountSats int64) (*Invoice, error) {
	body := map[string]int64{"amount_sat": amountSats}
	resp, err := c.post("/api/v1/lightning/receives/invoice", body)
	if err != nil {
		return nil, fmt.Errorf("create invoice request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, c.readError(resp)
	}

	// barkd returns {"invoice": "lnbs1..."} — we need to parse and extract payment_hash
	var invoiceResp struct {
		Invoice string `json:"invoice"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&invoiceResp); err != nil {
		return nil, fmt.Errorf("failed to decode invoice response: %w", err)
	}

	// Extract payment hash from the invoice by looking it up
	status, err := c.checkInvoiceByBolt11(invoiceResp.Invoice)
	if err != nil {
		// If we can't look it up yet, return with just the invoice string
		return &Invoice{
			Invoice:    invoiceResp.Invoice,
			AmountSats: amountSats,
		}, nil
	}

	return &Invoice{
		Invoice:     invoiceResp.Invoice,
		PaymentHash: status.PaymentHash,
		AmountSats:  amountSats,
	}, nil
}

// checkInvoiceByBolt11 looks up an invoice by its BOLT11 string.
func (c *Client) checkInvoiceByBolt11(bolt11 string) (*InvoiceStatus, error) {
	resp, err := c.get("/api/v1/lightning/receives/" + bolt11)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var status InvoiceStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode invoice status: %w", err)
	}
	return &status, nil
}

// CheckInvoice checks the status of a Lightning receive by payment hash or invoice string.
func (c *Client) CheckInvoice(identifier string) (*InvoiceStatus, error) {
	resp, err := c.get("/api/v1/lightning/receives/" + identifier)
	if err != nil {
		return nil, fmt.Errorf("check invoice request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var status InvoiceStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode invoice status: %w", err)
	}
	return &status, nil
}

// GetAddress returns the wallet's next Ark address.
func (c *Client) GetAddress() (string, error) {
	resp, err := c.post("/api/v1/wallet/addresses/next", nil)
	if err != nil {
		return "", fmt.Errorf("get address request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", c.readError(resp)
	}

	var addrResp struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&addrResp); err != nil {
		return "", fmt.Errorf("failed to decode address response: %w", err)
	}
	return addrResp.Address, nil
}

// GetBalance returns the current wallet balance.
func (c *Client) GetBalance() (*Balance, error) {
	resp, err := c.get("/api/v1/wallet/balance")
	if err != nil {
		return nil, fmt.Errorf("get balance request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var bal Balance
	if err := json.NewDecoder(resp.Body).Decode(&bal); err != nil {
		return nil, fmt.Errorf("failed to decode balance response: %w", err)
	}
	return &bal, nil
}

// Ping checks if barkd is reachable.
func (c *Client) Ping() error {
	resp, err := c.get("/ping")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("barkd ping returned status %d", resp.StatusCode)
	}
	return nil
}

// HistoryEntry represents a single wallet movement from barkd history.
type HistoryEntry struct {
	ID         int64           `json:"id"`
	Status     string          `json:"status"`
	Subsystem  HistorySubsystem `json:"subsystem"`
	EffectiveBalanceSat int64  `json:"effective_balance_sat"`
	SentTo     []ReceivedOn    `json:"sent_to"`
	ReceivedOn []ReceivedOn    `json:"received_on"`
	Time       HistoryTime     `json:"time"`
}

type HistorySubsystem struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type ReceivedOn struct {
	Destination ReceivedDestination `json:"destination"`
	AmountSat   int64               `json:"amount_sat"`
}

type ReceivedDestination struct {
	Type  string `json:"type"` // "ark" or "invoice"
	Value string `json:"value"`
}

type HistoryTime struct {
	CreatedAt   string `json:"created_at"`
	CompletedAt string `json:"completed_at"`
}

// GetHistory returns the wallet movement history.
func (c *Client) GetHistory() ([]HistoryEntry, error) {
	resp, err := c.get("/api/v1/wallet/history")
	if err != nil {
		return nil, fmt.Errorf("get history request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var history []HistoryEntry
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		return nil, fmt.Errorf("failed to decode history response: %w", err)
	}
	return history, nil
}

// FindArkReceive checks wallet history for a successful Ark payment involving a specific
// session address. This covers both normal receives from an external wallet and same-wallet
// self-funding, which appears in Bark history as an Ark send to that address.
func (c *Client) FindArkReceive(arkAddress string, afterTime time.Time) (int64, error) {
	history, err := c.GetHistory()
	if err != nil {
		return 0, err
	}

	for _, entry := range history {
		if entry.Status != "successful" {
			continue
		}
		if entry.Subsystem.Name != "bark.arkoor" {
			continue
		}
		// Check if completed after session creation
		if entry.Time.CompletedAt != "" {
			completedAt, err := time.Parse(time.RFC3339Nano, entry.Time.CompletedAt)
			if err == nil && completedAt.Before(afterTime) {
				continue
			}
		}
		// Check if received on the matching address
		for _, r := range entry.ReceivedOn {
			if r.Destination.Type == "ark" && r.Destination.Value == arkAddress {
				return r.AmountSat, nil
			}
		}
		// Same-wallet funding shows up as a successful Ark send to the session address.
		for _, r := range entry.SentTo {
			if r.Destination.Type == "ark" && r.Destination.Value == arkAddress {
				return r.AmountSat, nil
			}
		}
	}
	return 0, nil
}

// --- HTTP helpers ---

func (c *Client) get(path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	return c.httpClient.Do(req)
}

func (c *Client) post(path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	return c.httpClient.Do(req)
}

func (c *Client) readError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("barkd returned status %d: %s", resp.StatusCode, string(body))
}

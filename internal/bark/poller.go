package bark

import (
	"log"
	"time"

	"github.com/PiHiker/arkapi/internal/database"
)

// StartPaymentPoller runs a background goroutine that checks for paid invoices
// and Ark receives, then activates the corresponding sessions.
func StartPaymentPoller(client *Client, db *database.DB, ttlHours int) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			checkPendingPayments(client, db, ttlHours)
		}
	}()
	log.Println("bark: payment poller started (5s interval, Lightning + Ark)")
}

func checkPendingPayments(client *Client, db *database.DB, ttlHours int) {
	sessions, err := db.GetAwaitingSessions()
	if err != nil {
		log.Printf("bark: poller error fetching sessions: %v", err)
		return
	}

	for _, s := range sessions {
		// Try Lightning first
		if s.PaymentHash.Valid && s.PaymentHash.String != "" {
			status, err := client.CheckInvoice(s.PaymentHash.String)
			if err == nil && status.IsPaid() {
				if err := db.ActivateSession(s.Token, status.AmountSats, ttlHours, "lightning"); err != nil {
					log.Printf("bark: poller error activating session %s: %v", s.Token, err)
					continue
				}
				log.Printf("bark: session %s activated via Lightning (%d sats)",
					s.Token[:20]+"...", status.AmountSats)
				continue
			}
		}

		// Try Ark native payment
		if s.ArkAddress.Valid && s.ArkAddress.String != "" {
			amount, err := client.FindArkReceive(s.ArkAddress.String, s.CreatedAt)
			if err != nil {
				log.Printf("bark: poller error checking Ark receive for %s: %v", s.Token[:20]+"...", err)
				continue
			}
			if amount > 0 {
				if err := db.ActivateSession(s.Token, amount, ttlHours, "ark"); err != nil {
					log.Printf("bark: poller error activating session %s: %v", s.Token, err)
					continue
				}
				log.Printf("bark: session %s activated via Ark (%d sats)",
					s.Token[:20]+"...", amount)
				continue
			}
		}
	}
}

// CheckAndActivate checks a single session's payment status on demand (both paths).
// Returns true if the session was activated.
func CheckAndActivate(client *Client, db *database.DB, token string, s *database.Session, ttlHours int) bool {
	if client == nil {
		return false
	}

	// Try Lightning
	if s.PaymentHash.Valid && s.PaymentHash.String != "" {
		status, err := client.CheckInvoice(s.PaymentHash.String)
		if err == nil && status.IsPaid() {
			if err := db.ActivateSession(token, status.AmountSats, ttlHours, "lightning"); err != nil {
				log.Printf("bark: on-demand activation failed for %s: %v", token[:20]+"...", err)
				return false
			}
			log.Printf("bark: on-demand activation via Lightning: session %s (%d sats)", token[:20]+"...", status.AmountSats)
			return true
		}
	}

	// Try Ark
	if s.ArkAddress.Valid && s.ArkAddress.String != "" {
		amount, err := client.FindArkReceive(s.ArkAddress.String, s.CreatedAt)
		if err == nil && amount > 0 {
			if err := db.ActivateSession(token, amount, ttlHours, "ark"); err != nil {
				log.Printf("bark: on-demand activation failed for %s: %v", token[:20]+"...", err)
				return false
			}
			log.Printf("bark: on-demand activation via Ark: session %s (%d sats)", token[:20]+"...", amount)
			return true
		}
	}

	return false
}

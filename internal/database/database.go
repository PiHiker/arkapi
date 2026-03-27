// Package database handles all MySQL operations.
// Sessions are stored here, balances are checked and deducted here.
package database

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL driver - the underscore means "import for side effects only"
)

// DB wraps the MySQL connection pool
type DB struct {
	conn *sql.DB
}

// Session represents a funded API session
type Session struct {
	Token            string
	BalanceSats      int64
	Status           string
	CreatedAt        time.Time
	LastUsedAt       sql.NullTime
	ExpiresAt        sql.NullTime
	PaymentHash      sql.NullString
	LightningInvoice sql.NullString
	ArkAddress       sql.NullString
	FundingMethod    sql.NullString
}

// CallLog represents a single API call record
type CallLog struct {
	SessionToken string
	Endpoint     string
	CostSats     int
	ResponseMs   int
	StatusCode   int
}

// New creates a new database connection.
// sql.Open doesn't actually connect - it just prepares the connection pool.
// The first real query will establish the connection.
func New(dsn string) (*DB, error) {
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Connection pool settings - these are sensible defaults
	conn.SetMaxOpenConns(25)                 // Max simultaneous connections
	conn.SetMaxIdleConns(5)                  // Keep 5 connections ready
	conn.SetConnMaxLifetime(5 * time.Minute) // Recycle connections every 5 min

	// Actually test the connection
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &DB{conn: conn}, nil
}

// Close shuts down the connection pool
func (db *DB) Close() error {
	return db.conn.Close()
}

// CreateSession inserts a new session with the given token and balance.
// Returns the token for use in API calls.
func (db *DB) CreateSession(token string, balanceSats int64, ttlHours int) error {
	_, err := db.conn.Exec(
		"INSERT INTO sessions (token, balance_sats, status, expires_at) VALUES (?, ?, 'active', DATE_ADD(NOW(), INTERVAL ? HOUR))",
		token, balanceSats, ttlHours,
	)
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	return nil
}

// GetSession retrieves a session by token.
// Returns nil if not found.
func (db *DB) GetSession(token string) (*Session, error) {
	s := &Session{}
	err := db.conn.QueryRow(
		"SELECT token, balance_sats, status, created_at, last_used_at, expires_at, payment_hash, lightning_invoice, ark_address, funding_method FROM sessions WHERE token = ?",
		token,
	).Scan(&s.Token, &s.BalanceSats, &s.Status, &s.CreatedAt, &s.LastUsedAt, &s.ExpiresAt, &s.PaymentHash, &s.LightningInvoice, &s.ArkAddress, &s.FundingMethod)

	if err == sql.ErrNoRows {
		return nil, nil // Not found - not an error
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}
	return s, nil
}

// DeductSats subtracts sats from a session's balance.
// Returns the new balance, or an error if insufficient funds.
// This is atomic - no race conditions even with concurrent requests.
func (db *DB) DeductSats(token string, amount int, ttlHours int) (int64, error) {
	// Use a transaction to ensure atomicity
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	// If anything goes wrong, roll back
	defer tx.Rollback()

	// Lock the row and check balance
	var balance int64
	err = tx.QueryRow(
		"SELECT balance_sats FROM sessions WHERE token = ? AND status = 'active' FOR UPDATE",
		token,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("session not found or inactive: %w", err)
	}

	// Check sufficient funds
	if balance < int64(amount) {
		return balance, fmt.Errorf("insufficient balance: have %d, need %d", balance, amount)
	}

	// Deduct and update last_used timestamp
	newBalance := balance - int64(amount)
	_, err = tx.Exec(
		"UPDATE sessions SET balance_sats = ?, last_used_at = NOW(), expires_at = DATE_ADD(NOW(), INTERVAL ? HOUR) WHERE token = ?",
		newBalance, ttlHours, token,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to deduct sats: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}

	return newBalance, nil
}

// RefundSats adds sats back to a session (e.g. when work fails after deduction).
func (db *DB) RefundSats(token string, amount int) {
	_, _ = db.conn.Exec(
		"UPDATE sessions SET balance_sats = balance_sats + ? WHERE token = ? AND status = 'active'",
		amount, token,
	)
}

// LogCall records an API call in the call_log table.
// This is fire-and-forget - we don't want logging to slow down responses.
func (db *DB) LogCall(log CallLog) {
	// Using a goroutine so this doesn't block the response
	go func() {
		_, _ = db.conn.Exec(
			"INSERT INTO call_log (session_token, endpoint, cost_sats, response_ms, status_code) VALUES (?, ?, ?, ?, ?)",
			log.SessionToken, log.Endpoint, log.CostSats, log.ResponseMs, log.StatusCode,
		)
	}()
}

// CreateAwaitingSession inserts a session with status "awaiting_payment" and stores invoice details.
func (db *DB) CreateAwaitingSession(token string, paymentHash, lightningInvoice, arkAddress string, ttlHours int) error {
	_, err := db.conn.Exec(
		"INSERT INTO sessions (token, balance_sats, status, payment_hash, lightning_invoice, ark_address, expires_at) VALUES (?, 0, 'awaiting_payment', ?, ?, ?, DATE_ADD(NOW(), INTERVAL ? HOUR))",
		token, paymentHash, lightningInvoice, arkAddress, ttlHours,
	)
	if err != nil {
		return fmt.Errorf("failed to create awaiting session: %w", err)
	}
	return nil
}

// GetAwaitingSessions returns all sessions with status "awaiting_payment".
func (db *DB) GetAwaitingSessions() ([]*Session, error) {
	rows, err := db.conn.Query(
		"SELECT token, balance_sats, status, created_at, last_used_at, expires_at, payment_hash, lightning_invoice, ark_address, funding_method FROM sessions WHERE status = 'awaiting_payment'",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query awaiting sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		s := &Session{}
		if err := rows.Scan(&s.Token, &s.BalanceSats, &s.Status, &s.CreatedAt, &s.LastUsedAt, &s.ExpiresAt, &s.PaymentHash, &s.LightningInvoice, &s.ArkAddress, &s.FundingMethod); err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// ActivateSession sets a session to active with the given balance.
func (db *DB) ActivateSession(token string, balanceSats int64, ttlHours int, fundingMethod string) error {
	_, err := db.conn.Exec(
		"UPDATE sessions SET balance_sats = ?, status = 'active', expires_at = DATE_ADD(NOW(), INTERVAL ? HOUR), funding_method = ? WHERE token = ?",
		balanceSats, ttlHours, fundingMethod, token,
	)
	if err != nil {
		return fmt.Errorf("failed to activate session: %w", err)
	}
	return nil
}

// AddBalance adds sats to a session (used when payment is received)
func (db *DB) AddBalance(token string, amount int64) error {
	result, err := db.conn.Exec(
		"UPDATE sessions SET balance_sats = balance_sats + ?, status = 'active' WHERE token = ?",
		amount, token,
	)
	if err != nil {
		return fmt.Errorf("failed to add balance: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("session not found: %s", token)
	}
	return nil
}

// GetStats returns basic stats for the dashboard
type Stats struct {
	TotalCalls        int64
	TotalSats         int64
	ActiveSessions    int64
	EndpointBreakdown map[string]int64
	HourLabels        []string
	Calls24h          []int64
	Sats24h           []int64
}

type AdminRecentCall struct {
	SessionToken string    `json:"session_token"`
	Endpoint     string    `json:"endpoint"`
	CostSats     int64     `json:"cost_sats"`
	ResponseMs   int64     `json:"response_ms"`
	StatusCode   int64     `json:"status_code"`
	CreatedAt    time.Time `json:"created_at"`
}

type AdminStats struct {
	TotalCallsToday         int64             `json:"total_calls_today"`
	TotalSatsToday          int64             `json:"total_sats_today"`
	TotalCallsAllTime       int64             `json:"total_calls_all_time"`
	TotalSatsAllTime        int64             `json:"total_sats_all_time"`
	ActiveSessions          int64             `json:"active_sessions"`
	AwaitingSessions        int64             `json:"awaiting_sessions"`
	ExpiredSessions         int64             `json:"expired_sessions"`
	TotalSessions           int64             `json:"total_sessions"`
	ActiveBalanceSats       int64             `json:"active_balance_sats"`
	AwaitingBalanceSats     int64             `json:"awaiting_balance_sats"`
	EndpointBreakdownToday  map[string]int64  `json:"endpoint_breakdown_today"`
	EndpointBreakdownAllTime map[string]int64 `json:"endpoint_breakdown_all_time"`
	FundingBreakdown        map[string]int64  `json:"funding_breakdown"`
	RecentCalls             []AdminRecentCall `json:"recent_calls"`
}

func newEndpointMap() map[string]int64 {
	return map[string]int64{
		"dns-lookup":       0,
		"whois":            0,
		"ssl-check":        0,
		"headers":          0,
		"weather":          0,
		"ip-lookup":        0,
		"email-auth-check": 0,
		"bitcoin-news":     0,
		"ai-chat":          0,
		"ai-translate":     0,
		"translate":        0,
		"axfr-check":       0,
		"image-generate":   0,
		"screenshot":       0,
		"qr-generate":      0,
		"bitcoin-address":  0,
		"cve-search":       0,
		"prediction-market-search": 0,
		"domain-intel":     0,
		"domain-check":     0,
		"cve-lookup":       0,
		"btc-price":        0,
	}
}

func (db *DB) GetStats() (*Stats, error) {
	s := &Stats{
		EndpointBreakdown: newEndpointMap(),
		HourLabels: make([]string, 24),
		Calls24h:   make([]int64, 24),
		Sats24h:    make([]int64, 24),
	}

	startHour := time.Now().UTC().Truncate(time.Hour).Add(-23 * time.Hour)
	hourIndex := make(map[string]int, 24)
	for i := 0; i < 24; i++ {
		bucket := startHour.Add(time.Duration(i) * time.Hour)
		key := bucket.Format("2006-01-02 15")
		hourIndex[key] = i
		s.HourLabels[i] = bucket.Format("15:00")
	}

	// Total calls today
	db.conn.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(cost_sats), 0) FROM call_log WHERE created_at >= CURDATE()",
	).Scan(&s.TotalCalls, &s.TotalSats)

	// Active sessions
	db.conn.QueryRow(
		"SELECT COUNT(*) FROM sessions WHERE status = 'active' AND balance_sats > 0 AND (expires_at IS NULL OR expires_at >= UTC_TIMESTAMP())",
	).Scan(&s.ActiveSessions)

	rows, err := db.conn.Query(
		"SELECT TRIM(LEADING '/api/' FROM endpoint) AS endpoint_name, COUNT(*) FROM call_log WHERE created_at >= CURDATE() GROUP BY endpoint",
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var endpoint string
			var count int64
			if err := rows.Scan(&endpoint, &count); err == nil {
				s.EndpointBreakdown[endpoint] = count
			}
		}
	}

	hourlyRows, err := db.conn.Query(
		`SELECT DATE_FORMAT(created_at, '%Y-%m-%d %H') AS hour_bucket,
		        COUNT(*) AS call_count,
		        COALESCE(SUM(cost_sats), 0) AS sats_sum
		   FROM call_log
		  WHERE created_at >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL 24 HOUR)
		  GROUP BY hour_bucket
		  ORDER BY hour_bucket`,
	)
	if err == nil {
		defer hourlyRows.Close()
		for hourlyRows.Next() {
			var hourBucket string
			var callCount int64
			var satsSum int64
			if err := hourlyRows.Scan(&hourBucket, &callCount, &satsSum); err == nil {
				if idx, ok := hourIndex[hourBucket]; ok {
					s.Calls24h[idx] = callCount
					s.Sats24h[idx] = satsSum
				}
			}
		}
	}

	return s, nil
}

func (db *DB) GetAdminStats() (*AdminStats, error) {
	s := &AdminStats{
		EndpointBreakdownToday:   newEndpointMap(),
		EndpointBreakdownAllTime: newEndpointMap(),
		FundingBreakdown: map[string]int64{
			"lightning": 0,
			"ark":       0,
		},
		RecentCalls:              make([]AdminRecentCall, 0, 15),
	}

	db.conn.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(cost_sats), 0) FROM call_log WHERE created_at >= CURDATE()",
	).Scan(&s.TotalCallsToday, &s.TotalSatsToday)

	db.conn.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(cost_sats), 0) FROM call_log",
	).Scan(&s.TotalCallsAllTime, &s.TotalSatsAllTime)

	db.conn.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(balance_sats), 0) FROM sessions WHERE status = 'active' AND balance_sats > 0 AND (expires_at IS NULL OR expires_at >= UTC_TIMESTAMP())",
	).Scan(&s.ActiveSessions, &s.ActiveBalanceSats)

	db.conn.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(balance_sats), 0) FROM sessions WHERE status = 'awaiting_payment' AND (expires_at IS NULL OR expires_at >= UTC_TIMESTAMP())",
	).Scan(&s.AwaitingSessions, &s.AwaitingBalanceSats)

	db.conn.QueryRow(
		"SELECT COUNT(*) FROM sessions WHERE expires_at IS NOT NULL AND expires_at < UTC_TIMESTAMP()",
	).Scan(&s.ExpiredSessions)

	db.conn.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&s.TotalSessions)

	if rows, err := db.conn.Query(
		"SELECT funding_method, COUNT(*) FROM sessions WHERE funding_method IN ('lightning', 'ark') GROUP BY funding_method",
	); err == nil {
		defer rows.Close()
		for rows.Next() {
			var method string
			var count int64
			if err := rows.Scan(&method, &count); err == nil {
				s.FundingBreakdown[method] = count
			}
		}
	}

	if rows, err := db.conn.Query(
		"SELECT TRIM(LEADING '/api/' FROM endpoint) AS endpoint_name, COUNT(*) FROM call_log WHERE created_at >= CURDATE() GROUP BY endpoint",
	); err == nil {
		defer rows.Close()
		for rows.Next() {
			var endpoint string
			var count int64
			if err := rows.Scan(&endpoint, &count); err == nil {
				s.EndpointBreakdownToday[endpoint] = count
			}
		}
	}

	if rows, err := db.conn.Query(
		"SELECT TRIM(LEADING '/api/' FROM endpoint) AS endpoint_name, COUNT(*) FROM call_log GROUP BY endpoint",
	); err == nil {
		defer rows.Close()
		for rows.Next() {
			var endpoint string
			var count int64
			if err := rows.Scan(&endpoint, &count); err == nil {
				s.EndpointBreakdownAllTime[endpoint] = count
			}
		}
	}

	if rows, err := db.conn.Query(
		"SELECT session_token, endpoint, cost_sats, response_ms, status_code, created_at FROM call_log ORDER BY created_at DESC LIMIT 15",
	); err == nil {
		defer rows.Close()
		for rows.Next() {
			var c AdminRecentCall
			if err := rows.Scan(&c.SessionToken, &c.Endpoint, &c.CostSats, &c.ResponseMs, &c.StatusCode, &c.CreatedAt); err == nil {
				s.RecentCalls = append(s.RecentCalls, c)
			}
		}
	}

	return s, nil
}

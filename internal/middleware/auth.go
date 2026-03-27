// Package middleware provides HTTP middleware for the proxy.
// Middleware is code that runs BEFORE your handler - like a bouncer at a door.
package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/PiHiker/arkapi/internal/bark"
	"github.com/PiHiker/arkapi/internal/database"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	// SessionKey is the key used to store the session in the request context
	SessionKey contextKey = "session"
	// TokenKey stores the raw token string
	TokenKey contextKey = "token"
)

// Auth is middleware that validates the session token on every request.
// If the token is missing or invalid, it returns 401/403.
// If valid, it attaches the session to the request context so handlers can use it.
// AuthConfig holds optional dependencies for the Auth middleware.
type AuthConfig struct {
	BarkClient *bark.Client
	TTLHours   int
}

func Auth(db *database.DB, next http.Handler) http.Handler {
	return AuthWithBark(db, nil, next)
}

func AuthWithBark(db *database.DB, ac *AuthConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract token from Authorization header
		// Expected format: "Authorization: Bearer ak_xxxxx"
		auth := r.Header.Get("Authorization")
		if auth == "" {
			jsonError(w, http.StatusUnauthorized, "missing Authorization header — fund a session at POST /v1/sessions")
			return
		}

		// Strip "Bearer " prefix
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == auth {
			// "Bearer " prefix wasn't there
			jsonError(w, http.StatusUnauthorized, "invalid Authorization format — use: Bearer ak_xxxxx")
			return
		}

		token = strings.TrimSpace(token)

		// Look up the session in MySQL
		session, err := db.GetSession(token)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "database error")
			return
		}
		if session == nil {
			jsonError(w, http.StatusUnauthorized, "invalid session token")
			return
		}

		// If session is awaiting payment, try an on-demand check (Lightning or Ark)
		if session.Status == "awaiting_payment" && ac != nil && ac.BarkClient != nil {
			if bark.CheckAndActivate(ac.BarkClient, db, session.Token, session, ac.TTLHours) {
				// Re-fetch the now-activated session
				session, err = db.GetSession(token)
				if err != nil || session == nil {
					jsonError(w, http.StatusInternalServerError, "database error")
					return
				}
			}
		}

		// Check session is active
		if session.Status != "active" {
			if session.Status == "awaiting_payment" {
				jsonError(w, http.StatusPaymentRequired, "payment not yet received — pay the Lightning invoice to activate your session")
				return
			}
			jsonError(w, http.StatusForbidden, "session expired — fund a new session at POST /v1/sessions")
			return
		}
		if session.ExpiresAt.Valid && time.Now().After(session.ExpiresAt.Time) {
			jsonError(w, http.StatusForbidden, "session expired — fund a new session at POST /v1/sessions")
			return
		}

		// Check balance > 0 (specific cost check happens in handler)
		if session.BalanceSats <= 0 {
			jsonError(w, http.StatusPaymentRequired, "no sats remaining — top up your session")
			return
		}

		// Attach session and token to the request context
		// Handlers can retrieve these with GetSession(r) and GetToken(r)
		ctx := context.WithValue(r.Context(), SessionKey, session)
		ctx = context.WithValue(ctx, TokenKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetSession retrieves the session from the request context.
// Call this in your handlers after the Auth middleware has run.
func GetSession(r *http.Request) *database.Session {
	s, _ := r.Context().Value(SessionKey).(*database.Session)
	return s
}

// GetToken retrieves the raw token string from the request context.
func GetToken(r *http.Request) string {
	t, _ := r.Context().Value(TokenKey).(string)
	return t
}

// jsonError sends a JSON error response
func jsonError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   http.StatusText(code),
		"message": message,
		"code":    code,
	})
}

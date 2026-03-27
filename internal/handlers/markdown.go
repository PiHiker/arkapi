package handlers

import (
	"fmt"
	"net/http"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/go-shiori/go-readability"
)

// MarkdownRequest is what the consumer sends
type MarkdownRequest struct {
	URL string `json:"url"`
}

// MarkdownResponse is what we return
type MarkdownResponse struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
	Excerpt  string `json:"excerpt,omitempty"`
}

// URLToMarkdown handles /api/url-to-markdown
// Cost: 5 sats
func (h *Handler) URLToMarkdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}

	var req MarkdownRequest
	if err := parseBody(w, r, &req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON — send {\"url\": \"https://example.com\"}"})
		return
	}

	if req.URL == "" {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}

	// Re-use SSRF protection from headers handler
	// validateSafeURL returns (net.IP, error)
	_, err := validateSafeURL(req.URL)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h.executeHandler(w, r, "/api/url-to-markdown", 5, func() (interface{}, error) {
		return doURLToMarkdown(req.URL)
	})
}

func doURLToMarkdown(targetURL string) (*MarkdownResponse, error) {
	// 1. Fetch and Extract using Readability
	// We use a 10s timeout for the fetch
	article, err := readability.FromURL(targetURL, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("readability extraction failed: %w", err)
	}

	// 2. Convert Cleaned HTML to Markdown
	converter := md.NewConverter("", true, nil)
	markdown, err := converter.ConvertString(article.Content)
	if err != nil {
		return nil, fmt.Errorf("markdown conversion failed: %w", err)
	}

	return &MarkdownResponse{
		URL:      targetURL,
		Title:    article.Title,
		Markdown: markdown,
		Excerpt:  article.Excerpt,
	}, nil
}

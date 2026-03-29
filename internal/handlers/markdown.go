package handlers

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/go-shiori/go-readability"
)

const markdownMaxBodyBytes = 2 << 20

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

	safeURL, pinnedIP, err := parseAndValidateSafeURL(req.URL)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h.executeHandler(w, r, "/api/url-to-markdown", 5, func() (interface{}, error) {
		return doURLToMarkdown(safeURL, pinnedIP)
	})
}

func doURLToMarkdown(targetURL *url.URL, pinnedIP net.IP) (*MarkdownResponse, error) {
	body, finalURL, err := fetchSafeHTML(targetURL, pinnedIP)
	if err != nil {
		return nil, err
	}

	article, err := readability.FromReader(bytes.NewReader(body), finalURL)
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
		URL:      finalURL.String(),
		Title:    article.Title,
		Markdown: markdown,
		Excerpt:  article.Excerpt,
	}, nil
}

func fetchSafeHTML(targetURL *url.URL, pinnedIP net.IP) ([]byte, *url.URL, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &safeHTMLRoundTripper{
			initialURL: targetURL,
			initialIP:  pinnedIP,
		},
	}

	req, err := http.NewRequest(http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build markdown request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("markdown fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("markdown fetch returned status %d", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" && !strings.Contains(strings.ToLower(contentType), "text/html") {
		return nil, nil, fmt.Errorf("URL is not an HTML document")
	}

	limited := io.LimitReader(resp.Body, markdownMaxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, fmt.Errorf("read markdown response: %w", err)
	}
	if len(body) > markdownMaxBodyBytes {
		return nil, nil, fmt.Errorf("HTML document too large")
	}

	finalURL := targetURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = cloneURL(resp.Request.URL)
	}
	return body, finalURL, nil
}

type safeHTMLRoundTripper struct {
	initialURL *url.URL
	initialIP  net.IP
}

func (rt *safeHTMLRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("invalid request")
	}

	safeURL := cloneURL(req.URL)
	pinnedIP := rt.initialIP
	if rt.initialURL == nil || req.URL.String() != rt.initialURL.String() {
		var err error
		safeURL, pinnedIP, err = parseAndValidateSafeURL(req.URL.String())
		if err != nil {
			return nil, err
		}
	}

	transport := &http.Transport{
		DialContext: pinnedDialer(pinnedIP),
	}

	clonedReq := req.Clone(req.Context())
	clonedReq.URL = safeURL
	return transport.RoundTrip(clonedReq)
}

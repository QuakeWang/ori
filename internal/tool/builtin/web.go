package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuakeWang/ori/internal/tool"
)

const (
	defaultRequestTimeoutSeconds = 10
	maxResponseBytes             = 512 * 1024 // 512 KB
)

var defaultFetchHeaders = map[string]string{
	"Accept": "text/markdown",
}

var webFetchTransport http.RoundTripper = http.DefaultTransport

var webFetchLookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

type webFetchInput struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

func newWebFetchClient() *http.Client {
	return &http.Client{
		Transport: webFetchTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return validateWebFetchURL(req.Context(), req.URL)
		},
	}
}

func validateWebFetchTarget(ctx context.Context, rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if err := validateWebFetchURL(ctx, parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func validateWebFetchURL(ctx context.Context, parsed *url.URL) error {
	if parsed == nil {
		return fmt.Errorf("url is required")
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("blocked fetch target: unsupported scheme %q", parsed.Scheme)
	}

	host := normalizeWebFetchHost(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("url host is required")
	}
	if isBlockedWebFetchHost(host) {
		return fmt.Errorf("blocked fetch target: %s", host)
	}

	if ip := net.ParseIP(host); ip != nil {
		if isBlockedWebFetchIP(ip) {
			return fmt.Errorf("blocked fetch target: %s", host)
		}
		return nil
	}

	addrs, err := webFetchLookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve host %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("resolve host %q: no addresses", host)
	}
	for _, addr := range addrs {
		if isBlockedWebFetchIP(addr.IP) {
			return fmt.Errorf("blocked fetch target: %s resolves to %s", host, addr.IP.String())
		}
	}
	return nil
}

func normalizeWebFetchHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

func isBlockedWebFetchHost(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func isBlockedWebFetchIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 {
			return true
		}
		if ip4[0] == 100 && ip4[1]&0xc0 == 64 {
			return true // RFC6598 shared address space.
		}
	}
	return false
}

func webFetchTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "web.fetch",
			Description: "Fetch(GET) the content of a web page, returning markdown if possible.",
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"url":{"type":"string","description":"The URL to fetch"},
					"headers":{"type":"object","description":"Optional HTTP headers","additionalProperties":{"type":"string"}},
					"timeout":{"type":"integer","description":"Timeout in seconds (default 10)"}
				},
				"required":["url"]
			}`),
			Dangerous: true,
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in webFetchInput
			if err := decodeToolInput(input, &in, "web.fetch"); err != nil {
				return nil, err
			}

			reqCtx, cancel := context.WithTimeout(ctx, webFetchTimeout(in.Timeout))
			defer cancel()

			req, err := newWebFetchRequest(reqCtx, in)
			if err != nil {
				return nil, err
			}

			resp, err := newWebFetchClient().Do(req)
			if err != nil {
				return nil, fmt.Errorf("fetch %s: %w", in.URL, err)
			}
			defer func() { _ = resp.Body.Close() }()

			return webFetchResult(resp, in.URL)
		},
	}
}

func webFetchTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = defaultRequestTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func newWebFetchRequest(ctx context.Context, in webFetchInput) (*http.Request, error) {
	if in.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	parsedURL, err := validateWebFetchTarget(ctx, in.URL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	applyWebFetchHeaders(req.Header, in.Headers)
	return req, nil
}

func applyWebFetchHeaders(header http.Header, overrides map[string]string) {
	for k, v := range defaultFetchHeaders {
		header.Set(k, v)
	}
	for k, v := range overrides {
		header.Set(k, v)
	}
}

func webFetchResult(resp *http.Response, rawURL string) (*tool.Result, error) {
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch %s: HTTP %d %s", rawURL, resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return &tool.Result{
		Text:      string(body),
		Truncated: len(body) >= maxResponseBytes,
	}, nil
}

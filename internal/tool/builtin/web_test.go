package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/tool"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func stubWebFetchTransport(fn roundTripFunc) http.RoundTripper {
	return fn
}

func stubWebFetchResolver(addrs map[string][]net.IPAddr) func(context.Context, string) ([]net.IPAddr, error) {
	return func(_ context.Context, host string) ([]net.IPAddr, error) {
		if resolved, ok := addrs[host]; ok {
			return resolved, nil
		}
		return nil, errors.New("unexpected host lookup")
	}
}

func setWebFetchHooks(t *testing.T, transport http.RoundTripper, resolver func(context.Context, string) ([]net.IPAddr, error)) {
	t.Helper()
	previousTransport := webFetchTransport
	previousResolver := webFetchLookupIPAddr
	webFetchTransport = transport
	webFetchLookupIPAddr = resolver
	t.Cleanup(func() {
		webFetchTransport = previousTransport
		webFetchLookupIPAddr = previousResolver
	})
}

func testHTTPResponse(req *http.Request, statusCode int, body string, headers map[string]string) *http.Response {
	header := make(http.Header)
	for k, v := range headers {
		header.Set(k, v)
	}
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestWebFetchTool_Success(t *testing.T) {
	setWebFetchHooks(t,
		stubWebFetchTransport(func(req *http.Request) (*http.Response, error) {
			return testHTTPResponse(req, http.StatusOK, "# Hello World", map[string]string{"Content-Type": "text/markdown"}), nil
		}),
		stubWebFetchResolver(map[string][]net.IPAddr{
			"example.com": {{IP: net.ParseIP("93.184.216.34")}},
		}),
	)

	wf := webFetchTool()
	input, _ := json.Marshal(map[string]string{"url": "https://example.com/docs"})
	result, err := wf.Handler(context.Background(), &tool.Context{}, input)
	require.NoError(t, err)
	assert.Equal(t, "# Hello World", result.Text)
}

func TestWebFetchTool_InvalidInput(t *testing.T) {
	wf := webFetchTool()
	_, err := wf.Handler(context.Background(), &tool.Context{}, json.RawMessage(`{"url":`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid web.fetch input")
}

func TestWebFetchTool_DefaultHeaders(t *testing.T) {
	var receivedAccept string
	setWebFetchHooks(t,
		stubWebFetchTransport(func(req *http.Request) (*http.Response, error) {
			receivedAccept = req.Header.Get("Accept")
			return testHTTPResponse(req, http.StatusOK, "ok", nil), nil
		}),
		stubWebFetchResolver(map[string][]net.IPAddr{
			"example.com": {{IP: net.ParseIP("93.184.216.34")}},
		}),
	)

	wf := webFetchTool()
	input, _ := json.Marshal(map[string]string{"url": "https://example.com/docs"})
	_, err := wf.Handler(context.Background(), &tool.Context{}, input)
	require.NoError(t, err)
	assert.Equal(t, "text/markdown", receivedAccept)
}

func TestWebFetchTool_CustomHeaders(t *testing.T) {
	var receivedAuth string
	setWebFetchHooks(t,
		stubWebFetchTransport(func(req *http.Request) (*http.Response, error) {
			receivedAuth = req.Header.Get("Authorization")
			return testHTTPResponse(req, http.StatusOK, "ok", nil), nil
		}),
		stubWebFetchResolver(map[string][]net.IPAddr{
			"example.com": {{IP: net.ParseIP("93.184.216.34")}},
		}),
	)

	wf := webFetchTool()
	input, _ := json.Marshal(map[string]any{
		"url":     "https://example.com/docs",
		"headers": map[string]string{"Authorization": "Bearer test-token"},
	})
	_, err := wf.Handler(context.Background(), &tool.Context{}, input)
	require.NoError(t, err)
	assert.Equal(t, "Bearer test-token", receivedAuth)
}

func TestWebFetchTool_HTTPError(t *testing.T) {
	setWebFetchHooks(t,
		stubWebFetchTransport(func(req *http.Request) (*http.Response, error) {
			return testHTTPResponse(req, http.StatusNotFound, "", nil), nil
		}),
		stubWebFetchResolver(map[string][]net.IPAddr{
			"example.com": {{IP: net.ParseIP("93.184.216.34")}},
		}),
	)

	wf := webFetchTool()
	input, _ := json.Marshal(map[string]string{"url": "https://example.com/docs"})
	_, err := wf.Handler(context.Background(), &tool.Context{}, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestWebFetchTool_Timeout(t *testing.T) {
	setWebFetchHooks(t,
		stubWebFetchTransport(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
		stubWebFetchResolver(map[string][]net.IPAddr{
			"example.com": {{IP: net.ParseIP("93.184.216.34")}},
		}),
	)

	wf := webFetchTool()
	input, _ := json.Marshal(map[string]any{"url": "https://example.com/docs", "timeout": 1})
	_, err := wf.Handler(context.Background(), &tool.Context{}, input)
	assert.Error(t, err)
}

func TestWebFetchTool_EmptyURL(t *testing.T) {
	wf := webFetchTool()
	input := json.RawMessage(`{"url":""}`)
	_, err := wf.Handler(context.Background(), &tool.Context{}, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "url is required")
}

func TestWebFetchTool_BlocksLoopbackTarget(t *testing.T) {
	setWebFetchHooks(t,
		stubWebFetchTransport(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("transport should not be called for blocked loopback target: %s", req.URL.String())
			return nil, nil
		}),
		stubWebFetchResolver(nil),
	)

	wf := webFetchTool()
	input := json.RawMessage(`{"url":"http://127.0.0.1:8080"}`)
	_, err := wf.Handler(context.Background(), &tool.Context{}, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked fetch target")
}

func TestWebFetchTool_BlocksPrivateResolvedAddress(t *testing.T) {
	setWebFetchHooks(t,
		stubWebFetchTransport(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("transport should not be called for blocked private target: %s", req.URL.String())
			return nil, nil
		}),
		stubWebFetchResolver(map[string][]net.IPAddr{
			"internal.example": {{IP: net.ParseIP("10.0.0.8")}},
		}),
	)

	wf := webFetchTool()
	input := json.RawMessage(`{"url":"https://internal.example/query"}`)
	_, err := wf.Handler(context.Background(), &tool.Context{}, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked fetch target")
}

func TestWebFetchTool_BlocksRedirectToPrivateTarget(t *testing.T) {
	callCount := 0
	setWebFetchHooks(t,
		stubWebFetchTransport(func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return testHTTPResponse(req, http.StatusFound, "", map[string]string{
					"Location": "http://169.254.169.254/latest/meta-data",
				}), nil
			}
			t.Fatalf("redirect target should be blocked before the second round trip: %s", req.URL.String())
			return nil, nil
		}),
		stubWebFetchResolver(map[string][]net.IPAddr{
			"example.com": {{IP: net.ParseIP("93.184.216.34")}},
		}),
	)

	wf := webFetchTool()
	input := json.RawMessage(`{"url":"https://example.com/redirect"}`)
	_, err := wf.Handler(context.Background(), &tool.Context{}, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked fetch target")
}

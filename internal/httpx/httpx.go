// Package httpx wraps net/http with the request discipline both the packages
// API and the registry client need: a timeout, a bounded body, and a non-2xx
// status turned into an error that says whether retrying could help.
package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/henrytill/prune-ghcr/internal/retry"
)

// UserAgent identifies the action to both the API and the registry.
const UserAgent = "prune-ghcr"

// Timeout bounds a single request. http.Client has no default timeout, so
// without this a wedged read would stop being a retryable failure and become a
// job that hangs until the workflow's own limit.
const Timeout = 30 * time.Second

// MaxBodyBytes bounds how much of a response is read.
const MaxBodyBytes = 32 << 20

// Client performs requests and returns their bodies.
type Client struct {
	http *http.Client
}

// NewClient returns a Client with the package timeout applied.
func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: Timeout}}
}

// Response is the part of an HTTP response the callers use.
type Response struct {
	Body   []byte
	Header http.Header
}

// Do performs a request and returns its body.
//
// A non-2xx status becomes an error built with retry.StatusError, so a 403 or
// 404 fails immediately while a 502 is retried. The message includes the body,
// which is where the API puts its explanation.
func (c *Client) Do(ctx context.Context, method, url string, headers map[string]string) (*Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, &retry.NonRetryableError{Message: fmt.Sprintf("building request for %s: %s", url, err)}
	}
	request.Header.Set("user-agent", UserAgent)
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes))
	if err != nil {
		return nil, err
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, retry.StatusError(
			fmt.Sprintf("%s %s returned %d: %s", method, url, response.StatusCode, strings.TrimSpace(string(body))),
			response.StatusCode)
	}

	return &Response{Body: body, Header: response.Header}, nil
}

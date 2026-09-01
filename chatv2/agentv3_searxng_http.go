//go:build !386 && !arm

package chatv2

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
)

func (c *searXNGClient) getJSON(ctx context.Context, endpoint string, query url.Values, headers map[string]string) ([]byte, error) {
	requestURL, err := c.requestURL(endpoint, query)
	if err != nil {
		return nil, newSearXNGError(searXNGErrorRequestFailed)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, newSearXNGError(searXNGErrorRequestFailed)
	}
	req.Header.Set("User-Agent", c.config.UserAgent)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	if c.config.UsernameEnv != "" || c.config.PasswordEnv != "" {
		if c.config.UsernameEnv == "" || c.config.PasswordEnv == "" || c.getenv == nil {
			return nil, newSearXNGError(searXNGErrorUnavailable)
		}
		username, password := c.getenv(c.config.UsernameEnv), c.getenv(c.config.PasswordEnv)
		if username == "" || password == "" {
			return nil, newSearXNGError(searXNGErrorUnavailable)
		}
		req.SetBasicAuth(username, password)
	}
	if c.httpClient == nil {
		return nil, newSearXNGError(searXNGErrorRequestFailed)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if searXNGTimedOut(ctx, err) {
			return nil, newSearXNGError(searXNGErrorTimeout)
		}
		return nil, newSearXNGError(searXNGErrorRequestFailed)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusInternalServerError {
		return nil, newSearXNGError(searXNGErrorUnavailable)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, newSearXNGError(searXNGErrorRequestFailed)
	}
	if c.config.MaxResponseBytes < 1 {
		return nil, newSearXNGError(searXNGErrorInvalidResponse)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.config.MaxResponseBytes+1))
	if err != nil {
		if searXNGTimedOut(ctx, err) {
			return nil, newSearXNGError(searXNGErrorTimeout)
		}
		return nil, newSearXNGError(searXNGErrorRequestFailed)
	}
	if int64(len(body)) > c.config.MaxResponseBytes {
		return nil, newSearXNGError(searXNGErrorInvalidResponse)
	}
	return body, nil
}

func (c *searXNGClient) requestURL(endpoint string, query url.Values) (*url.URL, error) {
	if c == nil || c.baseURL == nil {
		return nil, errSearXNGMissingBaseURL
	}
	joined, err := url.JoinPath(c.baseURL.String(), endpoint)
	if err != nil {
		return nil, err
	}
	requestURL, err := url.Parse(joined)
	if err != nil || !sameSearXNGOrigin(c.baseURL, requestURL) {
		return nil, errSearXNGOriginChanged
	}
	requestURL.RawQuery = query.Encode()
	return requestURL, nil
}

func searXNGTimedOut(ctx context.Context, err error) bool {
	var networkError net.Error
	return errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout()
}

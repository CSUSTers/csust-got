package agentv3

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"csust-got/config"
)

var (
	errImageURLScheme       = errors.New("image url must use http or https")
	errImageURLHost         = errors.New("image url has no host")
	errImageURLPort         = errors.New("image url port must be 80 or 443")
	errImageURLResolve      = errors.New("image url host does not resolve")
	errImageURLNotPublic    = errors.New("image url resolves to a non-public address")
	errImageURLRedirects    = errors.New("image url has too many redirects")
	errImageURLDialTarget   = errors.New("image download dial target is not a public address")
	errImageURLProxyConnect = errors.New("image download proxy refused the tunnel")
	errImageProxyUnusable   = errors.New("image download proxy is unusable")
)

const (
	agentImageURLMaxRedirects    = 5
	agentImageURLResolveWindow   = 5 * time.Second
	agentImageProxyConnectWindow = 15 * time.Second
	agentImageClientTimeout      = 60 * time.Second
	agentImageDialTimeout        = 15 * time.Second
	agentImageAttemptMinBudget   = 2 * time.Second
)

// lookupPublicImageHost resolves hosts for public-address checks; tests override it.
var lookupPublicImageHost = func(ctx context.Context, host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(ctx, agentImageURLResolveWindow)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

// checkImageTarget is the single policy gate for image download targets; tests may relax it.
var checkImageTarget = defaultImageTargetCheck

func defaultImageTargetCheck(ip net.IP, port string) error {
	if port != "" && port != "80" && port != "443" {
		return fmt.Errorf("%w: %s", errImageURLPort, port)
	}
	if ip != nil && !isPublicIP(ip) {
		return fmt.Errorf("%w: %s", errImageURLNotPublic, ip)
	}
	return nil
}

// isPublicIP rejects loopback, private, link-local, multicast, unspecified, CGNAT, reserved and ULA ranges.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	switch {
	case ip.IsUnspecified(), ip.IsLoopback(), ip.IsPrivate(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 0:
			return false
		case ip4[0] == 100 && ip4[1]&0xc0 == 64: // 100.64.0.0/10 shared address space
			return false
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // 192.0.0.0/24 IETF protocol assignments
			return false
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 2, ip4[0] == 198 && ip4[1] == 51 && ip4[2] == 100, ip4[0] == 203 && ip4[1] == 0 && ip4[2] == 113:
			return false
		case ip4[0] == 198 && ip4[1]&0xfe == 18: // 198.18.0.0/15 benchmarking
			return false
		case ip4[0] >= 240: // 240.0.0.0/4 reserved and broadcast
			return false
		}
		return true
	}
	if len(ip) == net.IPv6len {
		switch {
		case ip[0]&0xfe == 0xfc: // fc00::/7 unique local
			return false
		case ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8: // 2001:db8::/32 documentation
			return false
		case ip[0] == 0x01 && ip[1] == 0 && ip[2] == 0 && ip[3] == 0: // 100::/64 discard
			return false
		case ip[0] == 0 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b: // 64:ff9b::/96 NAT64 maps IPv4 space
			return isPublicIP(net.IPv4(ip[12], ip[13], ip[14], ip[15]))
		}
	}
	return true
}

// validatePublicImageURL enforces http(s), a standard port and a public resolved address.
func validatePublicImageURL(ctx context.Context, raw string) (*url.URL, error) {
	parsed, _, err := resolvePublicImageURL(ctx, raw)
	return parsed, err
}

// resolvePublicImageURL validates the URL and returns the addresses every later connection must be pinned to.
func resolvePublicImageURL(ctx context.Context, raw string) (*url.URL, []net.IP, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, nil, err
	}
	if !isHTTPScheme(parsed.Scheme) {
		return nil, nil, errImageURLScheme
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, nil, errImageURLHost
	}
	if err := checkImageTarget(nil, parsed.Port()); err != nil {
		return nil, nil, err
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if err := checkImageTarget(ip, ""); err != nil {
			return nil, nil, err
		}
		return parsed, []net.IP{ip}, nil
	}
	ips, err := lookupPublicImageHost(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, nil, fmt.Errorf("%w: %s", errImageURLResolve, host)
	}
	for _, ip := range ips {
		if err := checkImageTarget(ip, ""); err != nil {
			return nil, nil, fmt.Errorf("%w (%s)", err, host)
		}
	}
	return parsed, ips, nil
}

// imageDownloadProxy returns the configured bot proxy, or nil for direct downloads.
func imageDownloadProxy() (*url.URL, error) {
	if config.BotConfig == nil || config.BotConfig.Proxy == "" {
		return nil, nil
	}
	proxyURL, err := url.Parse(config.BotConfig.Proxy)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(proxyURL.Scheme)
	if !isHTTPScheme(scheme) && scheme != "socks5" && scheme != "socks5h" {
		return nil, fmt.Errorf("%w: unsupported proxy scheme %q", errImageURLDialTarget, proxyURL.Scheme)
	}
	return proxyURL, nil
}

func isHTTPScheme(scheme string) bool {
	return strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")
}

func isHTTPProxy(proxyURL *url.URL) bool {
	return proxyURL != nil && isHTTPScheme(proxyURL.Scheme)
}

// pinnedDialTargets re-resolves and validates a dial address and returns every validated ip:port in resolver order.
func pinnedDialTargets(ctx context.Context, addr string) ([]string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := lookupPublicImageHost(ctx, host)
	if ip := net.ParseIP(host); ip != nil {
		ips, err = []net.IP{ip}, nil
	}
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("%w: %s", errImageURLResolve, host)
	}
	targets := make([]string, 0, len(ips))
	for _, ip := range ips {
		if err := checkImageTarget(ip, ""); err != nil {
			return nil, fmt.Errorf("%w: %w", errImageURLDialTarget, err)
		}
		targets = append(targets, net.JoinHostPort(ip.String(), port))
	}
	return targets, nil
}

// imageHopDeadline is the time budget of one hop: the caller deadline capped by the client timeout.
func imageHopDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(agentImageClientTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		return d
	}
	return deadline
}

// imageAttemptBudget splits the remaining hop budget evenly over the remaining candidates, like net.Dialer.
func imageAttemptBudget(deadline time.Time, remaining int) time.Duration {
	budget := time.Until(deadline)
	if remaining > 1 {
		budget /= time.Duration(remaining)
	}
	return max(budget, agentImageAttemptMinBudget)
}

// dialImageCandidates tries the validated targets in order and stops early on caller cancellation or proxy failure.
func dialImageCandidates(ctx context.Context, deadline time.Time, targets []string,
	dial func(context.Context, string) (net.Conn, error)) (net.Conn, error) {
	errs := make([]error, 0, len(targets))
	for i, target := range targets {
		attemptCtx, cancel := context.WithTimeout(ctx, imageAttemptBudget(deadline, len(targets)-i))
		conn, err := dial(attemptCtx, target)
		cancel()
		if err == nil {
			return conn, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", target, err))
		if ctx.Err() != nil || errors.Is(err, errImageProxyUnusable) {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// newPublicImageHTTPClient builds a single-hop client that never follows redirects itself.
// Direct and HTTP-proxy hops pin the connection to validated addresses in the dialer (HTTP proxies via CONNECT,
// so Host and SNI stay the original hostname) and fall back through them in order within deadline.
// SOCKS hops are pinned through the request URL, and serverName restores certificate verification for https.
func newPublicImageHTTPClient(proxyURL *url.URL, serverName string, deadline time.Time) *http.Client {
	dialer := &net.Dialer{Timeout: agentImageDialTimeout}
	transport := &http.Transport{
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	switch {
	case proxyURL == nil:
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			targets, err := pinnedDialTargets(ctx, addr)
			if err != nil {
				return nil, err
			}
			return dialImageCandidates(ctx, deadline, targets, func(ctx context.Context, target string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, target)
			})
		}
	case isHTTPProxy(proxyURL):
		transport.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
			targets, err := pinnedDialTargets(ctx, addr)
			if err != nil {
				return nil, err
			}
			return dialImageCandidates(ctx, deadline, targets, func(ctx context.Context, target string) (net.Conn, error) {
				return dialThroughHTTPProxy(ctx, dialer, proxyURL, target)
			})
		}
	default:
		transport.Proxy = http.ProxyURL(proxyURL)
		transport.DialContext = dialer.DialContext
		transport.TLSClientConfig = &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Transport: transport,
		Timeout:   agentImageClientTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// dialThroughHTTPProxy opens a CONNECT tunnel to an already validated ip:port.
// Failures to reach or authenticate with the proxy wrap errImageProxyUnusable so other targets are not tried.
func dialThroughHTTPProxy(ctx context.Context, dialer *net.Dialer, proxyURL *url.URL, target string) (net.Conn, error) {
	proxyAddr := proxyURL.Host
	if proxyURL.Port() == "" {
		port := "80"
		if strings.EqualFold(proxyURL.Scheme, "https") {
			port = "443"
		}
		proxyAddr = net.JoinHostPort(proxyURL.Hostname(), port)
	}
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errImageProxyUnusable, err)
	}
	if strings.EqualFold(proxyURL.Scheme, "https") {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: proxyURL.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: %w", errImageProxyUnusable, err)
		}
		conn = tlsConn
	}
	deadline := time.Now().Add(agentImageProxyConnectWindow)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	if err := writeImageProxyConnect(conn, proxyURL, target); err != nil {
		stop()
		_ = conn.Close()
		return nil, err
	}
	if !stop() {
		_ = conn.Close()
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func writeImageProxyConnect(conn net.Conn, proxyURL *url.URL, target string) error {
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username()+":"+password)))
	}
	if err := req.Write(conn); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusProxyAuthRequired:
		return fmt.Errorf("%w: %w: %s", errImageProxyUnusable, errImageURLProxyConnect, resp.Status)
	default:
		return fmt.Errorf("%w: %s", errImageURLProxyConnect, resp.Status)
	}
}

// pinImageRequest rewrites the request target to the validated address while keeping the original Host.
func pinImageRequest(req *http.Request, target *url.URL, ip net.IP) {
	pinned := *target
	host := ip.String()
	if ip.To4() == nil {
		host = "[" + host + "]"
	}
	if port := target.Port(); port != "" {
		host = net.JoinHostPort(ip.String(), port)
	}
	pinned.Host = host
	req.URL = &pinned
	req.Host = target.Host
}

// fetchPublicImageURL downloads an image from a public http(s) URL, validating and pinning every redirect hop.
func fetchPublicImageURL(ctx context.Context, rawURL string) (*http.Response, error) {
	proxyURL, err := imageDownloadProxy()
	if err != nil {
		return nil, err
	}
	target := rawURL
	for hop := 0; hop <= agentImageURLMaxRedirects; hop++ {
		resp, next, err := fetchPublicImageHop(ctx, proxyURL, target)
		if err != nil {
			return nil, err
		}
		if next == "" {
			return resp, nil
		}
		target = next
	}
	return nil, errImageURLRedirects
}

func fetchPublicImageHop(ctx context.Context, proxyURL *url.URL, rawURL string) (*http.Response, string, error) {
	target, ips, err := resolvePublicImageURL(ctx, rawURL)
	if err != nil {
		return nil, "", err
	}
	deadline := imageHopDeadline(ctx)
	client := newPublicImageHTTPClient(proxyURL, target.Hostname(), deadline)
	defer client.CloseIdleConnections()
	var resp *http.Response
	if proxyURL != nil && !isHTTPProxy(proxyURL) {
		resp, err = doPinnedImageRequests(ctx, client, target, ips, deadline)
	} else {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return nil, "", err
		}
		resp, err = client.Do(req)
	}
	if err != nil {
		return nil, "", err
	}
	location := resp.Header.Get("Location")
	if !isImageRedirectStatus(resp.StatusCode) || location == "" {
		return resp, "", nil
	}
	_ = resp.Body.Close()
	next, err := target.Parse(location)
	if err != nil {
		return nil, "", err
	}
	return nil, next.String(), nil
}

// doPinnedImageRequests sends the request pinned to each validated IP in order, moving on only while no
// connection could be established; HTTP responses and post-connect failures are returned as-is.
func doPinnedImageRequests(ctx context.Context, client *http.Client, target *url.URL, ips []net.IP, deadline time.Time) (*http.Response, error) {
	errs := make([]error, 0, len(ips))
	for i, ip := range ips {
		resp, connected, err := doPinnedImageRequest(ctx, client, target, ip, imageAttemptBudget(deadline, len(ips)-i))
		if err == nil {
			return resp, nil
		}
		errs = append(errs, err)
		if connected || ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}

func doPinnedImageRequest(ctx context.Context, client *http.Client, target *url.URL, ip net.IP, budget time.Duration) (*http.Response, bool, error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	var connected atomic.Bool
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(attemptCtx, trace), http.MethodGet, target.String(), nil)
	if err != nil {
		cancel()
		return nil, true, err
	}
	pinImageRequest(req, target, ip)
	timer := time.AfterFunc(budget, func() {
		if !connected.Load() {
			cancel()
		}
	})
	resp, err := client.Do(req)
	timer.Stop()
	if err != nil {
		cancel()
		return nil, connected.Load(), err
	}
	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, true, nil
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func isImageRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

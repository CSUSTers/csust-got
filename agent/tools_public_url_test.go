package agentv3

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/config"

	"github.com/stretchr/testify/require"
)

func TestIsPublicIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{ip: "127.0.0.1"}, {ip: "10.1.2.3"}, {ip: "172.16.0.9"}, {ip: "192.168.1.1"}, {ip: "169.254.169.254"},
		{ip: "0.0.0.0"}, {ip: "100.64.0.1"}, {ip: "224.0.0.1"}, {ip: "255.255.255.255"}, {ip: "240.0.0.1"},
		{ip: "192.0.2.1"}, {ip: "198.18.0.1"},
		{ip: "::1"}, {ip: "fc00::1"}, {ip: "fd12::1"}, {ip: "fe80::1"}, {ip: "ff02::1"}, {ip: "::"}, {ip: "2001:db8::1"},
		{ip: "::ffff:127.0.0.1"}, {ip: "::ffff:10.0.0.1"}, {ip: "64:ff9b::7f00:1"},
		{ip: "93.184.216.34", want: true}, {ip: "8.8.8.8", want: true}, {ip: "2606:4700::1111", want: true}, {ip: "64:ff9b::808:808", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			require.Equal(t, tt.want, isPublicIP(net.ParseIP(tt.ip)))
		})
	}
	require.False(t, isPublicIP(nil))
}

// allowLoopbackImageTargets disables the public-address policy so tests can download from httptest servers.
func allowLoopbackImageTargets(t *testing.T) {
	t.Helper()
	old := checkImageTarget
	checkImageTarget = func(net.IP, string) error { return nil }
	t.Cleanup(func() { checkImageTarget = old })
}

func TestAllowLoopbackImageTargetsRestoresPolicy(t *testing.T) {
	t.Run("relaxed", func(t *testing.T) {
		allowLoopbackImageTargets(t)
		_, err := validatePublicImageURL(t.Context(), "http://127.0.0.1:8080/a.jpg")
		require.NoError(t, err)
	})
	_, err := validatePublicImageURL(t.Context(), "http://127.0.0.1:8080/a.jpg")
	require.ErrorIs(t, err, errImageURLPort)
}

func stubPublicImageResolver(t *testing.T, hosts map[string][]string) {
	t.Helper()
	old := lookupPublicImageHost
	lookupPublicImageHost = func(_ context.Context, host string) ([]net.IP, error) {
		addrs, ok := hosts[host]
		if !ok {
			return nil, errImageURLResolve
		}
		ips := make([]net.IP, 0, len(addrs))
		for _, addr := range addrs {
			ips = append(ips, net.ParseIP(addr))
		}
		return ips, nil
	}
	t.Cleanup(func() { lookupPublicImageHost = old })
}

func TestValidatePublicImageURL(t *testing.T) {
	stubPublicImageResolver(t, map[string][]string{
		"public.test":   {"93.184.216.34"},
		"internal.test": {"10.0.0.5"},
		"mixed.test":    {"93.184.216.34", "127.0.0.1"},
	})
	tests := []struct {
		name    string
		url     string
		wantErr error
	}{
		{name: "public host", url: "https://public.test/image.png"},
		{name: "public literal", url: "http://93.184.216.34/a.jpg"},
		{name: "standard port", url: "https://public.test:443/a.jpg"},
		{name: "ftp", url: "ftp://public.test/a.jpg", wantErr: errImageURLScheme},
		{name: "file", url: "file:///etc/passwd", wantErr: errImageURLScheme},
		{name: "no host", url: "http:///a.jpg", wantErr: errImageURLHost},
		{name: "unusual port", url: "http://public.test:8080/a.jpg", wantErr: errImageURLPort},
		{name: "loopback literal", url: "http://127.0.0.1/a.jpg", wantErr: errImageURLNotPublic},
		{name: "ipv6 loopback", url: "http://[::1]/a.jpg", wantErr: errImageURLNotPublic},
		{name: "ula literal", url: "http://[fd00::1]/a.jpg", wantErr: errImageURLNotPublic},
		{name: "private host", url: "https://internal.test/a.jpg", wantErr: errImageURLNotPublic},
		{name: "mixed answers", url: "https://mixed.test/a.jpg", wantErr: errImageURLNotPublic},
		{name: "unresolvable", url: "https://nowhere.test/a.jpg", wantErr: errImageURLResolve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validatePublicImageURL(t.Context(), tt.url)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			require.True(t, isImageURLPolicyError(err))
			msg, ok := recoverableImageToolMessage(err)
			require.True(t, ok)
			require.Contains(t, msg, "公网")
		})
	}
}

func TestDownloadImageRejectsLocalServer(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	t.Cleanup(server.Close)

	_, err := downloadImage(t.Context(), &TurnContext{}, "", server.URL+"/image.png")
	require.Error(t, err)
	require.True(t, isImageURLPolicyError(err), err)
	require.Zero(t, hits.Load(), "loopback server must never be contacted")

	_, err = (&analyzeImageTool{}).InvokableRun(WithTurnContext(t.Context(), &TurnContext{Config: &config.AgentConfig{}}), `{"url":"`+server.URL+`/image.png"}`)
	require.NoError(t, err)
	require.Zero(t, hits.Load())
}

func TestPublicImageHTTPClientBlocksLoopbackDial(t *testing.T) {
	stubPublicImageResolver(t, map[string][]string{"rebind.test": {"127.0.0.1"}})
	client := newPublicImageHTTPClient(nil, "", time.Now().Add(time.Minute))
	transport := client.Transport.(*http.Transport)
	for _, addr := range []string{"127.0.0.1:80", "[::1]:443", "rebind.test:80"} {
		conn, dialErr := transport.DialContext(t.Context(), "tcp", addr)
		require.Nil(t, conn)
		require.True(t, errors.Is(dialErr, errImageURLDialTarget) || errors.Is(dialErr, errImageURLResolve), dialErr)
	}
	require.ErrorIs(t, client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}

// allowLoopbackOnly admits loopback literals for httptest servers while keeping the policy for everything else.
func allowLoopbackOnly(t *testing.T) {
	t.Helper()
	old := checkImageTarget
	checkImageTarget = func(ip net.IP, port string) error {
		if ip != nil && ip.IsLoopback() {
			return nil
		}
		if port != "" && ip == nil {
			return nil
		}
		return defaultImageTargetCheck(ip, port)
	}
	t.Cleanup(func() { checkImageTarget = old })
}

func TestFetchPublicImageURLDirectRedirects(t *testing.T) {
	allowLoopbackOnly(t)
	stubPublicImageResolver(t, map[string][]string{"internal.test": {"10.0.0.5"}})
	var hits atomic.Int32
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/image.png", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	})
	mux.HandleFunc("/relative", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/image.png", http.StatusFound)
	})
	mux.HandleFunc("/private", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://internal.test/a.jpg", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusMovedPermanently)
	})

	resp, err := fetchPublicImageURL(t.Context(), server.URL+"/relative")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.EqualValues(t, 1, hits.Load())

	_, err = fetchPublicImageURL(t.Context(), server.URL+"/private")
	require.ErrorIs(t, err, errImageURLNotPublic)

	_, err = fetchPublicImageURL(t.Context(), server.URL+"/loop")
	require.ErrorIs(t, err, errImageURLRedirects)
}

// recordingProxy is a CONNECT-only HTTP proxy that records tunnel targets and the Host of tunnelled requests.
type recordingProxy struct {
	mu          sync.Mutex
	targets     []string
	hosts       []string
	connects    int32
	denyConnect bool
	deny        map[string]bool
	origin      http.Handler
}

func (p *recordingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.connects++
	p.targets = append(p.targets, r.Host)
	p.mu.Unlock()
	if r.Method != http.MethodConnect {
		http.Error(w, "absolute-form requests leak the hostname", http.StatusBadRequest)
		return
	}
	if p.denyConnect || p.deny[r.Host] {
		http.Error(w, "CONNECT denied", http.StatusForbidden)
		return
	}
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	serveTunnelledRequest(conn, buf.Reader, p.origin, func(host string) {
		p.mu.Lock()
		p.hosts = append(p.hosts, host)
		p.mu.Unlock()
	})
}

func serveTunnelledRequest(conn net.Conn, reader *bufio.Reader, origin http.Handler, record func(host string)) {
	inner, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	record(inner.Host)
	rec := httptest.NewRecorder()
	origin.ServeHTTP(rec, inner)
	_ = rec.Result().Write(conn)
}

func (p *recordingProxy) snapshot() (targets, hosts []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...), append([]string(nil), p.hosts...)
}

func (p *recordingProxy) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.targets, p.hosts = nil, nil
}

func withImageProxy(t *testing.T, proxyURL string) {
	t.Helper()
	old := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	config.BotConfig.Proxy = proxyURL
	t.Cleanup(func() { config.BotConfig = old })
}

func imageOriginHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/image.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	})
	mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://cdn.test/image.png", http.StatusFound)
	})
	mux.HandleFunc("/private", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://internal.test/a.jpg", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	return mux
}

func stubProxyImageHosts(t *testing.T) {
	t.Helper()
	stubPublicImageResolver(t, map[string][]string{
		"public.test":   {"93.184.216.34"},
		"cdn.test":      {"2606:4700::1111"},
		"internal.test": {"10.0.0.5"},
	})
}

func TestFetchPublicImageURLThroughHTTPProxyTunnelsToValidatedIP(t *testing.T) {
	stubProxyImageHosts(t)
	proxy := &recordingProxy{origin: imageOriginHandler()}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	withImageProxy(t, server.URL)

	resp, err := fetchPublicImageURL(t.Context(), "http://public.test/hop")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	targets, hosts := proxy.snapshot()
	require.Equal(t, []string{"93.184.216.34:80", "[2606:4700::1111]:80"}, targets, "CONNECT only ever carries validated IPs")
	require.Equal(t, []string{"public.test", "cdn.test"}, hosts, "the origin still receives the hostname")

	proxy.reset()
	_, err = fetchPublicImageURL(t.Context(), "http://public.test/private")
	require.ErrorIs(t, err, errImageURLNotPublic)
	targets, _ = proxy.snapshot()
	require.Equal(t, []string{"93.184.216.34:80"}, targets, "the private redirect target is never sent to the proxy")

	proxy.reset()
	_, err = fetchPublicImageURL(t.Context(), "http://public.test/loop")
	require.ErrorIs(t, err, errImageURLRedirects)
	targets, _ = proxy.snapshot()
	require.Len(t, targets, agentImageURLMaxRedirects+1)

	proxy.reset()
	_, err = fetchPublicImageURL(t.Context(), "http://internal.test/a.jpg")
	require.ErrorIs(t, err, errImageURLNotPublic)
	targets, _ = proxy.snapshot()
	require.Empty(t, targets)

	_, err = fetchPublicImageURL(t.Context(), "https://public.test/image.png")
	require.Error(t, err, "the fake origin does not speak TLS")
	targets, _ = proxy.snapshot()
	require.Equal(t, []string{"93.184.216.34:443"}, targets, "https tunnels use the validated IP and port")
}

func TestFetchPublicImageURLThroughHTTPProxyRejectsDeniedTunnel(t *testing.T) {
	stubProxyImageHosts(t)
	proxy := &recordingProxy{origin: imageOriginHandler(), denyConnect: true}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	withImageProxy(t, server.URL)

	_, err := fetchPublicImageURL(t.Context(), "http://public.test/image.png")
	require.ErrorIs(t, err, errImageURLProxyConnect)
}

func TestDialThroughHTTPProxySendsBasicAuth(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Proxy-Authorization")
		http.Error(w, "nope", http.StatusProxyAuthRequired)
	}))
	t.Cleanup(server.Close)
	proxyURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	proxyURL.User = url.UserPassword("user", "pa:ss")

	_, err = dialThroughHTTPProxy(t.Context(), &net.Dialer{}, proxyURL, "93.184.216.34:443")
	require.ErrorIs(t, err, errImageURLProxyConnect)
	require.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pa:ss")), authHeader)
}

// fakeSocks5 accepts one SOCKS5 CONNECT per connection, records the requested address and serves the origin.
type fakeSocks5 struct {
	mu       sync.Mutex
	requests []string
	hosts    []string
	fail     map[string]bool
	origin   http.Handler
}

func startFakeSocks5(t *testing.T, origin http.Handler) *fakeSocks5 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	s := &fakeSocks5{origin: origin}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	withImageProxy(t, "socks5://"+ln.Addr().String())
	return s
}

func (s *fakeSocks5) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(reader, greeting); err != nil || greeting[0] != 5 {
		return
	}
	if _, err := io.ReadFull(reader, make([]byte, int(greeting[1]))); err != nil {
		return
	}
	_, _ = conn.Write([]byte{5, 0})
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil || header[1] != 1 {
		return
	}
	var addr string
	switch header[3] {
	case 1:
		buf := make([]byte, 4)
		_, _ = io.ReadFull(reader, buf)
		addr = net.IP(buf).String()
	case 4:
		buf := make([]byte, 16)
		_, _ = io.ReadFull(reader, buf)
		addr = "[" + net.IP(buf).String() + "]"
	case 3:
		n, _ := reader.ReadByte()
		buf := make([]byte, int(n))
		_, _ = io.ReadFull(reader, buf)
		addr = "domain:" + string(buf)
	}
	port := make([]byte, 2)
	_, _ = io.ReadFull(reader, port)
	s.mu.Lock()
	request := fmt.Sprintf("%s:%d", addr, int(port[0])<<8|int(port[1]))
	s.requests = append(s.requests, request)
	fail := s.fail[request]
	s.mu.Unlock()
	if fail {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	serveTunnelledRequest(conn, reader, s.origin, func(host string) {
		s.mu.Lock()
		s.hosts = append(s.hosts, host)
		s.mu.Unlock()
	})
}

func (s *fakeSocks5) snapshot() (requests, hosts []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...), append([]string(nil), s.hosts...)
}

func TestFetchPublicImageURLThroughSocksPinsValidatedIP(t *testing.T) {
	stubProxyImageHosts(t)
	socks := startFakeSocks5(t, imageOriginHandler())

	resp, err := fetchPublicImageURL(t.Context(), "http://public.test/hop")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	requests, hosts := socks.snapshot()
	require.Equal(t, []string{"93.184.216.34:80", "[2606:4700::1111]:80"}, requests, "SOCKS receives IP addresses, never domain names")
	require.Equal(t, []string{"public.test", "cdn.test"}, hosts, "Host header keeps the original hostname")

	_, err = fetchPublicImageURL(t.Context(), "http://public.test/private")
	require.ErrorIs(t, err, errImageURLNotPublic)
	requests, _ = socks.snapshot()
	require.Len(t, requests, 3, "the private redirect target is never sent to the proxy")
}

func TestImageDownloadProxyRejectsUnsupportedScheme(t *testing.T) {
	withImageProxy(t, "ftp://proxy.test:21")

	_, err := fetchPublicImageURL(t.Context(), "https://public.test/image.png")
	require.ErrorIs(t, err, errImageURLDialTarget)
	require.True(t, isImageURLPolicyError(err))

	config.BotConfig.Proxy = ""
	proxyURL, err := imageDownloadProxy()
	require.NoError(t, err)
	require.Nil(t, proxyURL)
}

func TestPinImageRequest(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		ip       string
		wantURL  string
		wantHost string
	}{
		{name: "ipv4", target: "http://public.test/a.jpg?x=1", ip: "93.184.216.34", wantURL: "http://93.184.216.34/a.jpg?x=1", wantHost: "public.test"},
		{name: "ipv4 port", target: "https://public.test:443/a.jpg", ip: "93.184.216.34", wantURL: "https://93.184.216.34:443/a.jpg", wantHost: "public.test:443"},
		{name: "ipv6", target: "https://cdn.test/a%20b.jpg", ip: "2606:4700::1111", wantURL: "https://[2606:4700::1111]/a%20b.jpg", wantHost: "cdn.test"},
		{name: "ipv6 port", target: "http://cdn.test:80/a.jpg", ip: "2606:4700::1111", wantURL: "http://[2606:4700::1111]:80/a.jpg", wantHost: "cdn.test:80"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := url.Parse(tt.target)
			require.NoError(t, err)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tt.target, nil)
			require.NoError(t, err)
			pinImageRequest(req, target, net.ParseIP(tt.ip))
			require.Equal(t, tt.wantURL, req.URL.String())
			require.Equal(t, tt.wantHost, req.Host)
		})
	}
}

func TestImageAttemptBudget(t *testing.T) {
	tests := []struct {
		name      string
		left      time.Duration
		remaining int
		wantMin   time.Duration
		wantMax   time.Duration
	}{
		{name: "split evenly", left: 30 * time.Second, remaining: 3, wantMin: 9 * time.Second, wantMax: 10 * time.Second},
		{name: "last candidate gets the rest", left: 30 * time.Second, remaining: 1, wantMin: 29 * time.Second, wantMax: 30 * time.Second},
		{name: "floor", left: 3 * time.Second, remaining: 4, wantMin: agentImageAttemptMinBudget, wantMax: agentImageAttemptMinBudget},
		{name: "expired", left: -time.Second, remaining: 2, wantMin: agentImageAttemptMinBudget, wantMax: agentImageAttemptMinBudget},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := imageAttemptBudget(time.Now().Add(tt.left), tt.remaining)
			require.GreaterOrEqual(t, got, tt.wantMin)
			require.LessOrEqual(t, got, tt.wantMax)
		})
	}
}

var errTestDialRefused = errors.New("refused")

func TestDialImageCandidates(t *testing.T) {
	refused := errTestDialRefused
	tests := []struct {
		name      string
		failures  map[string]error
		wantDials []string
		wantErr   error
	}{
		{name: "first succeeds", wantDials: []string{"a:80"}},
		{name: "falls back in order", failures: map[string]error{"a:80": refused, "b:80": refused}, wantDials: []string{"a:80", "b:80", "c:80"}},
		{name: "all fail", failures: map[string]error{"a:80": refused, "b:80": refused, "c:80": refused}, wantDials: []string{"a:80", "b:80", "c:80"}, wantErr: refused},
		{name: "unusable proxy stops", failures: map[string]error{"a:80": fmt.Errorf("%w: down", errImageProxyUnusable)}, wantDials: []string{"a:80"}, wantErr: errImageProxyUnusable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dials []string
			conn, err := dialImageCandidates(t.Context(), time.Now().Add(time.Minute), []string{"a:80", "b:80", "c:80"},
				func(ctx context.Context, target string) (net.Conn, error) {
					_, ok := ctx.Deadline()
					require.True(t, ok, "every attempt has its own deadline")
					dials = append(dials, target)
					if err := tt.failures[target]; err != nil {
						return nil, err
					}
					client, server := net.Pipe()
					_ = server.Close()
					return client, nil
				})
			require.Equal(t, tt.wantDials, dials)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Nil(t, conn)
				return
			}
			require.NoError(t, err)
			_ = conn.Close()
		})
	}
}

func TestFetchPublicImageURLDirectFallsBackToNextAddress(t *testing.T) {
	allowLoopbackOnly(t)
	var served []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		served = append(served, r.Context().Value(http.LocalAddrContextKey).(net.Addr).String())
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, closedPort, err := net.SplitHostPort(closed.Addr().String())
	require.NoError(t, err)
	require.NoError(t, closed.Close())
	stubPublicImageResolver(t, map[string][]string{"multi.test": {"::1", "127.0.0.1"}})

	resp, err := fetchPublicImageURL(t.Context(), "http://multi.test:"+port+"/image.png")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, []string{"127.0.0.1:" + port}, served, "the second validated address served the image")

	_, err = fetchPublicImageURL(t.Context(), "http://multi.test:"+closedPort+"/image.png")
	require.Error(t, err)
	require.Contains(t, err.Error(), "[::1]:"+closedPort)
	require.Contains(t, err.Error(), "127.0.0.1:"+closedPort)
}

func stubMultiAddressImageHost(t *testing.T) {
	t.Helper()
	stubPublicImageResolver(t, map[string][]string{"multi.test": {"93.184.216.34", "2606:4700::1111"}})
}

func requireOnlyIPTargets(t *testing.T, targets []string) {
	t.Helper()
	for _, target := range targets {
		require.NotContains(t, target, "multi.test", "the hostname is never sent to the proxy")
		host, _, err := net.SplitHostPort(strings.TrimPrefix(target, "domain:"))
		require.NoError(t, err)
		require.NotNil(t, net.ParseIP(host), target)
	}
}

func TestFetchPublicImageURLThroughHTTPProxyFallsBackToNextAddress(t *testing.T) {
	stubMultiAddressImageHost(t)
	proxy := &recordingProxy{origin: imageOriginHandler(), deny: map[string]bool{"93.184.216.34:80": true}}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	withImageProxy(t, server.URL)

	resp, err := fetchPublicImageURL(t.Context(), "http://multi.test/image.png")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	targets, hosts := proxy.snapshot()
	require.Equal(t, []string{"93.184.216.34:80", "[2606:4700::1111]:80"}, targets)
	require.Equal(t, []string{"multi.test"}, hosts)
}

func TestFetchPublicImageURLThroughHTTPProxyAllAddressesDenied(t *testing.T) {
	stubMultiAddressImageHost(t)
	proxy := &recordingProxy{origin: imageOriginHandler(), denyConnect: true}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	withImageProxy(t, server.URL)

	_, err := fetchPublicImageURL(t.Context(), "http://multi.test/image.png")
	require.ErrorIs(t, err, errImageURLProxyConnect)
	targets, hosts := proxy.snapshot()
	require.Equal(t, []string{"93.184.216.34:80", "[2606:4700::1111]:80"}, targets)
	require.Empty(t, hosts)
	requireOnlyIPTargets(t, targets)
}

func TestFetchPublicImageURLThroughSocksFallsBackToNextAddress(t *testing.T) {
	stubMultiAddressImageHost(t)
	origin := imageOriginHandler().(*http.ServeMux)
	origin.HandleFunc("/broken.png", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	socks := startFakeSocks5(t, origin)
	socks.mu.Lock()
	socks.fail = map[string]bool{"93.184.216.34:80": true}
	socks.mu.Unlock()

	resp, err := fetchPublicImageURL(t.Context(), "http://multi.test/image.png")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	requests, hosts := socks.snapshot()
	require.Equal(t, []string{"93.184.216.34:80", "[2606:4700::1111]:80"}, requests)
	require.Equal(t, []string{"multi.test"}, hosts)

	socks.mu.Lock()
	socks.fail, socks.requests, socks.hosts = nil, nil, nil
	socks.mu.Unlock()
	resp, err = fetchPublicImageURL(t.Context(), "http://multi.test/broken.png")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	requests, _ = socks.snapshot()
	require.Equal(t, []string{"93.184.216.34:80"}, requests, "HTTP responses are never retried on another address")
}

func TestFetchPublicImageURLThroughSocksAllAddressesFail(t *testing.T) {
	stubMultiAddressImageHost(t)
	socks := startFakeSocks5(t, imageOriginHandler())
	socks.mu.Lock()
	socks.fail = map[string]bool{"93.184.216.34:80": true, "[2606:4700::1111]:80": true}
	socks.mu.Unlock()

	_, err := fetchPublicImageURL(t.Context(), "http://multi.test/image.png")
	require.Error(t, err)
	requests, hosts := socks.snapshot()
	require.Equal(t, []string{"93.184.216.34:80", "[2606:4700::1111]:80"}, requests)
	require.Empty(t, hosts)
	requireOnlyIPTargets(t, requests)
}

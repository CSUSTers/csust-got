package agentv3

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"csust-got/config"
)

var (
	errImageURLScheme     = errors.New("image url must use http or https")
	errImageURLHost       = errors.New("image url has no host")
	errImageURLPort       = errors.New("image url port must be 80 or 443")
	errImageURLResolve    = errors.New("image url host does not resolve")
	errImageURLNotPublic  = errors.New("image url resolves to a non-public address")
	errImageURLRedirects  = errors.New("image url has too many redirects")
	errImageURLDialTarget = errors.New("image download dial target is not a public address")
)

const (
	agentImageURLMaxRedirects  = 5
	agentImageURLResolveWindow = 5 * time.Second
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
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, errImageURLScheme
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, errImageURLHost
	}
	if err := checkImageTarget(nil, parsed.Port()); err != nil {
		return nil, err
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if err := checkImageTarget(ip, ""); err != nil {
			return nil, err
		}
		return parsed, nil
	}
	ips, err := lookupPublicImageHost(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("%w: %s", errImageURLResolve, host)
	}
	for _, ip := range ips {
		if err := checkImageTarget(ip, ""); err != nil {
			return nil, fmt.Errorf("%w (%s)", err, host)
		}
	}
	return parsed, nil
}

// newPublicImageHTTPClient builds a client whose dials and redirects may only reach public addresses.
// With a bot proxy configured the proxy performs the connection, so only URL validation applies.
func newPublicImageHTTPClient() (*http.Client, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
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
			for _, ip := range ips {
				if err := checkImageTarget(ip, ""); err != nil {
					return nil, fmt.Errorf("%w: %w", errImageURLDialTarget, err)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if config.BotConfig != nil && config.BotConfig.Proxy != "" {
		proxyURL, err := url.Parse(config.BotConfig.Proxy)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
		transport.DialContext = dialer.DialContext
	}
	return &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= agentImageURLMaxRedirects {
				return errImageURLRedirects
			}
			_, err := validatePublicImageURL(req.Context(), req.URL.String())
			return err
		},
	}, nil
}

// fetchPublicImageURL downloads an image from a public http(s) URL.
func fetchPublicImageURL(ctx context.Context, rawURL string) (*http.Response, error) {
	parsed, err := validatePublicImageURL(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	client, err := newPublicImageHTTPClient()
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

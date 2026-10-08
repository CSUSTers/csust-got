package agentv3

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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

func TestPublicImageHTTPClientBlocksLoopbackDialAndRedirect(t *testing.T) {
	stubPublicImageResolver(t, map[string][]string{"rebind.test": {"127.0.0.1"}})
	old := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	t.Cleanup(func() { config.BotConfig = old })

	client, err := newPublicImageHTTPClient()
	require.NoError(t, err)
	transport := client.Transport.(*http.Transport)
	for _, addr := range []string{"127.0.0.1:80", "[::1]:443", "rebind.test:80"} {
		conn, dialErr := transport.DialContext(t.Context(), "tcp", addr)
		require.Nil(t, conn)
		require.True(t, errors.Is(dialErr, errImageURLDialTarget) || errors.Is(dialErr, errImageURLResolve), dialErr)
	}

	redirect, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://10.0.0.1/a.jpg", nil)
	require.NoError(t, err)
	require.ErrorIs(t, client.CheckRedirect(redirect, nil), errImageURLNotPublic)
	require.ErrorIs(t, client.CheckRedirect(redirect, make([]*http.Request, agentImageURLMaxRedirects)), errImageURLRedirects)
}

package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// guardTransport checks resolved destination addresses even when a deployment
// has an outbound proxy. Only actual proxy endpoints and explicitly configured
// image services may use private addresses; NO_PROXY and direct fallback
// requests still need the connection-level check against DNS rebinding.
func (p *ImageProxy) guardTransport(transport *http.Transport) {
	var proxyEndpoints sync.Map
	if selectProxy := transport.Proxy; selectProxy != nil {
		transport.Proxy = func(req *http.Request) (*url.URL, error) {
			proxyURL, err := selectProxy(req)
			if err == nil && proxyURL != nil {
				port := proxyURL.Port()
				if port == "" {
					switch strings.ToLower(proxyURL.Scheme) {
					case "https":
						port = "443"
					case "socks5", "socks5h":
						port = "1080"
					default:
						port = "80"
					}
				}
				proxyEndpoints.Store(net.JoinHostPort(strings.ToLower(proxyURL.Hostname()), port), true)
			}
			return proxyURL, err
		}
	}
	guardDialer := &net.Dialer{
		Timeout: 15 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || isPrivateIP(ip) {
				return errors.New("image proxy: requests to private/internal hosts are not allowed")
			}
			return nil
		},
	}
	trustedDialer := &net.Dialer{Timeout: 15 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		_, isProxyEndpoint := proxyEndpoints.Load(strings.ToLower(address))
		if isProxyEndpoint || p.isAllowedRemoteHost(host) {
			return trustedDialer.DialContext(ctx, network, address)
		}
		return guardDialer.DialContext(ctx, network, address)
	}
}

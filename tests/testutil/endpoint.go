package testutil

import (
	"net"
	"net/url"
)

// LoopbackIPEndpoint rewrites testcontainer endpoints that use localhost to
// 127.0.0.1 so plaintext endpoint validation still receives an IP literal.
func LoopbackIPEndpoint(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		u.Host = loopbackIPHostPort(u.Host)
		return u.String()
	}
	return loopbackIPHostPort(endpoint)
}

func loopbackIPHostPort(hostport string) string {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		if hostport == "localhost" {
			return "127.0.0.1"
		}
		return hostport
	}
	if host == "localhost" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return hostport
}

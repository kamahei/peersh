package main

import (
	"net"
	"testing"
)

func TestIsLoopbackBind(t *testing.T) {
	cases := []struct {
		name string
		addr *net.UDPAddr
		want bool
	}{
		{"nil addr", nil, false},
		{"nil IP", &net.UDPAddr{IP: nil, Port: 7777}, false},
		{"unspecified v4", &net.UDPAddr{IP: net.IPv4zero, Port: 7777}, false},
		{"unspecified v6", &net.UDPAddr{IP: net.IPv6unspecified, Port: 7777}, false},
		{"loopback v4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7777}, true},
		{"loopback v6", &net.UDPAddr{IP: net.IPv6loopback, Port: 7777}, true},
		{"public v4", &net.UDPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 7777}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isLoopbackBind(c.addr); got != c.want {
				t.Fatalf("isLoopbackBind(%v) = %v, want %v", c.addr, got, c.want)
			}
		})
	}
}

func TestEffectiveListen(t *testing.T) {
	cases := []struct {
		name           string
		listen         string
		signalingURL   string
		listenExplicit bool
		want           string
	}{
		{
			name:   "default direct stays loopback",
			listen: defaultDirectListen,
			want:   defaultDirectListen,
		},
		{
			name:         "default signaling binds externally",
			listen:       defaultDirectListen,
			signalingURL: "wss://example.com/ws",
			want:         defaultSignalingListen,
		},
		{
			name:           "explicit loopback with signaling is respected",
			listen:         defaultDirectListen,
			signalingURL:   "wss://example.com/ws",
			listenExplicit: true,
			want:           defaultDirectListen,
		},
		{
			name:           "explicit non-loopback is respected",
			listen:         ":9999",
			signalingURL:   "wss://example.com/ws",
			listenExplicit: true,
			want:           ":9999",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveListen(c.listen, c.signalingURL, c.listenExplicit); got != c.want {
				t.Fatalf("effectiveListen() = %q, want %q", got, c.want)
			}
		})
	}
}

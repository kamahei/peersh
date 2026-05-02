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

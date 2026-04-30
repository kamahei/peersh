module github.com/peersh/peersh/server

go 1.24

replace github.com/peersh/peersh/core => ../core

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/pion/dtls/v2 v2.2.7 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pion/stun/v2 v2.0.0 // indirect
	github.com/pion/transport/v2 v2.2.1 // indirect
	github.com/pion/transport/v3 v3.0.1 // indirect
	golang.org/x/crypto v0.12.0 // indirect
	golang.org/x/sys v0.11.0 // indirect
	nhooyr.io/websocket v1.8.17 // indirect
)

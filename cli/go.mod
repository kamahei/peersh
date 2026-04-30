module github.com/peersh/peersh/cli

go 1.25.0

require github.com/peersh/peersh/core v0.0.0

require (
	github.com/pion/dtls/v2 v2.2.7 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pion/stun/v2 v2.0.0 // indirect
	github.com/pion/transport/v2 v2.2.1 // indirect
	github.com/pion/transport/v3 v3.0.1 // indirect
	github.com/quic-go/quic-go v0.59.0 // indirect
	golang.org/x/crypto v0.41.0 // indirect
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	nhooyr.io/websocket v1.8.17 // indirect
)

replace github.com/peersh/peersh/core => ../core

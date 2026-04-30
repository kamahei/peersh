module github.com/peersh/peersh/cli

go 1.24

require github.com/peersh/peersh/core v0.0.0

require (
	github.com/quic-go/quic-go v0.59.0 // indirect
	golang.org/x/crypto v0.41.0 // indirect
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/peersh/peersh/core => ../core

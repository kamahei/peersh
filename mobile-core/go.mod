module github.com/peersh/peersh/mobile-core

go 1.25.0

require github.com/peersh/peersh/core v0.0.0

require (
	github.com/quic-go/quic-go v0.59.0 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/mobile v0.0.0-20260410095206-2cfb76559b7b // indirect
	golang.org/x/mod v0.35.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/tools v0.44.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/peersh/peersh/core => ../core

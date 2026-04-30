module github.com/peersh/peersh/server

go 1.24

replace github.com/peersh/peersh/core => ../core

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	nhooyr.io/websocket v1.8.17 // indirect
)

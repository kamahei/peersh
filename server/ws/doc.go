// Package ws hosts the WebSocket upgrade and per-connection frame loop.
//
// One goroutine per WebSocket connection. The state machine is
// Hello → Register → ready, after which the connection accepts Connect
// messages routed through package room.
package ws

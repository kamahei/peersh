// Command peersh-signaling is the peersh signaling server.
//
// Phase 2: WebSocket-based, deployable as a single binary or Docker image.
// Subcommands:
//
//	peersh-signaling serve --config /etc/peersh/signaling.toml
//	peersh-signaling psk add --user <id> [--label <text>]
//	peersh-signaling psk list
//	peersh-signaling psk revoke --user <id>
//
// The server is connection-setup-only — it never sees PowerShell command
// content. All command bytes flow peer-to-peer over QUIC.
package main

func main() {
	// Implementation lands in P2-T11 (psk subcommands) and P2-T12 (serve).
}

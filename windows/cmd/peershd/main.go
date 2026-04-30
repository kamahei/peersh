// Command peershd is the peersh Windows host. It listens for QUIC connections
// from peersh clients and forwards them to a PowerShell session on this
// machine.
//
// Phase 1: console app (no Windows Service registration), no auth, no
// signaling, direct LAN connections only.
package main

func main() {
	// Implementation lands in P1-T07.
}

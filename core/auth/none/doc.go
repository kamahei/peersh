// Package none implements auth.Provider as a permissive no-op. It is used in
// Phase 1 and in development / LAN-only deployments. It must not be selected
// for any deployment that authenticates users.
package none

// Package firebase implements auth.Provider on top of Firebase Auth ID
// tokens.
//
// The official hosted peersh server (Phase 5+) uses this provider; PSK
// remains the recommended path for self-hosting. Operators bring their
// own Firebase project and provide either Application Default Credentials
// or a service-account JSON; see docs/firebase-setup.md.
//
// Phase 5 ships the provider as a standalone package without changing
// `core/auth/`'s public interface; callers select between providers at
// server startup.
package firebase

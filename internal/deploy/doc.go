// Package deploy coordinates deployment locks, resumable journals, and hooks.
//
// The VM stores deploy.lock/owner.json and deploy.state below /opt/<app>.
// Hold the lock while writing journal events or changing release state.
// Refresh the lock before its TTL expires. Release checks the owner record.
// ForceUnlock is an explicit recovery operation that bypasses ownership checks.
//
// Integrations register named hooks during initialization. Hooks receive the same
// remote connection and environment as the deployer. Hooks must not log secrets.
package deploy

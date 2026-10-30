// Package compose renders the Compose files of the sidecar services (ADR 0022 §2):
// compose.services.yml for a release, and .spinup/compose.services.yml for spinup dev.
// The renderers are pure functions of the services, the app, and the environment.
//
// Stub from the services pre-seed. Unit S1 replaces it.
package compose

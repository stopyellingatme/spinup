// Package auditwired is the doctor check audit-wired (ADR 0025 decision 13.4). It
// warns when the audit module is on and api/cmd/api/main.go lacks the audit
// wiring, and when the table user_identities exists without the audit trigger.
package auditwired

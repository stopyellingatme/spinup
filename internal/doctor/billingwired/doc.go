// Package billingwired is the doctor check billing-wired (ADR 0032 §15.6). The check
// parses the API and worker main files and the production Compose file, and reports
// the first missing billing wiring. With the audit module, it also checks the audit
// triggers on the billing tables.
package billingwired

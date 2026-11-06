package modules

// AuditNextSteps returns the manual wiring steps for an existing app with audit.
func AuditNextSteps() string {
	return `Next: in api/cmd/api/main.go, import:
 "github.com/stopyellingatme/spinup/kit/audit"
 "github.com/stopyellingatme/spinup/kit/audit/authsink"
Next: add these options to auth.New(pool, ...):
 auth.WithEvents(authsink.New(pool)),
 auth.WithActorContext(authsink.ActorContext),
Next: add this option to app.New(...):
 app.WithCommand("audit", audit.Command(pool)),
Next: run spinup db migrate up before starting the new binary.
Next: run spinup g audit <resource> for each existing table.
Next: wrap existing writes in audit.InTx and use s.Queries.WithTx(tx).
Next: run spinup doctor and inspect the audit-wired check.
`
}

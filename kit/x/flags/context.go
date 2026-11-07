package flags

import "context"

// scope is the flag state of one request: the provider, one snapshot,
// and the subject. Every evaluation in the request reads this snapshot.
type scope struct {
	provider *Flags
	snapshot *Snapshot
	subject  Subject
}

// scopeKey is the context key of the scope.
type scopeKey struct{}

// Context returns ctx with a scope for s and the current snapshot.
// A job or a command calls it, for example with Subject{Anonymous: true}.
// Later Replace calls do not change the answers in the returned context.
func (f *Flags) Context(ctx context.Context, s Subject) context.Context {
	return f.scope(ctx, f.Snapshot(), s)
}

// scope returns ctx with a scope for snap and s. Middleware reads the
// snapshot pointer once at the start of a request and calls scope, so
// one request sees one snapshot (ADR 0029 decision 6.8). A nil snap
// uses the empty snapshot.
func (f *Flags) scope(ctx context.Context, snap *Snapshot, s Subject) context.Context {
	if snap == nil {
		snap = f.empty
	}
	return context.WithValue(ctx, scopeKey{}, &scope{provider: f, snapshot: snap, subject: s})
}

// scopeFrom returns the scope in ctx, or nil.
func scopeFrom(ctx context.Context) *scope {
	sc, _ := ctx.Value(scopeKey{}).(*scope)
	return sc
}

// Evaluate evaluates key for the scope in ctx. It adds the span event of
// ADR 0029 decision 12.
//
// Install Middleware after the auth middleware, or call Flags.Context in
// a job or a command. Without a scope, Evaluate returns false with the
// variant "off" and ReasonError. It does not panic and does not log.
func Evaluate(ctx context.Context, key string) Resolution {
	r, ok := lookupOverride(key)
	if !ok {
		if sc := scopeFrom(ctx); sc != nil {
			r = resolve(sc.snapshot, key, sc.subject)
		} else {
			r = resolution(false, ReasonError)
		}
	}
	addEvent(ctx, key, r)
	return r
}

// Enabled is Evaluate(ctx, key).Value.
func Enabled(ctx context.Context, key string) bool {
	return Evaluate(ctx, key).Value
}

// SubjectFrom returns the subject of the scope in ctx. ok is false without a scope.
func SubjectFrom(ctx context.Context) (s Subject, ok bool) {
	sc := scopeFrom(ctx)
	if sc == nil {
		return Subject{}, false
	}
	return sc.subject, true
}

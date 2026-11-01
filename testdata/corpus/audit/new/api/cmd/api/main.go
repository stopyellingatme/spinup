package main

func main() {
	authn, err := auth.New(pool, auth.WithSecureCookie(false), auth.WithRoles(roles.Set), auth.WithEvents(authsink.New(pool)), auth.WithActorContext(authsink.ActorContext))
	_ = authn
	_ = err
	_ = app.WithCommand("audit", audit.Command(pool))
}

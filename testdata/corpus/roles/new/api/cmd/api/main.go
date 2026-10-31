package main

func main() {
	authn, err := auth.New(pool, auth.WithSecureCookie(false), auth.WithRoles(roles.Set), auth.WithRegistration(registration))
	_ = authn
	_ = err
}

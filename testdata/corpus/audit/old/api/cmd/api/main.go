package main

func main() {
	authn, err := auth.New(pool, auth.WithSecureCookie(false), auth.WithRoles(roles.Set))
	_ = authn
	_ = err
}

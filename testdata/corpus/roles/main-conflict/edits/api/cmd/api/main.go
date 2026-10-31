package main

func main() {
	authn, err := auth.New(pool, auth.WithSecureCookie(true))
	_ = authn
	_ = err
}

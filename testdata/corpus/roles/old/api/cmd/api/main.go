package main

func main() {
	authn, err := auth.New(pool, auth.WithSecureCookie(false))
	_ = authn
	_ = err
}

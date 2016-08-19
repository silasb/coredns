package main

import "github.com/mholt/caddy/caddy/caddymain"

//go:generate go run plugin_generate.go
func main() {
	caddymain.Run()
}

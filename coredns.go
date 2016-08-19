package main

import (
	"flag"

	"github.com/mholt/caddy/caddy/caddymain"
)

//go:generate go run plugin_generate.go
func main() {
	flag.Set("type", "dns")

	caddymain.Run()
}

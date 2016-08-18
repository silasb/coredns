package main

//go:generate go run plugin_generate.go

import (
	"github.com/mholt/caddy/caddy/caddymain"
)

var run = caddymain.Run

func main() {
	run()
}

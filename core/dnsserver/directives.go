package dnsserver

// Add here, and in core/coredns.go to use them.

// Directives are registered in the order they should be
// executed.
//
// Ordering is VERY important. Every middleware will
// feel the effects of all other middleware below
// (after) them during a request, but they must not
// care what middleware above them are doing.
var Directives = []string{
	"prometheus",
	"bind",
	"health",
	"pprof",

	"errors",
	"log",
	"chaos",
	"cache",

	"rewrite",
	"loadbalance",

	"dnssec",
	"file",
	"secondary",
	"etcd",
	"kubernetes",
	"proxy",
}

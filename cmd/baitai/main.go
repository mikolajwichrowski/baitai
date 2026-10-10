// Command baitai is a terminal AI office: you talk to the boss, the boss runs a team of agents.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mikolajwichrowski/baitai/internal/config"
	"github.com/mikolajwichrowski/baitai/internal/office"
	"github.com/mikolajwichrowski/baitai/internal/tui"
)

func main() {
	state := flag.String("state", ".office", "directory for the office's saved state and settings")
	out := flag.String("output", "output", "directory the agents write their files to")
	flag.Parse()

	if err := os.MkdirAll(*state, 0o755); err != nil {
		fail(err)
	}
	o, err := office.New(*state, *out, config.Load(*state))
	if err != nil {
		fail(err)
	}
	if err := tui.Run(o); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "✖", err)
	os.Exit(1)
}

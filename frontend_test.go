package main

import (
	"os/exec"
	"testing"
)

// The Go tests never execute the frontend, so a syntax error in it would ship
// silently and show up as a blank page. This is the cheapest possible guard.
func TestFrontendParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	for _, f := range []string{"web/app.js", "web/theme.js"} {
		if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse:\n%s", f, out)
		}
	}
}

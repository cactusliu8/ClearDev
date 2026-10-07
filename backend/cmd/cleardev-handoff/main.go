package main

import (
	"fmt"
	"os"

	"github.com/aoagents/agent-orchestrator/backend/internal/devhandoff"
)

func main() {
	if err := devhandoff.Main(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

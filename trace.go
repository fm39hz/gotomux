package main

import (
	"fmt"
	"os"
	"time"
)

// Tracing prints cold-start milestones to stderr when GOTOMUX_TRACE=1.
//
// The plan's latency budgets are per-path (daemon vs standalone) and the two
// paths do very different work, so a single wall-clock number cannot tell you
// which one you got or where it went.
var (
	traceOn    = os.Getenv("GOTOMUX_TRACE") != ""
	traceStart = time.Now()
)

func trace(stage string) {
	if !traceOn {
		return
	}
	fmt.Fprintf(os.Stderr, "trace %-22s %8.2fms\n", stage, float64(time.Since(traceStart).Microseconds())/1000)
}

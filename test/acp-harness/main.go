package main

import (
"bufio"
"context"
"encoding/json"
"flag"
"fmt"
"log/slog"
"net"
"os"
"strings"
"time"
)

func main() {
target := flag.String("target", envOr("ACP_TARGET", "localhost:3000"), "ACP server address (host:port)")
scenarioFlag := flag.String("scenarios", "all", "Comma-separated list of scenarios to run (default: all)")
timeout := flag.Duration("timeout", 30*time.Second, "Per-scenario timeout")
verbose := flag.Bool("verbose", false, "Show all JSON-RPC messages")
flag.Parse()

slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

fmt.Println("=== ACP Validation Report ===")
fmt.Printf("Target: %s\n\n", *target)

allScenarios := AllScenarios()
toRun := selectScenarios(allScenarios, *scenarioFlag)

passed := 0
total := len(toRun)
var results []ScenarioResult

for _, name := range toRun {
fn := allScenarios[name]

// Each scenario gets its own connection.
c, err := dialWithRetry(*target, 5*time.Second)
if err != nil {
results = append(results, ScenarioResult{
Name:    name,
Passed:  false,
Details: fmt.Sprintf("dial failed: %v", err),
})
continue
}
c.verbose = *verbose

ctx, cancel := context.WithTimeout(context.Background(), *timeout)
r := fn(ctx, c)
cancel()
c.Close()

results = append(results, r)
if r.Passed {
passed++
}
}

// Print results.
for _, r := range results {
icon := "✓"
if !r.Passed {
icon = "✗"
}
fmt.Printf("%s %s\n", icon, r.Name)
if r.Details != "" {
fmt.Printf("  %s\n", r.Details)
}
for _, a := range r.Assertions {
if !a.Passed {
fmt.Printf("  ✗ %s: %s\n", a.Name, a.Message)
} else if *verbose {
fmt.Printf("  ✓ %s\n", a.Name)
}
}
}

fmt.Printf("\n=== %d/%d scenarios passed ===\n", passed, total)
if passed < total {
os.Exit(1)
}
}

// selectScenarios returns an ordered list of scenario names to run.
func selectScenarios(all map[string]Scenario, f string) []string {
ordered := []string{
"happy-path",
"multi-turn",
"cancel",
"session-load",
"permission-handling",
"error-handling",
"concurrent-sessions",
"mcp-passthrough",
}
if f == "all" || f == "" {
return ordered
}
requested := strings.Split(f, ",")
var out []string
for _, r := range requested {
r = strings.TrimSpace(r)
if _, ok := all[r]; ok {
out = append(out, r)
} else {
fmt.Fprintf(os.Stderr, "warning: unknown scenario %q\n", r)
}
}
return out
}

// dialWithRetry attempts to connect, retrying until timeout.
func dialWithRetry(addr string, timeout time.Duration) (*Client, error) {
deadline := time.Now().Add(timeout)
var lastErr error
for time.Now().Before(deadline) {
c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
if err == nil {
return &Client{
conn:    c,
scanner: bufio.NewScanner(c),
enc:     json.NewEncoder(c),
}, nil
}
lastErr = err
time.Sleep(100 * time.Millisecond)
}
return nil, fmt.Errorf("could not connect to %s: %v", addr, lastErr)
}

func envOr(key, def string) string {
if v := os.Getenv(key); v != "" {
return v
}
return def
}

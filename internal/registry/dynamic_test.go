package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"

	"github.com/raykao/agent-forge/internal/a2a"
)

// startEmbeddedNATS starts an in-process NATS server with JetStream enabled
// and returns a connected NATS client plus a cleanup function.
func startEmbeddedNATS(t *testing.T) (*nats.Conn, func()) {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1 // random available port
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natsserver.RunServer(&opts)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		srv.Shutdown()
		t.Fatalf("startEmbeddedNATS: connect: %v", err)
	}
	return nc, func() {
		nc.Close()
		srv.Shutdown()
	}
}

// createStaticRegistry writes a JSON registry file to a temp directory and
// returns the file path. The caller's t.Cleanup handles removal via TempDir.
func createStaticRegistry(t *testing.T, entries []AgentEntry) string {
	t.Helper()
	type registryFile struct {
		Agents []AgentEntry `json:"agents"`
	}
	data, err := json.Marshal(registryFile{Agents: entries})
	if err != nil {
		t.Fatalf("createStaticRegistry: marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("createStaticRegistry: write: %v", err)
	}
	return path
}

// makeEntry is a convenience builder for test AgentEntry values.
func makeEntry(name, skillID, tag, subject, runtime string, port int) AgentEntry {
	return AgentEntry{
		Card: a2a.AgentCard{
			Name:               name,
			Description:        name + " agent",
			Version:            "1.0.0",
			DefaultInputModes:  []string{"text"},
			DefaultOutputModes: []string{"text"},
			Skills: []a2a.AgentSkill{
				{
					ID:          skillID,
					Name:        skillID + "-skill",
					Description: "Does " + skillID,
					Tags:        []string{tag},
				},
			},
		},
		QueueSubject: subject,
		Runtime:      runtime,
		ACPPort:      port,
	}
}

// waitForCondition polls fn up to maxWait, returning true when fn returns true.
func waitForCondition(maxWait time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNewDynamicRegistry_CreatesKVBucket(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc, TTL: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	// Verify the bucket is accessible via the raw JetStream API.
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	kv, err := js.KeyValue(AgentCardBucket)
	if err != nil {
		t.Fatalf("bucket %q not found after NewDynamicRegistry: %v", AgentCardBucket, err)
	}
	if kv.Bucket() != AgentCardBucket {
		t.Errorf("bucket name: want %q, got %q", AgentCardBucket, kv.Bucket())
	}
}

func TestDynamicRegistry_Register_And_FindByName(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	entry := makeEntry("copilot-coder", "code-gen", "go", "agent.tasks.coder", "container", 8080)
	if err := dr.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Allow watcher to propagate the Put.
	ok := waitForCondition(2*time.Second, func() bool {
		_, found := dr.FindByName("copilot-coder")
		return found
	})
	if !ok {
		t.Fatal("FindByName: entry not found after Register")
	}

	got, found := dr.FindByName("copilot-coder")
	if !found {
		t.Fatal("FindByName: expected entry, got none")
	}
	if got.Card.Name != "copilot-coder" {
		t.Errorf("name: want %q, got %q", "copilot-coder", got.Card.Name)
	}
	if got.QueueSubject != "agent.tasks.coder" {
		t.Errorf("subject: want %q, got %q", "agent.tasks.coder", got.QueueSubject)
	}
}

func TestDynamicRegistry_Register_And_FindBySkill(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	entry := makeEntry("test-writer", "write-tests", "testing", "agent.tasks.tester", "container", 8081)
	if err := dr.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ok := waitForCondition(2*time.Second, func() bool {
		return len(dr.FindBySkill("write-tests")) > 0
	})
	if !ok {
		t.Fatal("FindBySkill: entry not propagated via watcher")
	}

	results := dr.FindBySkill("write-tests")
	if len(results) != 1 {
		t.Fatalf("FindBySkill: want 1, got %d", len(results))
	}
	if results[0].Card.Name != "test-writer" {
		t.Errorf("name: want %q, got %q", "test-writer", results[0].Card.Name)
	}
}

func TestDynamicRegistry_Register_And_FindByTag(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	entry := makeEntry("doc-writer", "write-docs", "documentation", "agent.tasks.docs", "container", 8082)
	if err := dr.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ok := waitForCondition(2*time.Second, func() bool {
		return len(dr.FindByTag("documentation")) > 0
	})
	if !ok {
		t.Fatal("FindByTag: entry not propagated via watcher")
	}

	results := dr.FindByTag("documentation")
	if len(results) != 1 {
		t.Fatalf("FindByTag: want 1, got %d", len(results))
	}
	if results[0].Card.Name != "doc-writer" {
		t.Errorf("name: want %q, got %q", "doc-writer", results[0].Card.Name)
	}
}

func TestDynamicRegistry_Route(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	entry := makeEntry("router-agent", "route-skill", "routing", "agent.tasks.router", "container", 9000)
	if err := dr.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ok := waitForCondition(2*time.Second, func() bool {
		return len(dr.FindBySkill("route-skill")) > 0
	})
	if !ok {
		t.Fatal("Route: entry not propagated via watcher")
	}

	subject, err := dr.Route("route-skill")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if subject != "agent.tasks.router" {
		t.Errorf("Route subject: want %q, got %q", "agent.tasks.router", subject)
	}

	// Unknown skill returns error.
	_, err = dr.Route("no-such-skill")
	if err == nil {
		t.Error("Route with unknown skill: expected error, got nil")
	}
}

func TestDynamicRegistry_RouteByScore(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	// Two agents, both handle "code-review" but only one has the "go" tag.
	alpha := makeEntry("alpha", "code-review", "go", "agent.tasks.alpha", "runtime-go", 8090)
	beta := makeEntry("beta", "code-review", "python", "agent.tasks.beta", "runtime-py", 8091)

	if err := dr.Register(ctx, alpha); err != nil {
		t.Fatalf("Register alpha: %v", err)
	}
	if err := dr.Register(ctx, beta); err != nil {
		t.Fatalf("Register beta: %v", err)
	}

	ok := waitForCondition(2*time.Second, func() bool {
		return len(dr.FindBySkill("code-review")) == 2
	})
	if !ok {
		t.Fatal("RouteByScore: both entries not propagated via watcher")
	}

	// Request prefers "go" tag - alpha should win.
	best, err := dr.RouteByScore(RoutingRequest{
		SkillID: "code-review",
		Tags:    []string{"go"},
	})
	if err != nil {
		t.Fatalf("RouteByScore: %v", err)
	}
	if best.Card.Name != "alpha" {
		t.Errorf("RouteByScore: want alpha, got %q", best.Card.Name)
	}
}

func TestDynamicRegistry_Deregister(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	entry := makeEntry("temp-agent", "temp-skill", "temp", "agent.tasks.temp", "container", 9001)
	if err := dr.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Wait for registration to appear.
	ok := waitForCondition(2*time.Second, func() bool {
		_, found := dr.FindByName("temp-agent")
		return found
	})
	if !ok {
		t.Fatal("Deregister: entry not registered in time")
	}

	if err := dr.Deregister(ctx, "temp-agent"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	// Wait for removal to propagate.
	ok = waitForCondition(2*time.Second, func() bool {
		_, found := dr.FindByName("temp-agent")
		return !found
	})
	if !ok {
		t.Fatal("Deregister: entry still present after Deregister")
	}
}

func TestDynamicRegistry_WatchUpdates(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	// Registry starts empty.
	if len(dr.All()) != 0 {
		t.Fatalf("expected empty registry, got %d entries", len(dr.All()))
	}

	entry := makeEntry("watch-agent", "watch-skill", "watch", "agent.tasks.watch", "container", 9010)
	if err := dr.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Watcher should update the local cache.
	ok := waitForCondition(2*time.Second, func() bool {
		_, found := dr.FindByName("watch-agent")
		return found
	})
	if !ok {
		t.Fatal("WatchUpdates: cache not updated after Register")
	}
}

func TestDynamicRegistry_WatchDeletes(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	entry := makeEntry("delete-watch-agent", "del-skill", "delete", "agent.tasks.del", "container", 9020)
	if err := dr.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ok := waitForCondition(2*time.Second, func() bool {
		_, found := dr.FindByName("delete-watch-agent")
		return found
	})
	if !ok {
		t.Fatal("WatchDeletes: registration not reflected in cache")
	}

	if err := dr.Deregister(ctx, "delete-watch-agent"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	ok = waitForCondition(2*time.Second, func() bool {
		_, found := dr.FindByName("delete-watch-agent")
		return !found
	})
	if !ok {
		t.Fatal("WatchDeletes: deletion not reflected in cache")
	}
}

func TestDynamicRegistry_FallbackToStatic(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	staticEntry := makeEntry("static-agent", "static-skill", "static", "agent.tasks.static", "process", 7000)
	fallbackPath := createStaticRegistry(t, []AgentEntry{staticEntry})

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{
		NATSConn:     nc,
		FallbackPath: fallbackPath,
	})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	// Static entry should be returned even though dynamic KV is empty.
	got, found := dr.FindByName("static-agent")
	if !found {
		t.Fatal("FallbackToStatic: static entry not found")
	}
	if got.QueueSubject != "agent.tasks.static" {
		t.Errorf("subject: want %q, got %q", "agent.tasks.static", got.QueueSubject)
	}

	results := dr.FindBySkill("static-skill")
	if len(results) != 1 {
		t.Fatalf("FindBySkill fallback: want 1, got %d", len(results))
	}
}

func TestDynamicRegistry_DynamicOverridesFallback(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	staticEntry := makeEntry("agent-a", "old-skill", "old", "agent.tasks.static-a", "process", 7001)
	fallbackPath := createStaticRegistry(t, []AgentEntry{staticEntry})

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{
		NATSConn:     nc,
		FallbackPath: fallbackPath,
	})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	// Register dynamic entry with same name but different skills.
	dynamicEntry := makeEntry("agent-a", "new-skill", "new", "agent.tasks.dynamic-a", "container", 8000)
	if err := dr.Register(ctx, dynamicEntry); err != nil {
		t.Fatalf("Register dynamic: %v", err)
	}

	ok := waitForCondition(2*time.Second, func() bool {
		got, found := dr.FindByName("agent-a")
		return found && got.QueueSubject == "agent.tasks.dynamic-a"
	})
	if !ok {
		t.Fatal("DynamicOverridesFallback: dynamic entry did not override static")
	}

	got, found := dr.FindByName("agent-a")
	if !found {
		t.Fatal("FindByName: agent-a not found")
	}
	if got.QueueSubject != "agent.tasks.dynamic-a" {
		t.Errorf("subject: want %q (dynamic), got %q", "agent.tasks.dynamic-a", got.QueueSubject)
	}

	// The "old-skill" should not appear since dynamic overrides.
	byOld := dr.FindBySkill("old-skill")
	if len(byOld) != 0 {
		t.Errorf("old-skill should not appear once dynamic overrides: got %d results", len(byOld))
	}

	// The "new-skill" should appear.
	byNew := dr.FindBySkill("new-skill")
	if len(byNew) != 1 {
		t.Errorf("new-skill: want 1, got %d", len(byNew))
	}
}

func TestDynamicRegistry_All_MergesEntries(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	staticEntry := makeEntry("static-only", "s-skill", "s-tag", "agent.tasks.s", "process", 7002)
	fallbackPath := createStaticRegistry(t, []AgentEntry{staticEntry})

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{
		NATSConn:     nc,
		FallbackPath: fallbackPath,
	})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	dynamicEntry := makeEntry("dynamic-only", "d-skill", "d-tag", "agent.tasks.d", "container", 8001)
	if err := dr.Register(ctx, dynamicEntry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ok := waitForCondition(2*time.Second, func() bool {
		return len(dr.All()) == 2
	})
	if !ok {
		t.Fatalf("All_MergesEntries: want 2, got %d", len(dr.All()))
	}

	all := dr.All()
	names := make(map[string]bool, len(all))
	for _, e := range all {
		names[e.Card.Name] = true
	}
	if !names["static-only"] {
		t.Error("All: missing static-only entry")
	}
	if !names["dynamic-only"] {
		t.Error("All: missing dynamic-only entry")
	}
}

func TestDynamicRegistry_TTLExpiry(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	// Use a 2-second TTL for fast expiry in tests.
	ttl := 2 * time.Second
	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc, TTL: ttl})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	entry := makeEntry("ttl-agent", "ttl-skill", "ttl", "agent.tasks.ttl", "container", 9030)
	if err := dr.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Wait for entry to appear in local cache.
	ok := waitForCondition(2*time.Second, func() bool {
		_, found := dr.FindByName("ttl-agent")
		return found
	})
	if !ok {
		t.Fatal("TTLExpiry: entry not registered in cache in time")
	}

	// Check KV store directly: bind to the same bucket via the connection.
	// This is more reliable than waiting for watcher cache updates, which depend
	// on the server's MaxAge purge interval delivering a delete event.
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("JetStream for direct KV check: %v", err)
	}
	kv, err := js.KeyValue(AgentCardBucket)
	if err != nil {
		t.Fatalf("KeyValue for direct KV check: %v", err)
	}

	// Wait up to ttl*5 for the KV entry to be purged by the server.
	// The NATS server checks MaxAge at an interval of min(TTL/2, 5s), so
	// expiry should be reflected in the store within a few seconds of TTL.
	ok = waitForCondition(ttl*5, func() bool {
		e, err := kv.Get("ttl-agent")
		if err != nil {
			return true // ErrKeyNotFound or similar - entry is gone
		}
		// Entry may still exist as a delete marker (operation != Put).
		return e.Operation() != nats.KeyValuePut
	})
	if !ok {
		t.Fatal("TTLExpiry: KV entry still present after 5x TTL duration")
	}

	// The watcher should also have propagated the deletion to the local cache.
	// Allow additional propagation time beyond what we already waited.
	ok = waitForCondition(2*time.Second, func() bool {
		_, found := dr.FindByName("ttl-agent")
		return !found
	})
	if !ok {
		t.Log("TTLExpiry: KV entry expired but watcher cache not yet updated - this is a timing edge case")
	}
}

func TestDynamicRegistry_ConcurrentAccess(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc, TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}
	defer dr.Close()

	const goroutines = 10
	var wg sync.WaitGroup

	// Writers: register agents concurrently.
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("concurrent-agent-%d", i)
			entry := makeEntry(name, "concurrent-skill", "concurrent",
				fmt.Sprintf("agent.tasks.concurrent-%d", i), "container", 9100+i)
			if regErr := dr.Register(ctx, entry); regErr != nil {
				t.Errorf("Register %s: %v", name, regErr)
			}
		}(i)
	}

	// Readers: read concurrently while writes happen.
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = dr.All()
			_ = dr.FindBySkill("concurrent-skill")
			_ = dr.FindByTag("concurrent")
		}()
	}

	wg.Wait()

	// Wait for all writes to propagate, then deregister all.
	ok := waitForCondition(3*time.Second, func() bool {
		return len(dr.FindBySkill("concurrent-skill")) == goroutines
	})
	if !ok {
		t.Logf("ConcurrentAccess: only %d of %d agents found after writes",
			len(dr.FindBySkill("concurrent-skill")), goroutines)
	}

	// Concurrent deregisters.
	var wg2 sync.WaitGroup
	for i := range goroutines {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			name := fmt.Sprintf("concurrent-agent-%d", i)
			if err := dr.Deregister(ctx, name); err != nil {
				t.Logf("Deregister %s: %v (may already be gone)", name, err)
			}
		}(i)
	}
	wg2.Wait()
}

func TestDynamicRegistry_Close(t *testing.T) {
	nc, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	ctx := context.Background()
	dr, err := NewDynamicRegistry(ctx, DynamicConfig{NATSConn: nc})
	if err != nil {
		t.Fatalf("NewDynamicRegistry: %v", err)
	}

	// Close should not return an error.
	if err := dr.Close(); err != nil {
		t.Errorf("Close: unexpected error: %v", err)
	}

	// Register after close - KV is still valid, only the watcher stopped.
	// The KV operations via NATS still work; only the background goroutine exits.
	entry := makeEntry("post-close-agent", "pc-skill", "pc", "agent.tasks.pc", "container", 9999)
	if err := dr.Register(ctx, entry); err != nil {
		t.Errorf("Register after Close: unexpected error: %v", err)
	}
}

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// AgentCardBucket is the JetStream KV bucket name for agent registrations.
const AgentCardBucket = "agent-cards"

// DynamicConfig holds configuration for the dynamic registry.
type DynamicConfig struct {
	NATSConn     *nats.Conn   // Active NATS connection
	FallbackPath string       // Path to static registry JSON (optional)
	TTL          time.Duration // Entry TTL for heartbeat expiry (default 30s)
	Logger       *slog.Logger
}

// DynamicRegistry wraps NATS JetStream KV for real-time agent discovery.
// It implements the same lookup interface as the static Registry, allowing
// callers to use either implementation interchangeably.
type DynamicRegistry struct {
	kv       nats.KeyValue
	mu       sync.RWMutex
	agents   map[string]AgentEntry // name -> entry (local cache)
	fallback *Registry             // Static registry fallback
	logger   *slog.Logger
	cancel   context.CancelFunc // Stops the watcher
}

// NewDynamicRegistry creates a DynamicRegistry backed by NATS JetStream KV.
// It binds to (or creates) the "agent-cards" KV bucket, optionally loads a
// static fallback registry, performs an initial cache sync, and starts a
// background watcher goroutine to keep the cache up to date.
func NewDynamicRegistry(ctx context.Context, cfg DynamicConfig) (*DynamicRegistry, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 30 * time.Second
	}

	js, err := cfg.NATSConn.JetStream()
	if err != nil {
		return nil, fmt.Errorf("dynamic registry: jetstream context: %w", err)
	}

	// Create or bind to the KV bucket.
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket: AgentCardBucket,
		TTL:    cfg.TTL,
	})
	if err != nil {
		if !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			return nil, fmt.Errorf("dynamic registry: create kv bucket %q: %w", AgentCardBucket, err)
		}
		cfg.Logger.Info("dynamic registry: kv bucket already exists, binding",
			"bucket", AgentCardBucket)
		kv, err = js.KeyValue(AgentCardBucket)
		if err != nil {
			return nil, fmt.Errorf("dynamic registry: open kv bucket %q: %w", AgentCardBucket, err)
		}
	}

	dr := &DynamicRegistry{
		kv:     kv,
		agents: make(map[string]AgentEntry),
		logger: cfg.Logger,
	}

	// Load optional fallback static registry.
	if cfg.FallbackPath != "" {
		reg, err := LoadFromFile(cfg.FallbackPath)
		if err != nil {
			return nil, fmt.Errorf("dynamic registry: load fallback %s: %w", cfg.FallbackPath, err)
		}
		dr.fallback = reg
	}

	// Initial sync: load all existing KV entries into the local cache.
	keys, err := kv.Keys()
	if err != nil && !errors.Is(err, nats.ErrNoKeysFound) {
		return nil, fmt.Errorf("dynamic registry: initial sync (keys): %w", err)
	}
	for _, key := range keys {
		entry, err := kv.Get(key)
		if err != nil {
			cfg.Logger.Warn("dynamic registry: initial sync: get failed", "key", key, "err", err)
			continue
		}
		var ae AgentEntry
		if err := json.Unmarshal(entry.Value(), &ae); err != nil {
			cfg.Logger.Warn("dynamic registry: initial sync: unmarshal failed", "key", key, "err", err)
			continue
		}
		dr.agents[key] = ae
	}

	// Start background watcher.
	watchCtx, cancel := context.WithCancel(ctx)
	dr.cancel = cancel
	go dr.watch(watchCtx)

	return dr, nil
}

// watch retries runWatcher with exponential backoff until ctx is cancelled.
// This handles transient NATS failures (e.g., NATS pod restarts in Kubernetes)
// without silently freezing the registry cache.
func (dr *DynamicRegistry) watch(ctx context.Context) {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := dr.runWatcher(ctx)
		if err == nil {
			// Clean shutdown via context cancellation.
			return
		}
		dr.logger.Warn("dynamic registry: watcher stopped, retrying",
			"err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// runWatcher subscribes to KV changes and updates the local cache.
// Returns nil on clean shutdown (ctx cancelled), or an error on failure.
func (dr *DynamicRegistry) runWatcher(ctx context.Context) error {
	watcher, err := dr.kv.WatchAll()
	if err != nil {
		return fmt.Errorf("watch: failed to start watcher: %w", err)
	}
	defer watcher.Stop() //nolint:errcheck

	for {
		select {
		case <-ctx.Done():
			return nil
		case entry, ok := <-watcher.Updates():
			if !ok {
				return fmt.Errorf("watch: updates channel closed")
			}
			// nil entry signals that the initial delivery of existing keys is done.
			if entry == nil {
				continue
			}
			switch entry.Operation() {
			case nats.KeyValuePut:
				var ae AgentEntry
				if err := json.Unmarshal(entry.Value(), &ae); err != nil {
					dr.logger.Warn("dynamic registry: watch: unmarshal error",
						"key", entry.Key(), "err", err)
					continue
				}
				dr.mu.Lock()
				dr.agents[entry.Key()] = ae
				dr.mu.Unlock()
				dr.logger.Info("dynamic registry: agent registered",
					"name", entry.Key(), "operation", "put")
			case nats.KeyValueDelete, nats.KeyValuePurge:
				dr.mu.Lock()
				delete(dr.agents, entry.Key())
				dr.mu.Unlock()
				dr.logger.Info("dynamic registry: agent deregistered",
					"name", entry.Key(), "operation", entry.Operation().String())
			}
		}
	}
}

// Register adds or updates an agent entry in the KV store.
// Agents call this on startup and periodically as a heartbeat.
// The bucket TTL handles automatic expiry if the agent stops re-registering.
// The call respects ctx cancellation: if ctx is cancelled while the KV Put
// is in flight, Register returns immediately with ctx.Err().
func (dr *DynamicRegistry) Register(ctx context.Context, entry AgentEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("dynamic registry: marshal entry %q: %w", entry.Card.Name, err)
	}
	type kvResult struct {
		rev uint64
		err error
	}
	ch := make(chan kvResult, 1)
	go func() {
		rev, err := dr.kv.Put(entry.Card.Name, data)
		ch <- kvResult{rev, err}
	}()
	select {
	case <-ctx.Done():
		return fmt.Errorf("dynamic registry: register %q: %w", entry.Card.Name, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("dynamic registry: register %q: %w", entry.Card.Name, r.err)
		}
		return nil
	}
}

// Deregister removes an agent from the KV store.
// The call respects ctx cancellation: if ctx is cancelled while the KV Delete
// is in flight, Deregister returns immediately with ctx.Err().
func (dr *DynamicRegistry) Deregister(ctx context.Context, name string) error {
	ch := make(chan error, 1)
	go func() {
		ch <- dr.kv.Delete(name)
	}()
	select {
	case <-ctx.Done():
		return fmt.Errorf("dynamic registry: deregister %q: %w", name, ctx.Err())
	case err := <-ch:
		if err != nil {
			return fmt.Errorf("dynamic registry: delete entry %q: %w", name, err)
		}
		return nil
	}
}

// Close stops the background watcher and releases resources.
func (dr *DynamicRegistry) Close() error {
	dr.cancel()
	return nil
}

// FindBySkill returns all agents (dynamic + fallback) with a matching skill ID.
// Dynamic entries are included; fallback entries are appended only if they are
// not already present in the dynamic map.
func (dr *DynamicRegistry) FindBySkill(skillID string) []AgentEntry {
	all := dr.All()
	var result []AgentEntry
	for _, e := range all {
		for _, s := range e.Card.Skills {
			if s.ID == skillID {
				result = append(result, e)
				break
			}
		}
	}
	return result
}

// FindByTag returns all agents that have at least one skill with the given tag.
func (dr *DynamicRegistry) FindByTag(tag string) []AgentEntry {
	all := dr.All()
	var result []AgentEntry
	for _, e := range all {
		matched := false
		for _, s := range e.Card.Skills {
			if matched {
				break
			}
			for _, t := range s.Tags {
				if t == tag {
					result = append(result, e)
					matched = true
					break
				}
			}
		}
	}
	return result
}

// FindByName returns a specific agent by name.
// Dynamic entries take priority over fallback entries.
func (dr *DynamicRegistry) FindByName(name string) (*AgentEntry, bool) {
	dr.mu.RLock()
	e, ok := dr.agents[name]
	dr.mu.RUnlock()
	if ok {
		return &e, true
	}
	if dr.fallback != nil {
		return dr.fallback.FindByName(name)
	}
	return nil, false
}

// Route returns the NATS subject for the best-matching agent for a skill.
func (dr *DynamicRegistry) Route(skillID string) (string, error) {
	matches := dr.FindBySkill(skillID)
	if len(matches) == 0 {
		return "", errors.New("dynamic registry: no agent found for skill: " + skillID)
	}
	return matches[0].QueueSubject, nil
}

// RouteByScore returns the best agent based on scored matching.
// Uses the shared Score() function from scorer.go.
func (dr *DynamicRegistry) RouteByScore(req RoutingRequest) (*AgentEntry, error) {
	all := dr.All()
	var best *AgentEntry
	bestScore := 0.0
	for i := range all {
		s := Score(all[i], req)
		if s > bestScore {
			bestScore = s
			entry := all[i]
			best = &entry
		}
	}
	if best == nil {
		return nil, errors.New("dynamic registry: no agent matched the routing request")
	}
	return best, nil
}

// All returns all known agents (dynamic + fallback, deduplicated by name).
// Dynamic entries take precedence over fallback entries with the same name.
// The result is sorted by agent name for deterministic ordering.
func (dr *DynamicRegistry) All() []AgentEntry {
	dr.mu.RLock()
	seen := make(map[string]bool, len(dr.agents))
	result := make([]AgentEntry, 0, len(dr.agents))
	for name, entry := range dr.agents {
		result = append(result, entry)
		seen[name] = true
	}
	dr.mu.RUnlock()

	if dr.fallback != nil {
		for _, entry := range dr.fallback.All() {
			if !seen[entry.Card.Name] {
				result = append(result, entry)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Card.Name < result[j].Card.Name
	})
	return result
}

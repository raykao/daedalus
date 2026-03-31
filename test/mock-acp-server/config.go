package main

import "time"

// MockConfig controls mock server behavior for different test scenarios.
type MockConfig struct {
	Port               int
	StreamingDelay     time.Duration
	ResponseDelay      time.Duration
	SendPermissions    bool
	FailOnPrompt       bool
	LoadSessionSupport bool
	MaxSessions        int
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() MockConfig {
	return MockConfig{
		Port:               3000,
		StreamingDelay:     100 * time.Millisecond,
		ResponseDelay:      500 * time.Millisecond,
		SendPermissions:    false,
		FailOnPrompt:       false,
		LoadSessionSupport: true,
		MaxSessions:        10,
	}
}

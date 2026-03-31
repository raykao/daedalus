package main

import (
"encoding/json"
"fmt"
"strings"
)

// AssertResult holds the outcome of a single test assertion.
type AssertResult struct {
Name    string
Passed  bool
Message string
}

// Assertions groups validation helpers for ACP responses.
type Assertions struct {
results []AssertResult
}

func (a *Assertions) assert(name string, passed bool, format string, args ...any) {
msg := ""
if !passed {
msg = fmt.Sprintf(format, args...)
}
a.results = append(a.results, AssertResult{Name: name, Passed: passed, Message: msg})
}

// Check records a pass/fail based on a boolean condition.
func (a *Assertions) Check(name string, passed bool, format string, args ...any) {
a.assert(name, passed, format, args...)
}

// HasField checks that the JSON object contains a key with a non-null value.
func (a *Assertions) HasField(name string, raw json.RawMessage, field string) {
var obj map[string]json.RawMessage
if err := json.Unmarshal(raw, &obj); err != nil {
a.assert(name, false, "response is not a JSON object: %v", err)
return
}
val, ok := obj[field]
a.assert(name, ok && string(val) != "null", "missing field %q in %s", field, raw)
}

// StringField extracts a string field and checks it is non-empty.
func (a *Assertions) StringField(name string, raw json.RawMessage, field string) string {
var obj map[string]json.RawMessage
if err := json.Unmarshal(raw, &obj); err != nil {
a.assert(name, false, "not a JSON object: %v", err)
return ""
}
val, ok := obj[field]
if !ok || string(val) == "null" {
a.assert(name, false, "missing field %q", field)
return ""
}
var s string
if err := json.Unmarshal(val, &s); err != nil {
a.assert(name, false, "field %q is not a string: %v", field, err)
return ""
}
a.assert(name, s != "", "field %q is empty", field)
return s
}

// BoolField extracts a bool field and checks it equals want.
func (a *Assertions) BoolField(name string, raw json.RawMessage, field string, want bool) {
var obj map[string]json.RawMessage
if err := json.Unmarshal(raw, &obj); err != nil {
a.assert(name, false, "not a JSON object: %v", err)
return
}
val, ok := obj[field]
if !ok {
a.assert(name, false, "missing field %q", field)
return
}
var b bool
if err := json.Unmarshal(val, &b); err != nil {
a.assert(name, false, "field %q is not a bool: %v", field, err)
return
}
a.assert(name, b == want, "field %q: expected %v, got %v", field, want, b)
}

// MinNotifications checks that at least n notifications were collected.
func (a *Assertions) MinNotifications(name string, notifs []Notification, n int) {
a.assert(name, len(notifs) >= n, "expected \u2265%d notifications, got %d", n, len(notifs))
}

// NotificationMethod checks that at least one notification has the given method.
func (a *Assertions) NotificationMethod(name string, notifs []Notification, method string) {
for _, n := range notifs {
if n.Method == method {
a.assert(name, true, "")
return
}
}
a.assert(name, false, "no notification with method %q found (got %s)", method, notifMethods(notifs))
}

func notifMethods(notifs []Notification) string {
var ms []string
for _, n := range notifs {
ms = append(ms, n.Method)
}
return "[" + strings.Join(ms, ", ") + "]"
}

// Pass records an unconditional pass.
func (a *Assertions) Pass(name string) {
a.assert(name, true, "")
}

// Fail records an unconditional failure.
func (a *Assertions) Fail(name, reason string) {
	a.assert(name, false, "%s", reason)
}

// All returns all assertion results.
func (a *Assertions) All() []AssertResult { return a.results }

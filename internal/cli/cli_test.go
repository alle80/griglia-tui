package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, err bytes.Buffer
	code := Run(args, &out, &err, Options{Version: "test", WorkingDir: dir})
	return code, out.String(), err.String()
}

func TestInitDuplicateAndDiscovery(t *testing.T) {
	dir := t.TempDir()
	code, out, stderr := run(t, dir, "init", "--name", "Demo")
	if code != 0 || !strings.Contains(out, "Initialized") || stderr != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, ".griglia", "griglia.db")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".griglia", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "griglia.db*\n" {
		t.Fatalf("gitignore=%q", b)
	}
	code, _, stderr = run(t, dir, "init")
	if code != 5 || !strings.Contains(stderr, "already initialized") {
		t.Fatalf("duplicate code=%d stderr=%q", code, stderr)
	}
	nested := filepath.Join(dir, "a", "b")
	if err = os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = run(t, nested, "task", "list")
	if code != 0 || out != "No tasks.\n" || stderr != "" {
		t.Fatalf("nested code=%d out=%q err=%q", code, out, stderr)
	}
}

func TestTaskFlowAndOrdering(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := run(t, dir, "init"); code != 0 {
		t.Fatal(code)
	}
	if code, out, stderr := run(t, dir, "task", "add", "Low", "--priority", "low"); code != 0 || !strings.Contains(out, "#1") || stderr != "" {
		t.Fatalf("add: %d %q %q", code, out, stderr)
	}
	if code, _, _ := run(t, dir, "task", "add", "Urgent", "--priority", "urgent", "--lifecycle", "ready"); code != 0 {
		t.Fatal(code)
	}
	code, out, stderr := run(t, dir, "task", "list")
	if code != 0 || strings.Index(out, "Urgent") > strings.Index(out, "Low") || stderr != "" {
		t.Fatalf("list: %d %q %q", code, out, stderr)
	}
	code, out, stderr = run(t, dir, "task", "show", "1")
	if code != 0 || !strings.Contains(out, "Title: Low") || stderr != "" {
		t.Fatalf("show: %d %q %q", code, out, stderr)
	}
	code, _, stderr = run(t, dir, "task", "show", "99")
	if code != 4 || !strings.Contains(stderr, "task not found") {
		t.Fatalf("not found: %d %q", code, stderr)
	}
}

func TestLifecycleCommandsHumanAndJSON(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := run(t, dir, "init"); code != 0 {
		t.Fatal(code)
	}
	if code, _, _ := run(t, dir, "task", "add", "Lifecycle"); code != 0 {
		t.Fatal(code)
	}
	code, out, stderr := run(t, dir, "task", "edit", "1", "--title", "Edited", "--description", "details", "--priority", "high")
	if code != 0 || stderr != "" || !strings.Contains(out, "Edited task #1") {
		t.Fatalf("edit: %d %q %q", code, out, stderr)
	}
	code, out, stderr = run(t, dir, "task", "ready", "1", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"lifecycle":"ready"`) || !strings.Contains(out, `"version":3`) {
		t.Fatalf("ready: %d %q %q", code, out, stderr)
	}
	code, out, stderr = run(t, dir, "task", "done", "1", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"lifecycle":"done"`) || !strings.Contains(out, `"progress":100`) {
		t.Fatalf("done: %d %q %q", code, out, stderr)
	}
	if code, _, stderr = run(t, dir, "task", "cancel", "1"); code != 5 || !strings.Contains(stderr, "cannot move") {
		t.Fatalf("terminal: %d %q", code, stderr)
	}
	if code, _, _ = run(t, dir, "task", "add", "Cancel"); code != 0 {
		t.Fatal(code)
	}
	if code, out, stderr = run(t, dir, "task", "cancel", "2", "--reason", "obsolete"); code != 0 || stderr != "" || !strings.Contains(out, "Cancelled") {
		t.Fatalf("cancel: %d %q %q", code, out, stderr)
	}
}

func TestLifecycleCommandValidation(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := run(t, dir, "init"); code != 0 {
		t.Fatal(code)
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"task", "edit", "1"}, 2}, {[]string{"task", "edit", "1", "--priority", "bad"}, 2}, {[]string{"task", "ready", "bad"}, 2}, {[]string{"task", "done", "99"}, 4},
	} {
		code, out, stderr := run(t, dir, append([]string{"--json"}, tc.args...)...)
		if code != tc.code || stderr != "" || !strings.Contains(out, `"ok":false`) {
			t.Fatalf("args=%v code=%d out=%q err=%q", tc.args, code, out, stderr)
		}
	}
}

func TestAgentCoordinationCLIAndJSON(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := run(t, dir, "init"); code != 0 {
		t.Fatal(code)
	}
	if code, _, _ := run(t, dir, "task", "add", "Agent work", "--lifecycle", "ready", "--priority", "urgent"); code != 0 {
		t.Fatal(code)
	}
	code, out, stderr := run(t, dir, "task", "claim-next", "--agent", "codex", "--instance", "session-a", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"operational_state":"working"`) || !strings.Contains(out, `"agent_name":"codex"`) {
		t.Fatalf("claim-next: %d %q %q", code, out, stderr)
	}
	code, out, stderr = run(t, dir, "task", "claim", "1", "--agent", "codex", "--instance", "session-a", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"working"`) {
		t.Fatalf("repeated claim: %d %q %q", code, out, stderr)
	}
	for _, args := range [][]string{
		{"task", "claim", "1", "--agent", "claude", "--instance", "session-b", "--json"},
		{"task", "release", "1", "--agent", "claude", "--instance", "session-b", "--json"},
		{"task", "progress", "1", "40", "--agent", "claude", "--instance", "session-b", "--json"},
		{"task", "done", "1", "--json"}, {"task", "cancel", "1", "--json"},
		{"task", "edit", "1", "--title", "unsafe", "--json"},
	} {
		code, out, stderr = run(t, dir, args...)
		if code != 5 || stderr != "" || !strings.Contains(out, `"code":"conflict"`) {
			t.Fatalf("conflict args=%v code=%d out=%q err=%q", args, code, out, stderr)
		}
	}
	code, out, stderr = run(t, dir, "task", "progress", "1", "40", "--agent", "codex", "--instance", "session-a", "--message", "Implementing", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"progress":40`) || !strings.Contains(out, `"phase":"Implementing"`) {
		t.Fatalf("progress: %d %q %q", code, out, stderr)
	}
	code, out, stderr = run(t, dir, "task", "done", "1", "--agent", "codex", "--instance", "session-a", "--comment", "Implemented and tested", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"lifecycle":"done"`) || !strings.Contains(out, `"completion_summary":"Implemented and tested"`) || !strings.Contains(out, `"active_claim":null`) {
		t.Fatalf("done: %d %q %q", code, out, stderr)
	}
	code, out, stderr = run(t, dir, "task", "claim-next", "--agent", "codex", "--instance", "session-a", "--json")
	if code != 4 || stderr != "" || !strings.Contains(out, `"code":"no_eligible_task"`) {
		t.Fatalf("no work: %d %q %q", code, out, stderr)
	}
}

func TestAgentCommandValidationAndRelease(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := run(t, dir, "init"); code != 0 {
		t.Fatal(code)
	}
	if code, _, _ := run(t, dir, "task", "add", "Release", "--lifecycle", "ready"); code != 0 {
		t.Fatal(code)
	}
	for _, args := range [][]string{{"task", "claim", "1", "--agent", "", "--instance", "x", "--json"}, {"task", "progress", "1", "101", "--agent", "a", "--instance", "x", "--json"}, {"task", "progress", "1", "bad", "--agent", "a", "--instance", "x", "--json"}} {
		code, out, stderr := run(t, dir, args...)
		if code != 2 || stderr != "" || !strings.Contains(out, `"code":"invalid_input"`) {
			t.Fatalf("args=%v code=%d out=%q err=%q", args, code, out, stderr)
		}
	}
	if code, _, _ := run(t, dir, "task", "claim", "1", "--agent", "codex", "--instance", "one"); code != 0 {
		t.Fatal(code)
	}
	code, out, stderr := run(t, dir, "task", "release", "1", "--agent", "codex", "--instance", "one", "--reason", "handoff", "--json")
	if code != 0 || stderr != "" || !strings.Contains(out, `"operational_state":"available"`) || !strings.Contains(out, `"active_claim":null`) {
		t.Fatalf("release=%d %q %q", code, out, stderr)
	}
}

func TestJSONEnvelopesAndStreams(t *testing.T) {
	dir := t.TempDir()
	code, out, stderr := run(t, dir, "--json", "task", "list")
	if code != 3 || stderr != "" {
		t.Fatalf("missing project: %d %q", code, stderr)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env["protocol_version"] != "1" || env["ok"] != false {
		t.Fatalf("envelope=%v", env)
	}
	if code, _, _ := run(t, dir, "init"); code != 0 {
		t.Fatal(code)
	}
	code, out, stderr = run(t, dir, "task", "add", "JSON task", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("json add: %d %q", code, stderr)
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env["ok"] != true || env["error"] != nil {
		t.Fatalf("envelope=%v", env)
	}
	code, out, stderr = run(t, dir, "task", "show", "bad", "--json")
	if code != 2 || stderr != "" {
		t.Fatalf("json input: %d %q", code, stderr)
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = run(t, dir, "task", "add", "Ready task", "--lifecycle", "ready", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("ready add: %d %q", code, stderr)
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	task := env["data"].(map[string]any)["task"].(map[string]any)
	if task["operational_state"] != "available" {
		t.Fatalf("ready operational_state=%v, want available", task["operational_state"])
	}
}

func TestExplicitProjectAndVersion(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := run(t, "/", "init", "--project", dir); code != 0 {
		t.Fatal(code)
	}
	if code, out, _ := run(t, "/", "--project", dir, "task", "list"); code != 0 || out != "No tasks.\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if code, out, stderr := run(t, "/", "version", "--json"); code != 0 || stderr != "" || !strings.Contains(out, "\"version\":\"test\"") {
		t.Fatalf("version: %d %q %q", code, out, stderr)
	}
}

func TestUsageErrorsAreInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing positional argument", []string{"task", "show"}},
		{"too many positional arguments", []string{"task", "show", "1", "2"}},
		{"unknown flag", []string{"task", "show", "1", "--unknown"}},
		{"unknown command", []string{"unknown"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--json"}, tc.args...)
			code, out, stderr := run(t, t.TempDir(), args...)
			if code != 2 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			var env envelope
			if err := json.Unmarshal([]byte(out), &env); err != nil {
				t.Fatalf("invalid JSON %q: %v", out, err)
			}
			if env.Ok || env.Error == nil || env.Error.Code != "invalid_input" {
				t.Fatalf("envelope=%+v", env)
			}
		})
	}
}

func TestTUIRequiresInitializedProject(t *testing.T) {
	code, out, stderr := run(t, t.TempDir())
	if code != 3 || out != "" || !strings.Contains(stderr, "no .griglia/griglia.db found") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

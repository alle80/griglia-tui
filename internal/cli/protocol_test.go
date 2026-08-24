package cli

// The --json output is a public agent contract. These tests pin the envelope,
// DTO field sets, nullability, stable error codes, exit codes, and stream
// discipline so that accidental protocol changes fail loudly. Dynamic values
// (timestamps, UUIDs) are asserted structurally, never snapshotted.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	protocolTimeRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)
	protocolUUIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// decodeEnvelope asserts stdout carries exactly one JSON document shaped as
// the protocol v1 envelope and returns its parsed fields.
func decodeEnvelope(t *testing.T, stdout string) (bool, map[string]any, map[string]any) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%q", err, stdout)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		t.Fatalf("stdout carries more than one JSON document: %q", stdout)
	}
	if trailer := strings.TrimSpace(stdout[len(stdout)-1:]); trailer != "" && !strings.HasSuffix(strings.TrimSpace(stdout), "}") {
		t.Fatalf("unexpected trailer on stdout: %q", stdout)
	}
	assertExactKeys(t, "envelope", envelope, "protocol_version", "ok", "data", "error")
	if envelope["protocol_version"] != "1" {
		t.Fatalf("protocol_version=%v", envelope["protocol_version"])
	}
	ok, isBool := envelope["ok"].(bool)
	if !isBool {
		t.Fatalf("ok is not a boolean: %v", envelope["ok"])
	}
	if ok {
		if envelope["error"] != nil {
			t.Fatalf("ok envelope has error: %v", envelope["error"])
		}
		data, isMap := envelope["data"].(map[string]any)
		if !isMap {
			t.Fatalf("data is not an object: %v", envelope["data"])
		}
		return true, data, nil
	}
	errObj, isMap := envelope["error"].(map[string]any)
	if !isMap {
		t.Fatalf("error is not an object: %v", envelope["error"])
	}
	assertExactKeys(t, "error", errObj, "code", "message")
	// Error envelopes carry data null, except the documented partial-success
	// case (`workspace remove` whose cleanup failed); callers that expect the
	// general rule assert data == nil themselves.
	if envelope["data"] == nil {
		return false, nil, errObj
	}
	data, isMap := envelope["data"].(map[string]any)
	if !isMap {
		t.Fatalf("error envelope data is not an object: %v", envelope["data"])
	}
	return false, data, errObj
}

func assertExactKeys(t *testing.T, label string, m map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(m))
	for key := range m {
		got = append(got, key)
	}
	sort.Strings(got)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if len(got) != len(sorted) {
		t.Fatalf("%s keys=%v want=%v", label, got, sorted)
	}
	for i := range got {
		if got[i] != sorted[i] {
			t.Fatalf("%s keys=%v want=%v", label, got, sorted)
		}
	}
}

func assertTaskDTO(t *testing.T, task map[string]any) {
	t.Helper()
	assertExactKeys(t, "task", task,
		"id", "uid", "title", "description", "lifecycle", "operational_state",
		"priority", "progress", "phase", "completion_summary", "created_at",
		"updated_at", "completed_at", "cancelled_at", "version", "active_claim")
	if !protocolUUIDRE.MatchString(task["uid"].(string)) {
		t.Fatalf("uid=%v", task["uid"])
	}
	for _, field := range []string{"created_at", "updated_at"} {
		if !protocolTimeRE.MatchString(task[field].(string)) {
			t.Fatalf("%s=%v", field, task[field])
		}
	}
	for _, field := range []string{"completed_at", "cancelled_at"} {
		if task[field] != nil && !protocolTimeRE.MatchString(task[field].(string)) {
			t.Fatalf("%s=%v", field, task[field])
		}
	}
	if _, isNumber := task["id"].(float64); !isNumber {
		t.Fatalf("id is not a number: %v", task["id"])
	}
	if _, isNumber := task["version"].(float64); !isNumber {
		t.Fatalf("version is not a number: %v", task["version"])
	}
	if claim, present := task["active_claim"].(map[string]any); present {
		assertClaimDTO(t, claim)
	}
}

func assertClaimDTO(t *testing.T, claim map[string]any) {
	t.Helper()
	assertExactKeys(t, "claim", claim, "agent_name", "instance_id", "claimed_at")
	if !protocolTimeRE.MatchString(claim["claimed_at"].(string)) {
		t.Fatalf("claimed_at=%v", claim["claimed_at"])
	}
}

func assertQuestionDTO(t *testing.T, question map[string]any) {
	t.Helper()
	assertExactKeys(t, "question", question,
		"id", "task_id", "body", "blocking", "asked_by", "asked_at",
		"answer", "answered_at", "acknowledged_at")
	assertExactKeys(t, "asked_by", question["asked_by"].(map[string]any), "agent_name", "instance_id")
	if !protocolTimeRE.MatchString(question["asked_at"].(string)) {
		t.Fatalf("asked_at=%v", question["asked_at"])
	}
	for _, field := range []string{"answered_at", "acknowledged_at"} {
		if question[field] != nil && !protocolTimeRE.MatchString(question[field].(string)) {
			t.Fatalf("%s=%v", field, question[field])
		}
	}
}

func assertDependencyDTO(t *testing.T, dependency map[string]any) {
	t.Helper()
	assertExactKeys(t, "dependency", dependency, "task_id", "depends_on_task_id", "title", "lifecycle", "satisfied")
	if _, isBool := dependency["satisfied"].(bool); !isBool {
		t.Fatalf("satisfied is not a boolean: %v", dependency["satisfied"])
	}
}

// runJSON executes the CLI and asserts the JSON stream discipline shared by
// every protocol command: one envelope on stdout and nothing on stderr,
// except internal-error diagnostics which are out of protocol scope.
func runJSON(t *testing.T, dir string, args ...string) (int, map[string]any, map[string]any) {
	t.Helper()
	code, stdout, stderr := run(t, dir, append(args, "--json")...)
	if stderr != "" {
		t.Fatalf("stderr must stay silent in JSON mode: %q", stderr)
	}
	ok, data, errObj := decodeEnvelope(t, stdout)
	if ok != (code == 0) {
		t.Fatalf("ok=%v but exit=%d", ok, code)
	}
	return code, data, errObj
}

func initProtocolProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if code, _, _ := run(t, dir, "init"); code != 0 {
		t.Fatal("init failed")
	}
	return dir
}

func TestProtocolVersionAndInit(t *testing.T) {
	dir := t.TempDir()
	code, data, _ := runJSON(t, dir, "version")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "version", data, "version", "commit", "build_date")
	if data["version"] != "test" || data["commit"] != "unknown" || data["build_date"] != "unknown" {
		t.Fatalf("version data=%v", data)
	}
	code, data, _ = runJSON(t, dir, "init", "--name", "Proto")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "init", data, "project")
	project := data["project"].(map[string]any)
	assertExactKeys(t, "project", project, "name", "database")
	if project["name"] != "Proto" || !strings.HasSuffix(project["database"].(string), ".griglia/griglia.db") {
		t.Fatalf("project=%v", project)
	}
}

func TestProtocolTaskLifecycleDTOs(t *testing.T) {
	dir := initProtocolProject(t)
	code, data, _ := runJSON(t, dir, "task", "add", "First", "--description", "body", "--priority", "high")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "add", data, "task")
	task := data["task"].(map[string]any)
	assertTaskDTO(t, task)
	if task["lifecycle"] != "backlog" || task["operational_state"] != nil || task["active_claim"] != nil || task["completed_at"] != nil || task["cancelled_at"] != nil {
		t.Fatalf("backlog task=%v", task)
	}

	code, data, _ = runJSON(t, dir, "task", "edit", "1", "--title", "Renamed")
	if code != 0 {
		t.Fatal(code)
	}
	assertTaskDTO(t, data["task"].(map[string]any))

	code, data, _ = runJSON(t, dir, "task", "ready", "1")
	if code != 0 {
		t.Fatal(code)
	}
	task = data["task"].(map[string]any)
	assertTaskDTO(t, task)
	if task["lifecycle"] != "ready" || task["operational_state"] != "available" {
		t.Fatalf("ready task=%v", task)
	}

	code, data, _ = runJSON(t, dir, "task", "show", "1")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "show", data, "task")
	assertTaskDTO(t, data["task"].(map[string]any))

	code, data, _ = runJSON(t, dir, "task", "list")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "list", data, "tasks")
	tasks := data["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("tasks=%v", tasks)
	}
	assertTaskDTO(t, tasks[0].(map[string]any))

	code, data, _ = runJSON(t, dir, "task", "done", "1")
	if code != 0 {
		t.Fatal(code)
	}
	task = data["task"].(map[string]any)
	assertTaskDTO(t, task)
	if task["lifecycle"] != "done" || task["progress"] != float64(100) || task["completed_at"] == nil || task["operational_state"] != nil {
		t.Fatalf("done task=%v", task)
	}

	code, data, _ = runJSON(t, dir, "task", "add", "Cancel me")
	if code != 0 {
		t.Fatal(code)
	}
	code, data, _ = runJSON(t, dir, "task", "cancel", "2", "--reason", "obsolete")
	if code != 0 {
		t.Fatal(code)
	}
	task = data["task"].(map[string]any)
	assertTaskDTO(t, task)
	if task["lifecycle"] != "cancelled" || task["cancelled_at"] == nil {
		t.Fatalf("cancelled task=%v", task)
	}
}

func TestProtocolTerminalCreationIsConsistent(t *testing.T) {
	dir := initProtocolProject(t)
	code, data, _ := runJSON(t, dir, "task", "add", "Imported as done", "--lifecycle", "done")
	if code != 0 {
		t.Fatal(code)
	}
	task := data["task"].(map[string]any)
	assertTaskDTO(t, task)
	if task["progress"] != float64(100) || task["completed_at"] == nil {
		t.Fatalf("terminal-created task must be consistent: %v", task)
	}
	code, data, _ = runJSON(t, dir, "task", "add", "Imported as cancelled", "--lifecycle", "cancelled")
	if code != 0 {
		t.Fatal(code)
	}
	if data["task"].(map[string]any)["cancelled_at"] == nil {
		t.Fatalf("cancelled-created task must carry cancelled_at: %v", data)
	}
}

func TestProtocolClaimWorkflowDTOs(t *testing.T) {
	dir := initProtocolProject(t)
	runJSON(t, dir, "task", "add", "Work", "--lifecycle", "ready")
	agent := []string{"--agent", "codex", "--instance", "one"}

	code, data, _ := runJSON(t, dir, append([]string{"task", "claim", "1"}, agent...)...)
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "claim", data, "task", "claim")
	task := data["task"].(map[string]any)
	assertTaskDTO(t, task)
	if task["operational_state"] != "working" || task["active_claim"] == nil {
		t.Fatalf("claimed task=%v", task)
	}
	assertClaimDTO(t, data["claim"].(map[string]any))

	code, data, _ = runJSON(t, dir, append([]string{"task", "progress", "1", "40", "--message", "building"}, agent...)...)
	if code != 0 {
		t.Fatal(code)
	}
	task = data["task"].(map[string]any)
	if task["progress"] != float64(40) || task["phase"] != "building" {
		t.Fatalf("progress task=%v", task)
	}

	code, data, _ = runJSON(t, dir, append([]string{"task", "release", "1", "--reason", "handoff"}, agent...)...)
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "release", data, "task", "claim")
	task = data["task"].(map[string]any)
	if task["active_claim"] != nil || data["claim"] != nil || task["operational_state"] != "available" {
		t.Fatalf("released task=%v claim=%v", task, data["claim"])
	}

	code, data, _ = runJSON(t, dir, append([]string{"task", "claim-next"}, agent...)...)
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "claim-next", data, "task", "claim")
	if data["task"].(map[string]any)["id"] != float64(1) {
		t.Fatalf("claim-next task=%v", data["task"])
	}

	code, data, _ = runJSON(t, dir, append([]string{"task", "done", "1", "--comment", "shipped"}, agent...)...)
	if code != 0 {
		t.Fatal(code)
	}
	task = data["task"].(map[string]any)
	assertTaskDTO(t, task)
	if task["lifecycle"] != "done" || task["completion_summary"] != "shipped" || task["active_claim"] != nil {
		t.Fatalf("agent-completed task=%v", task)
	}

	// The eligible pool is now empty: the stable no-work error.
	code, _, errObj := runJSON(t, dir, append([]string{"task", "claim-next"}, agent...)...)
	if code != 4 || errObj["code"] != "no_eligible_task" {
		t.Fatalf("code=%d err=%v", code, errObj)
	}
}

func TestProtocolQuestionDTOs(t *testing.T) {
	dir := initProtocolProject(t)
	runJSON(t, dir, "task", "add", "Ask", "--lifecycle", "ready")
	agent := []string{"--agent", "codex", "--instance", "one"}
	runJSON(t, dir, append([]string{"task", "claim", "1"}, agent...)...)

	code, data, _ := runJSON(t, dir, append([]string{"task", "ask", "1", "Which port?", "--blocking"}, agent...)...)
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "ask", data, "question")
	question := data["question"].(map[string]any)
	assertQuestionDTO(t, question)
	if question["blocking"] != true || question["answer"] != nil || question["answered_at"] != nil || question["acknowledged_at"] != nil {
		t.Fatalf("asked question=%v", question)
	}

	code, data, _ = runJSON(t, dir, "task", "show", "1")
	if code != 0 || data["task"].(map[string]any)["operational_state"] != "waiting_for_human" {
		t.Fatalf("waiting task=%v", data)
	}

	code, data, _ = runJSON(t, dir, "task", "answer", "1", "Use 8080")
	if code != 0 {
		t.Fatal(code)
	}
	question = data["question"].(map[string]any)
	assertQuestionDTO(t, question)
	if question["answer"] != "Use 8080" || question["answered_at"] == nil {
		t.Fatalf("answered question=%v", question)
	}

	code, data, _ = runJSON(t, dir, "task", "questions", "1", "--unacknowledged")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "questions", data, "questions")
	questions := data["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("questions=%v", questions)
	}
	assertQuestionDTO(t, questions[0].(map[string]any))

	code, data, _ = runJSON(t, dir, append([]string{"task", "acknowledge", "1"}, agent...)...)
	if code != 0 {
		t.Fatal(code)
	}
	question = data["question"].(map[string]any)
	assertQuestionDTO(t, question)
	if question["acknowledged_at"] == nil {
		t.Fatalf("acknowledged question=%v", question)
	}

	code, data, _ = runJSON(t, dir, "task", "questions", "1")
	if code != 0 || len(data["questions"].([]any)) != 1 {
		t.Fatalf("all questions=%v", data)
	}
	code, data, _ = runJSON(t, dir, "task", "questions", "1", "--unanswered")
	if code != 0 || len(data["questions"].([]any)) != 0 {
		t.Fatalf("unanswered must be an empty array, got %v", data["questions"])
	}
}

func TestProtocolDependencyDTOs(t *testing.T) {
	dir := initProtocolProject(t)
	runJSON(t, dir, "task", "add", "Dependent", "--lifecycle", "ready")
	runJSON(t, dir, "task", "add", "Prerequisite", "--lifecycle", "ready")

	code, data, _ := runJSON(t, dir, "task", "depend", "1", "--on", "2")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "depend", data, "dependency")
	dependency := data["dependency"].(map[string]any)
	assertDependencyDTO(t, dependency)
	if dependency["task_id"] != float64(1) || dependency["depends_on_task_id"] != float64(2) || dependency["satisfied"] != false {
		t.Fatalf("dependency=%v", dependency)
	}

	code, data, _ = runJSON(t, dir, "task", "show", "1")
	if code != 0 || data["task"].(map[string]any)["operational_state"] != "blocked" {
		t.Fatalf("blocked task=%v", data)
	}

	code, data, _ = runJSON(t, dir, "task", "dependencies", "1")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "dependencies", data, "dependencies")
	list := data["dependencies"].([]any)
	if len(list) != 1 {
		t.Fatalf("dependencies=%v", list)
	}
	assertDependencyDTO(t, list[0].(map[string]any))

	code, data, _ = runJSON(t, dir, "task", "undepend", "1", "--on", "2")
	if code != 0 {
		t.Fatal(code)
	}
	assertExactKeys(t, "undepend", data, "dependency")
	edge := data["dependency"].(map[string]any)
	assertExactKeys(t, "removed edge", edge, "task_id", "depends_on_task_id")

	code, data, _ = runJSON(t, dir, "task", "dependencies", "1")
	if code != 0 || len(data["dependencies"].([]any)) != 0 {
		t.Fatalf("dependencies must be an empty array, got %v", data["dependencies"])
	}
}

func TestProtocolStableErrors(t *testing.T) {
	uninitialized := t.TempDir()
	dir := initProtocolProject(t)
	runJSON(t, dir, "task", "add", "Errors", "--lifecycle", "ready")
	runJSON(t, dir, "task", "claim", "1", "--agent", "codex", "--instance", "one")

	for _, tc := range []struct {
		name string
		dir  string
		args []string
		code int
		kind string
	}{
		{"invalid task id", dir, []string{"task", "show", "zero"}, 2, "invalid_input"},
		{"invalid priority", dir, []string{"task", "add", "X", "--priority", "extreme"}, 2, "invalid_input"},
		{"unknown flag", dir, []string{"task", "list", "--bogus"}, 2, "invalid_input"},
		{"unknown command", dir, []string{"task", "explode", "1"}, 2, "invalid_input"},
		{"missing identity", dir, []string{"task", "claim", "1"}, 2, "invalid_input"},
		{"project not initialized", uninitialized, []string{"task", "list"}, 3, "project_not_initialized"},
		{"task not found", dir, []string{"task", "show", "99"}, 4, "not_found"},
		{"question not found", dir, []string{"task", "answer", "99", "text"}, 4, "not_found"},
		{"conflicting claim", dir, []string{"task", "claim", "1", "--agent", "other", "--instance", "two"}, 5, "conflict"},
		{"claimed human edit", dir, []string{"task", "edit", "1", "--title", "New"}, 5, "conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, data, errObj := runJSON(t, tc.dir, tc.args...)
			if code != tc.code || errObj["code"] != tc.kind {
				t.Fatalf("code=%d err=%v want %d/%s", code, errObj, tc.code, tc.kind)
			}
			if data != nil {
				t.Fatalf("error envelopes carry data null outside the workspace remove partial-success case: %v", data)
			}
			if message, _ := errObj["message"].(string); strings.TrimSpace(message) == "" {
				t.Fatalf("error message must be present: %v", errObj)
			}
		})
	}
}

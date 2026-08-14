package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func chdirCleanCLITestRoot(t *testing.T) string {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir clean root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	return root
}

func newIssueDeliverTestCmd(serverURL string) *cobra.Command {
	cmd := &cobra.Command{Use: "deliver"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("content-file", "", "")
	cmd.Flags().String("parent", "", "")
	cmd.Flags().String("local-command", "", "")
	cmd.Flags().String("local-result", "", "")
	cmd.Flags().String("customer-path", "", "")
	cmd.Flags().String("customer-method", "", "")
	cmd.Flags().String("customer-surface", "", "")
	cmd.Flags().String("customer-evidence", "", "")
	cmd.Flags().String("customer-reason", "", "")
	cmd.Flags().String("output", "json", "")
	_ = cmd.Flags().Set("server-url", serverURL)
	_ = cmd.Flags().Set("workspace-id", "ws-1")
	return cmd
}

func TestRunIssueDeliverReadsFileAndSendsVerification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	root := chdirCleanCLITestRoot(t)
	contentPath := filepath.Join(root, "delivery.md")
	if err := os.WriteFile(contentPath, []byte("Verified delivery body.\n"), 0o600); err != nil {
		t.Fatalf("write delivery body: %v", err)
	}

	var delivered map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+testIssueUUID:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": testIssueUUID, "identifier": "MUL-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/"+testIssueUUID+"/deliver":
			if err := json.NewDecoder(r.Body).Decode(&delivered); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"issue": map[string]any{"id": testIssueUUID, "identifier": "MUL-1", "status": "in_review"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	cmd := newIssueDeliverTestCmd(srv.URL)
	for name, value := range map[string]string{
		"content-file": "delivery.md", "parent": "comment-1", "local-command": "go test ./...", "local-result": "passed",
		"customer-path": "passed", "customer-method": "browser", "customer-surface": "https://app.multica.ai/issues/MUL-1", "customer-evidence": "receipt visible",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}
	if _, err := captureRuntimeStdout(t, func() error { return runIssueDeliver(cmd, []string{testIssueUUID}) }); err != nil {
		t.Fatalf("runIssueDeliver: %v", err)
	}
	if delivered["content"] != "Verified delivery body.\n" || delivered["parent_id"] != "comment-1" {
		t.Fatalf("delivery content/parent = %#v", delivered)
	}
	local := delivered["local_verification"].(map[string]any)
	customer := delivered["customer_path"].(map[string]any)
	if local["command"] != "go test ./..." || customer["method"] != "browser" || customer["evidence"] != "receipt visible" {
		t.Fatalf("delivery verification = local:%#v customer:%#v", local, customer)
	}
}

func TestRunIssueDeliverSendsNotApplicableReasonOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	root := chdirCleanCLITestRoot(t)
	if err := os.WriteFile(filepath.Join(root, "delivery.md"), []byte("Internal maintenance completed.\n"), 0o600); err != nil {
		t.Fatalf("write delivery body: %v", err)
	}
	var delivered map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+testIssueUUID:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": testIssueUUID, "identifier": "MUL-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/"+testIssueUUID+"/deliver":
			_ = json.NewDecoder(r.Body).Decode(&delivered)
			_ = json.NewEncoder(w).Encode(map[string]any{"issue": map[string]any{"identifier": "MUL-1"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	cmd := newIssueDeliverTestCmd(srv.URL)
	for name, value := range map[string]string{
		"content-file": "delivery.md", "local-command": "go test ./internal/service", "local-result": "passed",
		"customer-path": "not_applicable", "customer-reason": "No customer-facing surface changed.",
	} {
		_ = cmd.Flags().Set(name, value)
	}
	if _, err := captureRuntimeStdout(t, func() error { return runIssueDeliver(cmd, []string{testIssueUUID}) }); err != nil {
		t.Fatalf("runIssueDeliver: %v", err)
	}
	customer := delivered["customer_path"].(map[string]any)
	if customer["status"] != "not_applicable" || customer["reason"] == "" {
		t.Fatalf("customer path = %#v", customer)
	}
	for _, forbidden := range []string{"method", "surface", "evidence"} {
		if _, present := customer[forbidden]; present {
			t.Fatalf("not-applicable payload included %s: %#v", forbidden, customer)
		}
	}
}

func TestRunRuntimeDisableCallsDurableEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	chdirCleanCLITestRoot(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/runtimes/rt-1/disable" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"runtime": map[string]any{"id": "rt-1", "status": "disabled"}, "cancelled_task_count": 2})
	}))
	defer srv.Close()
	cmd := newRuntimeDeleteTestCmd(srv.URL)
	_ = cmd.Flags().Set("output", "json")
	out, err := captureRuntimeStdout(t, func() error { return runRuntimeDisable(cmd, []string{"rt-1"}) })
	if err != nil {
		t.Fatalf("runRuntimeDisable: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil || result["cancelled_task_count"] != float64(2) {
		t.Fatalf("runtime disable output=%#v err=%v", result, err)
	}
}

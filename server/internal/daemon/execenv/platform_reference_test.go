package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/runtimeapps"
)

// platformRefTestCtx returns a representative assignment-triggered context
// exercising every section the platform-reference split touches: repos,
// project, connected apps, skills.
func platformRefTestCtx(mode PlatformReferenceMode) TaskContextForEnv {
	return TaskContextForEnv{
		IssueID:           "issue-pr-1",
		AgentName:         "Eve",
		AgentID:           "eve-1",
		PlatformReference: mode,
		Repos: []RepoContextForEnv{
			{URL: "https://github.com/org/backend", Description: "backend"},
			{URL: "https://github.com/org/frontend"},
		},
		ProjectID:    "proj-1",
		ProjectTitle: "Project One",
		AgentSkills:  []SkillContextForEnv{{Name: "skill-x", Description: "x"}},
		ConnectedApps: []runtimeapps.ConnectedApp{{
			Provider:    "composio",
			ServerName:  "composio",
			ToolkitSlug: "notion",
			ToolkitName: "Notion",
		}},
	}
}

func TestParsePlatformReferenceMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw     string
		want    PlatformReferenceMode
		wantErr bool
	}{
		{"", DefaultPlatformReference, false},
		{"inline", PlatformReferenceInline, false},
		{"file", PlatformReferenceFile, false},
		{"  FILE  ", PlatformReferenceFile, false},
		{"Inline", PlatformReferenceInline, false},
		{"files", "", true},
		{"0", "", true},
		{"off", "", true},
	}
	for _, tc := range cases {
		got, err := ParsePlatformReferenceMode(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParsePlatformReferenceMode(%q): expected error, got %q", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePlatformReferenceMode(%q): unexpected error: %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParsePlatformReferenceMode(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	// Pin the default: rollback contract is MULTICA_PLATFORM_REFERENCE=inline,
	// which only makes sense while the default is file.
	if DefaultPlatformReference != PlatformReferenceFile {
		t.Errorf("DefaultPlatformReference = %q, want %q", DefaultPlatformReference, PlatformReferenceFile)
	}
}

// TestEffectivePlatformReferenceResolution pins the ctx > env > default
// resolution chain and the invalid-env inline fallback. Not parallel: uses
// t.Setenv.
func TestEffectivePlatformReferenceResolution(t *testing.T) {
	t.Setenv(PlatformReferenceEnvVar, "")
	if got := effectivePlatformReference(TaskContextForEnv{}); got != DefaultPlatformReference {
		t.Errorf("empty ctx + empty env: got %q, want default %q", got, DefaultPlatformReference)
	}
	t.Setenv(PlatformReferenceEnvVar, "inline")
	if got := effectivePlatformReference(TaskContextForEnv{}); got != PlatformReferenceInline {
		t.Errorf("env=inline: got %q", got)
	}
	if got := effectivePlatformReference(TaskContextForEnv{PlatformReference: PlatformReferenceFile}); got != PlatformReferenceFile {
		t.Errorf("ctx must win over env: got %q", got)
	}
	t.Setenv(PlatformReferenceEnvVar, "garbage")
	if got := effectivePlatformReference(TaskContextForEnv{}); got != PlatformReferenceInline {
		t.Errorf("invalid env must fall back to inline, got %q", got)
	}
}

// TestPlatformReferenceInlineBriefUnchanged locks the inline-mode brief to
// the pre-split composition: no pointer, full Available Commands, and every
// reference section rendered inline. This is the rollback shape
// (MULTICA_PLATFORM_REFERENCE=inline must be byte-identical to the
// pre-Phase-1 brief; the shared byte-for-byte writer constants plus these
// sentinels pin that).
func TestPlatformReferenceInlineBriefUnchanged(t *testing.T) {
	t.Parallel()
	out := buildMetaSkillContent("claude", platformRefTestCtx(PlatformReferenceInline))

	if strings.Contains(out, ".multica/platform.md") {
		t.Errorf("inline brief must not reference .multica/platform.md\n---\n%s", out)
	}
	for _, want := range []string{
		"core agent loop and common issue create/update tasks",
		"### Squad maintenance",
		"## Knowledge Layers",
		"## Issue Metadata",
		"high-signal scratchpad",
		"**Recommended keys**",
		"## Attachments",
		"## Connected Apps",
		"- Notion (`notion`) via MCP server `composio`",
		"Available in this workspace — `multica repo checkout <url> [--ref <branch-or-sha>]` to fetch (creates a git worktree on a dedicated branch).",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inline brief missing %q\n---\n%s", want, out)
		}
	}
}

// TestPlatformReferenceFileModeBriefShape asserts the file-mode brief keeps
// the pointer plus the three core command lines inline and drops the moved
// reference sections, while every behavior/safety INV anchor stays inline.
func TestPlatformReferenceFileModeBriefShape(t *testing.T) {
	t.Parallel()
	out := buildMetaSkillContent("claude", platformRefTestCtx(PlatformReferenceFile))

	// The mandatory pointer line.
	if !strings.Contains(out, "Platform command/reference details live in .multica/platform.md — read it when you need command flags or platform mechanics beyond this brief.") {
		t.Fatalf("file-mode brief missing the platform.md pointer line\n---\n%s", out)
	}

	// Moved sections must NOT render inline.
	for _, banned := range []string{
		"## Knowledge Layers",
		"## Attachments",
		"## Connected Apps",
		"### Squad maintenance",
		"core agent loop and common issue create/update tasks",
		"multica issue create --title",
		"multica issue update <id>",
		"multica issue comment add <issue-id>",
		"multica issue metadata list <issue-id>",
		"multica workspace member invite",
		"high-signal scratchpad",
		"**Recommended keys**",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("file-mode brief should NOT contain moved content %q\n---\n%s", banned, out)
		}
	}

	// The three core command lines stay inline, byte-identical to the full
	// list's bullets — including the complete `issue deliver` flag contract.
	for _, want := range []string{
		cmdBulletIssueGet,
		cmdBulletIssueCommentList,
		cmdBulletIssueDeliver,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("file-mode brief missing core command line %q\n---\n%s", want, out)
		}
	}

	// INV anchors: behavior/safety contracts never move to the file.
	for _, want := range []string{
		"## Background Task Safety",
		"## Agent Identity",
		"## Instruction Precedence",
		"### Workflow",
		"## Single-Issue Execution",
		"## Mentions",
		"## Comment Formatting",
		"Never use inline `--content` for agent-authored comments",
		"## Important: Always Use the `multica` CLI",
		"## Output",
		// The deliver contract in the workflow + Output text.
		"multica issue deliver issue-pr-1 --content-file <path>",
		"--customer-path passed|not_applicable",
		"⚠️ **Final results MUST be delivered via `multica issue deliver`.**",
		// Issue Metadata keeps its heading + the one-sentence stand-in so
		// workflow references to "the `## Issue Metadata` section above"
		// still resolve.
		"## Issue Metadata",
		"Read issue metadata on entry, write sparingly on exit — full guidance in .multica/platform.md.",
		"See the `## Issue Metadata` section above",
		"multica issue metadata set`/`delete",
		// Repositories keeps the dynamic URL list inline.
		"## Repositories",
		"- https://github.com/org/backend — backend",
		"- https://github.com/org/frontend",
		"checkout usage in .multica/platform.md",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("file-mode brief missing INV anchor %q\n---\n%s", want, out)
		}
	}
}

// TestPlatformReferenceFileContent asserts .multica/platform.md carries every
// moved section.
func TestPlatformReferenceFileContent(t *testing.T) {
	t.Parallel()
	content := buildPlatformReferenceContent(platformRefTestCtx(PlatformReferenceFile))

	for _, want := range []string{
		"# Multica Platform Reference",
		// Full Available Commands list.
		"## Available Commands",
		"core agent loop and common issue create/update tasks",
		cmdBulletIssueGet,
		cmdBulletIssueCommentList,
		cmdBulletIssueDeliver,
		"multica issue create --title",
		"multica issue update <id>",
		"multica issue status <id> <status>",
		"multica issue comment add <issue-id>",
		"multica issue metadata list <issue-id>",
		"multica issue metadata set <issue-id>",
		"multica issue metadata delete <issue-id>",
		"multica repo checkout <url>",
		"### Squad maintenance",
		// Repositories usage prose (URL list stays in the brief).
		"## Repositories",
		"`multica repo checkout <url> [--ref <branch-or-sha>]`",
		// Knowledge Layers.
		"## Knowledge Layers",
		"memory files as non-authoritative hints",
		// Full Issue Metadata guidance.
		"## Issue Metadata",
		"high-signal scratchpad",
		"**Read on entry.**",
		"**Write on exit.**",
		"**What NOT to pin.**",
		"**Recommended keys**",
		// Attachments.
		"## Attachments",
		"multica attachment --help",
		// Connected Apps (agent-config data).
		"## Connected Apps",
		"- Notion (`notion`) via MCP server `composio`",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("platform.md missing %q\n---\n%s", want, content)
		}
	}

	// No task-identifying values may leak into the byte-stable file.
	for _, banned := range []string{"issue-pr-1", "eve-1"} {
		if strings.Contains(content, banned) {
			t.Errorf("platform.md must not embed task/agent identifiers, found %q\n---\n%s", banned, content)
		}
	}
}

// TestPlatformReferenceContentByteStable pins that platform.md is
// byte-identical across runs whose task-specific context differs (issue id,
// trigger comment, comment counters) while workspace/agent config is the
// same.
func TestPlatformReferenceContentByteStable(t *testing.T) {
	t.Parallel()
	a := platformRefTestCtx(PlatformReferenceFile)
	b := platformRefTestCtx(PlatformReferenceFile)
	b.IssueID = "issue-pr-2"
	b.TriggerCommentID = "comment-pr-9"
	b.TriggerThreadID = "thread-pr-9"
	b.NewCommentCount = 7
	b.NewCommentsSince = "2026-08-21T00:00:00Z"
	b.HandoffNote = "different note"
	b.PriorSessionResumed = true

	ca := buildPlatformReferenceContent(a)
	cb := buildPlatformReferenceContent(b)
	if ca != cb {
		t.Fatalf("platform.md must be byte-stable across task contexts:\n--- a ---\n%s\n--- b ---\n%s", ca, cb)
	}
}

// TestInjectRuntimeConfigPlatformReferenceFileLifecycle covers the on-disk
// contract: file mode writes .multica/platform.md, inline mode does not,
// quick-create never does, and CleanupRuntimeConfig removes it.
func TestInjectRuntimeConfigPlatformReferenceFileLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("file_mode_writes_and_cleanup_removes", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ctx := platformRefTestCtx(PlatformReferenceFile)
		if _, err := InjectRuntimeConfig(dir, "claude", ctx); err != nil {
			t.Fatalf("InjectRuntimeConfig: %v", err)
		}
		refPath := filepath.Join(dir, ".multica", "platform.md")
		onDisk, err := os.ReadFile(refPath)
		if err != nil {
			t.Fatalf("expected platform.md to be written: %v", err)
		}
		if string(onDisk) != buildPlatformReferenceContent(ctx) {
			t.Errorf("platform.md on disk differs from rendered content")
		}
		brief, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
		if err != nil {
			t.Fatalf("read CLAUDE.md: %v", err)
		}
		if !strings.Contains(string(brief), ".multica/platform.md") {
			t.Errorf("file-mode brief on disk missing pointer to platform.md")
		}
		if err := CleanupRuntimeConfig(dir, "claude"); err != nil {
			t.Fatalf("CleanupRuntimeConfig: %v", err)
		}
		if _, err := os.Stat(refPath); !os.IsNotExist(err) {
			t.Errorf("expected CleanupRuntimeConfig to remove platform.md, stat err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
			t.Errorf("expected CleanupRuntimeConfig to remove the daemon-created CLAUDE.md, stat err=%v", err)
		}
	})

	t.Run("inline_mode_writes_no_file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if _, err := InjectRuntimeConfig(dir, "claude", platformRefTestCtx(PlatformReferenceInline)); err != nil {
			t.Fatalf("InjectRuntimeConfig: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".multica", "platform.md")); !os.IsNotExist(err) {
			t.Errorf("inline mode must not write platform.md, stat err=%v", err)
		}
	})

	t.Run("quick_create_always_inline", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		fileCtx := TaskContextForEnv{QuickCreatePrompt: "p", AgentName: "Eve", AgentID: "eve-1", PlatformReference: PlatformReferenceFile}
		inlineCtx := fileCtx
		inlineCtx.PlatformReference = PlatformReferenceInline
		if _, err := InjectRuntimeConfig(dir, "claude", fileCtx); err != nil {
			t.Fatalf("InjectRuntimeConfig: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".multica", "platform.md")); !os.IsNotExist(err) {
			t.Errorf("quick-create must not write platform.md, stat err=%v", err)
		}
		// The quick-create guardrail brief is identical in both modes.
		if buildMetaSkillContent("claude", fileCtx) != buildMetaSkillContent("claude", inlineCtx) {
			t.Errorf("quick-create brief must be byte-identical across platform-reference modes")
		}
	})
}

// TestFileModeBriefPointerUsesAbsoluteWorkdirPath pins ARG-548 review
// ADV-10: agents cd into checked-out repos, where a relative
// `.multica/platform.md` pointer dangles. When the workdir is known (as it
// always is on the InjectRuntimeConfig path), every file-mode stand-in line
// must print the absolute workdir-joined path. platform.md CONTENT must stay
// free of the path — it is byte-stable across runs.
func TestFileModeBriefPointerUsesAbsoluteWorkdirPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := platformRefTestCtx(PlatformReferenceFile)

	brief, err := InjectRuntimeConfig(dir, "claude", ctx)
	if err != nil {
		t.Fatalf("InjectRuntimeConfig: %v", err)
	}
	absRef := filepath.Join(dir, ".multica", "platform.md")
	for _, want := range []string{
		"Platform command/reference details live in " + absRef + " — read it when you need command flags or platform mechanics beyond this brief.",
		"Read issue metadata on entry, write sparingly on exit — full guidance in " + absRef + ".",
		"Available in this workspace — checkout usage in " + absRef + ".",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("file-mode brief missing absolute-path stand-in %q\n---\n%s", want, brief)
		}
	}
	onDisk, err := os.ReadFile(absRef)
	if err != nil {
		t.Fatalf("read platform.md: %v", err)
	}
	if strings.Contains(string(onDisk), dir) {
		t.Errorf("platform.md must stay byte-stable and never embed the workdir path\n---\n%s", onDisk)
	}
}

// TestInjectRuntimeConfigForEnvLocalDirectory pins ARG-548 review ADV-9: in
// local_directory mode the workdir is the user's own tree, so
// .multica/platform.md must go through the sidecar manifest — never
// overwrite user bytes, degrade the run to the inline brief on collision,
// and clean up only what we created.
func TestInjectRuntimeConfigForEnvLocalDirectory(t *testing.T) {
	t.Parallel()

	newLocalEnv := func(t *testing.T) *Environment {
		t.Helper()
		return &Environment{
			RootDir:        t.TempDir(),
			WorkDir:        t.TempDir(),
			LocalDirectory: true,
		}
	}

	t.Run("user_owned_platform_md_degrades_to_inline", func(t *testing.T) {
		t.Parallel()
		env := newLocalEnv(t)
		refPath := filepath.Join(env.WorkDir, ".multica", "platform.md")
		if err := os.MkdirAll(filepath.Dir(refPath), 0o755); err != nil {
			t.Fatal(err)
		}
		const userContent = "my own notes about this repo\n"
		if err := os.WriteFile(refPath, []byte(userContent), 0o644); err != nil {
			t.Fatal(err)
		}

		brief, err := InjectRuntimeConfigForEnv(env, "claude", platformRefTestCtx(PlatformReferenceFile))
		if err != nil {
			t.Fatalf("InjectRuntimeConfigForEnv: %v", err)
		}
		// The run degrades to the fully-inline brief: no pointer, inline
		// reference sections present.
		if strings.Contains(brief, "platform.md") {
			t.Errorf("degraded brief must not point at platform.md\n---\n%s", brief)
		}
		if !strings.Contains(brief, "## Knowledge Layers") {
			t.Errorf("degraded brief must render the inline reference sections\n---\n%s", brief)
		}
		// User bytes untouched by inject AND by cleanup.
		if got, _ := os.ReadFile(refPath); string(got) != userContent {
			t.Fatalf("user platform.md was modified: %q", got)
		}
		if err := CleanupRuntimeConfig(env.WorkDir, "claude"); err != nil {
			t.Fatalf("CleanupRuntimeConfig: %v", err)
		}
		if got, err := os.ReadFile(refPath); err != nil || string(got) != userContent {
			t.Fatalf("cleanup must leave the user's platform.md intact, got %q err=%v", got, err)
		}
		if err := CleanupSidecars(env.RootDir); err != nil {
			t.Fatalf("CleanupSidecars: %v", err)
		}
		if got, err := os.ReadFile(refPath); err != nil || string(got) != userContent {
			t.Fatalf("sidecar cleanup must leave the user's platform.md intact, got %q err=%v", got, err)
		}
	})

	t.Run("fresh_write_is_manifest_tracked_and_cleaned_up", func(t *testing.T) {
		t.Parallel()
		env := newLocalEnv(t)
		brief, err := InjectRuntimeConfigForEnv(env, "claude", platformRefTestCtx(PlatformReferenceFile))
		if err != nil {
			t.Fatalf("InjectRuntimeConfigForEnv: %v", err)
		}
		refPath := filepath.Join(env.WorkDir, ".multica", "platform.md")
		if _, err := os.Stat(refPath); err != nil {
			t.Fatalf("expected platform.md to be written: %v", err)
		}
		if !strings.Contains(brief, refPath) {
			t.Errorf("file-mode brief must point at the absolute platform.md path %q\n---\n%s", refPath, brief)
		}
		m, err := readSidecarManifest(env.RootDir)
		if err != nil {
			t.Fatalf("readSidecarManifest: %v", err)
		}
		if !m.hasFile(refPath) {
			t.Fatalf("manifest must record platform.md, got %+v", m)
		}
		if err := CleanupRuntimeConfig(env.WorkDir, "claude"); err != nil {
			t.Fatalf("CleanupRuntimeConfig: %v", err)
		}
		if err := CleanupSidecars(env.RootDir); err != nil {
			t.Fatalf("CleanupSidecars: %v", err)
		}
		if _, err := os.Stat(filepath.Join(env.WorkDir, ".multica")); !os.IsNotExist(err) {
			t.Errorf("expected the created .multica dir to be removed on cleanup, stat err=%v", err)
		}
	})

	t.Run("reinject_refreshes_owned_file_without_degrading", func(t *testing.T) {
		t.Parallel()
		env := newLocalEnv(t)
		ctx := platformRefTestCtx(PlatformReferenceFile)
		ctx.PriorSessionResumed = true
		if _, err := InjectRuntimeConfigForEnv(env, "claude", ctx); err != nil {
			t.Fatalf("first inject: %v", err)
		}
		// The fresh-session retry path re-injects with the resumed flag
		// cleared; the manifest-recorded file must be refreshed, not refused.
		ctx.PriorSessionResumed = false
		brief, err := InjectRuntimeConfigForEnv(env, "claude", ctx)
		if err != nil {
			t.Fatalf("re-inject: %v", err)
		}
		refPath := filepath.Join(env.WorkDir, ".multica", "platform.md")
		if !strings.Contains(brief, refPath) {
			t.Errorf("re-injected brief degraded to inline; must stay in file mode\n---\n%s", brief)
		}
		if onDisk, err := os.ReadFile(refPath); err != nil || string(onDisk) != buildPlatformReferenceContent(ctx) {
			t.Errorf("re-injected platform.md must carry the current rendering, err=%v", err)
		}
	})
}

// TestPlatformReferenceBriefSizeDelta documents the point of the split: the
// file-mode brief must be materially smaller than the inline brief for a
// representative issue-run context. Logs exact byte counts for release notes.
func TestPlatformReferenceBriefSizeDelta(t *testing.T) {
	t.Parallel()
	inline := buildMetaSkillContent("claude", platformRefTestCtx(PlatformReferenceInline))
	file := buildMetaSkillContent("claude", platformRefTestCtx(PlatformReferenceFile))
	ref := buildPlatformReferenceContent(platformRefTestCtx(PlatformReferenceFile))
	if len(file) >= len(inline) {
		t.Fatalf("file-mode brief (%d bytes) should be smaller than inline brief (%d bytes)", len(file), len(inline))
	}
	t.Logf("inline brief: %d bytes; file-mode brief: %d bytes (delta %d); platform.md: %d bytes",
		len(inline), len(file), len(inline)-len(file), len(ref))
}

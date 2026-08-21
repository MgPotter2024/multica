package execenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// PlatformReferenceMode selects where the static platform-mechanics sections
// of the runtime brief live (ARG-548 Phase 1, upstream #4043-style).
//
//   - inline: every section is rendered into the per-run brief
//     (CLAUDE.md / AGENTS.md) — the pre-Phase-1, byte-identical behavior.
//   - file: the low-risk static reference sections (full Available Commands
//     list, Knowledge Layers, Issue Metadata guidance, Attachments,
//     Repositories usage prose, Connected Apps) move to
//     `.multica/platform.md` in the task workdir, and the brief keeps a
//     mandatory one-line pointer plus the three core command lines.
//
// Behavior/safety contracts (Background Task Safety, Agent Identity,
// Workflow, Mentions, Comment Formatting, Output, the `multica issue
// deliver` contract, Always-Use-CLI, ...) NEVER move to the file — that
// split is a safety boundary, not a size optimization.
type PlatformReferenceMode string

const (
	// PlatformReferenceInline keeps the full brief inline (legacy behavior).
	PlatformReferenceInline PlatformReferenceMode = "inline"
	// PlatformReferenceFile externalizes static reference sections to
	// .multica/platform.md.
	PlatformReferenceFile PlatformReferenceMode = "file"

	// DefaultPlatformReference is the mode used when
	// MULTICA_PLATFORM_REFERENCE is unset. Rollback to the legacy brief is
	// MULTICA_PLATFORM_REFERENCE=inline.
	DefaultPlatformReference = PlatformReferenceFile

	// PlatformReferenceEnvVar is the daemon environment variable that
	// selects the mode. Parsed with validation at daemon startup
	// (LoadConfig) so a typo fails loudly instead of silently picking a
	// mode.
	PlatformReferenceEnvVar = "MULTICA_PLATFORM_REFERENCE"

	// PlatformReferenceRelPath is the workdir-relative path (slash form) of
	// the externalized platform reference file.
	PlatformReferenceRelPath = ".multica/platform.md"
)

// ParsePlatformReferenceMode validates a raw MULTICA_PLATFORM_REFERENCE
// value. Empty resolves to DefaultPlatformReference; anything other than
// "inline"/"file" (case-insensitive) is rejected.
func ParsePlatformReferenceMode(raw string) (PlatformReferenceMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return DefaultPlatformReference, nil
	case string(PlatformReferenceInline):
		return PlatformReferenceInline, nil
	case string(PlatformReferenceFile):
		return PlatformReferenceFile, nil
	default:
		return "", fmt.Errorf("%s: must be %q or %q, got %q", PlatformReferenceEnvVar, PlatformReferenceInline, PlatformReferenceFile, raw)
	}
}

// effectivePlatformReference resolves the mode for a task context.
// Resolution order: explicit ctx.PlatformReference (set by the daemon from
// its validated Config) → MULTICA_PLATFORM_REFERENCE from the process env →
// DefaultPlatformReference. An invalid env value falls back to inline —
// daemon startup already rejects it loudly via LoadConfig, so this path only
// exists for defense in depth, and inline is the byte-identical legacy brief
// that can never half-apply the split.
func effectivePlatformReference(ctx TaskContextForEnv) PlatformReferenceMode {
	switch ctx.PlatformReference {
	case PlatformReferenceInline, PlatformReferenceFile:
		return ctx.PlatformReference
	}
	mode, err := ParsePlatformReferenceMode(os.Getenv(PlatformReferenceEnvVar))
	if err != nil {
		return PlatformReferenceInline
	}
	return mode
}

// usePlatformReferenceFile reports whether this task's brief externalizes the
// static reference sections. Quick-create is always inline: its Available
// Commands section is already a minimal hard guardrail (exactly one allowed
// command), and pointing it at a file full of other commands would tempt the
// model to bend that guardrail.
func usePlatformReferenceFile(ctx TaskContextForEnv) bool {
	if classifyTask(ctx) == kindQuickCreate {
		return false
	}
	return effectivePlatformReference(ctx) == PlatformReferenceFile
}

// buildPlatformReferenceContent renders `.multica/platform.md`.
//
// Byte-stability contract: for the same workspace/agent configuration the
// output must be identical across runs — no timestamps, no run/task IDs, no
// issue-specific values. Every section below is either fully static
// (Available Commands, Repositories usage, Knowledge Layers, Issue Metadata,
// Attachments) or derived only from agent configuration (Connected Apps).
// TestPlatformReferenceContentByteStable pins this.
func buildPlatformReferenceContent(ctx TaskContextForEnv) string {
	var b strings.Builder
	b.WriteString("# Multica Platform Reference\n\n")
	b.WriteString("Static reference for the Multica platform: CLI command flags and platform mechanics. The runtime brief (the Multica-managed section of CLAUDE.md / AGENTS.md) carries the task workflow and behavior contracts; on any conflict the runtime brief wins.\n\n")
	writeAvailableCommands(&b)
	writePlatformRepositoriesUsage(&b)
	writeKnowledgeLayers(&b)
	writeIssueMetadata(&b)
	writeAttachments(&b)
	writeConnectedApps(&b, ctx)
	return b.String()
}

// writePlatformRepositoriesUsage emits the Repositories usage prose moved out
// of the brief in file mode. The dynamic repo URL list stays inline in the
// brief (it is task-context data, not static mechanics), so this section only
// documents how to fetch and points back at the brief for the list.
func writePlatformRepositoriesUsage(b *strings.Builder) {
	b.WriteString("## Repositories\n\n")
	b.WriteString("Fetch a workspace repository with `multica repo checkout <url> [--ref <branch-or-sha>]` — creates a git worktree on a dedicated branch. Add `--ref` when a task or handoff names an exact revision. The repo URLs available to the current task are listed in the runtime brief's Repositories section.\n\n")
}

// platformReferencePath returns the absolute path of the platform reference
// file under workDir.
func platformReferencePath(workDir string) string {
	return filepath.Join(workDir, filepath.FromSlash(PlatformReferenceRelPath))
}

// writePlatformReferenceFile writes `.multica/platform.md` into the workdir,
// overwriting any previous copy so reused workdirs always carry the current
// rendering. The `.multica` directory usually already exists (the daemon task
// marker lives there); MkdirAll is defensive for prompt-only providers whose
// Prepare path may not have created it yet.
func writePlatformReferenceFile(workDir string, ctx TaskContextForEnv) error {
	path := platformReferencePath(workDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create platform reference dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(buildPlatformReferenceContent(ctx)), 0o644); err != nil {
		return fmt.Errorf("write platform reference: %w", err)
	}
	return nil
}

// removePlatformReferenceFile deletes `.multica/platform.md` from the
// workdir. Missing file is a no-op — the task may have run in inline mode or
// as quick-create. Called from CleanupRuntimeConfig BEFORE CleanupSidecars so
// the manifest-driven rmdir of `.multica` in the local_directory flow finds
// the directory empty of Multica-owned files.
func removePlatformReferenceFile(workDir string) error {
	if err := os.Remove(platformReferencePath(workDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove platform reference: %w", err)
	}
	return nil
}

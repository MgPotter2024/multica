---
title: "feat: Add a Twenty Work Request action to Multica issues"
type: feat
date: 2026-07-12
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
---

# feat: Add a Twenty Work Request action to Multica issues

## Goal Capsule

- Show an `Open in Twenty` action when an issue carries trusted Argos CRM metadata.
- Open the exact Work Request in a new browser tab or the operating system browser.
- Hide the action for ordinary Multica issues and malformed or untrusted metadata.
- Preserve the raw metadata dialog and all existing issue actions.

## Product Contract

### Requirements

- R1. Read only the bridge-owned `argos_twenty_record_url` metadata key.
- R2. Accept only HTTPS URLs on `crm.aiparis.org` whose path begins with
  `/object/workRequest/`.
- R3. Render a visible, keyboard-accessible action in the issue detail sidebar.
- R4. Use the existing cross-platform external-opening helper so web and Electron behavior remain
  consistent.
- R5. Hide the action for missing, non-string, malformed, non-HTTPS, wrong-host, or wrong-route
  values.
- R6. Do not expose any other metadata value or change metadata editing behavior.
- R7. Provide English, Simplified Chinese, Japanese, and Korean labels through the existing locale
  system.

### Acceptance Examples

- AE1. A linked Argos issue shows `Open in Twenty`; activating it opens the exact CRM URL.
- AE2. A normal issue with empty metadata has no CRM action.
- AE3. An issue with `javascript:`, an attacker host, or an unrelated CRM route has no action.
- AE4. The existing Metadata button still opens the complete formatted JSON dialog.

### Scope Boundaries

- No general-purpose arbitrary metadata link renderer.
- No Infisical secret link is inferred from issue metadata.
- No app-level SSO, cookie sharing, or authentication bypass.
- No change to issue creation, status transitions, comments, or permissions.

## Planning Contract

### Key Technical Decisions

- KTD1. Add a small pure URL parser beside the issue detail component so the allowlist can be
  tested without rendering the entire page.
- KTD2. Match both origin and route. The metadata key alone is not a sufficient trust boundary
  because workspace members and agents can update metadata.
- KTD3. Use `openExternal` and a Lucide external-link icon to match existing web/Electron behavior
  and UI conventions.
- KTD4. Keep the action in the detail sidebar above raw Metadata because it is a human workflow
  command, not agent-facing diagnostic data.

## Implementation Units

### U1. Validate the bridge-owned Twenty URL

- **Requirements:** R1, R2, R5.
- **Files:** `packages/views/issues/utils/twenty-work-request-url.ts`,
  `packages/views/issues/utils/twenty-work-request-url.test.ts`.
- **Approach:** Parse the metadata value with `URL`, require the exact HTTPS origin and Work Request
  route prefix, and return a normalized URL or `null`.
- **Execution note:** Write the table-driven tests before the parser.
- **Test scenarios:** valid UUID route, query/hash preservation, missing value, non-string value,
  malformed URL, HTTP, user-info URL, lookalike hostname, wrong route, and prefix confusion.
- **Verification:** Run the focused utility test.
- **Dependencies:** None.

### U2. Render the cross-platform issue action

- **Requirements:** R3, R4, R6, R7.
- **Files:** `packages/views/issues/components/issue-detail.tsx`,
  `packages/views/issues/components/issue-detail.test.tsx`, `packages/views/locales/en/issues.json`,
  `packages/views/locales/zh-Hans/issues.json`, `packages/views/locales/ja/issues.json`,
  `packages/views/locales/ko/issues.json`.
- **Approach:** Derive the validated URL from issue metadata, render a compact outline button only
  when valid, and call `openExternal` on activation. Keep stable dimensions and existing sidebar
  spacing on desktop and mobile sheets.
- **Execution note:** Add failing visibility and activation tests before the component change.
- **Test scenarios:** visible valid action, hidden missing action, hidden invalid action, one
  `openExternal` call with the normalized URL, raw Metadata button unchanged, keyboard-accessible
  button name, and long localized labels without overflow.
- **Verification:** Run focused issue-detail tests, view package typecheck, and production build.
- **Dependencies:** U1.

## Risks And Dependencies

- The bridge must populate `argos_twenty_record_url`; deployment order remains backward-compatible.
- Hard allowlisting `crm.aiparis.org` intentionally makes this a company-fork feature rather than a
  generic upstream metadata-link capability.
- The action opens a new context, so `openExternal` must retain its existing `noopener,noreferrer`
  behavior on web.

## Verification Contract

| Scope | Command | Done signal |
|---|---|---|
| URL parser | Focused Vitest file | All allowlist and rejection cases pass |
| Issue detail | Focused issue-detail tests | Visibility and external open pass |
| Type safety | Views/app typecheck | No TypeScript errors |
| Build | Production web build | Multica web compiles |
| Browser desktop | Local issue fixture | Action is visible and stable |
| Browser mobile | Local issue fixture | Action fits the sidebar sheet and opens correctly |

## Definition Of Done

- Linked issues show a safe `Open in Twenty` action on desktop and mobile.
- Unlinked or untrusted metadata never produces an external action.
- Existing issue metadata, status, comments, and action behavior are unchanged.
- The release image is built from the reviewed production commit and verified by digest.

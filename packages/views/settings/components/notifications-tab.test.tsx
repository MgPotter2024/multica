import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { NotificationPreferenceResponse } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({
  mutate: vi.fn(),
  isPending: false,
  response: {
    workspace_id: "workspace-1",
    preferences: {},
  } as NotificationPreferenceResponse,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: mocks.response }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/notification-preferences/queries", () => ({
  notificationPreferenceOptions: () => ({ queryKey: ["notification-preferences"] }),
}));

vi.mock("@multica/core/notification-preferences/mutations", () => ({
  useUpdateNotificationPreferences: () => ({
    mutate: mocks.mutate,
    isPending: mocks.isPending,
  }),
}));

vi.mock("./browser-notification-setting", () => ({
  BrowserNotificationSetting: () => null,
}));

import { NotificationsTab } from "./notifications-tab";

describe("NotificationsTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.response = {
      workspace_id: "workspace-1",
      preferences: {},
    };
    mocks.isPending = false;
  });

  it("enables Mentions-only mode without discarding event preferences", async () => {
    mocks.response.preferences = { comments: "muted" };
    const user = userEvent.setup();
    renderWithI18n(<NotificationsTab />);

    await user.click(screen.getByRole("switch", { name: "Only direct @mentions" }));

    expect(mocks.mutate).toHaveBeenCalledWith(
      { comments: "muted", inbox_mode: "mentions_only" },
      expect.objectContaining({ onError: expect.any(Function) }),
    );
  });

  it("disables ordinary event groups while preserving system delivery", () => {
    mocks.response.preferences = { inbox_mode: "mentions_only" };
    renderWithI18n(<NotificationsTab />);

    expect(screen.getByRole("switch", { name: "Only direct @mentions" })).toBeChecked();
    for (const name of [
      "Assignments",
      "Status changes",
      "Comments & Mentions",
      "Priority & Due date",
      "Agent activity",
    ]) {
      expect(screen.getByRole("switch", { name })).toHaveAttribute("aria-disabled", "true");
    }
    expect(screen.getByRole("switch", { name: "Show system notifications" })).not.toHaveAttribute(
      "aria-disabled",
    );
  });

  it("returns to the existing event-group preferences when disabled", async () => {
    mocks.response.preferences = {
      inbox_mode: "mentions_only",
      assignments: "muted",
    };
    const user = userEvent.setup();
    renderWithI18n(<NotificationsTab />);

    await user.click(screen.getByRole("switch", { name: "Only direct @mentions" }));

    expect(mocks.mutate).toHaveBeenCalledWith(
      { assignments: "muted" },
      expect.objectContaining({ onError: expect.any(Function) }),
    );
  });

  it("prevents overlapping full-preference updates while a save is pending", () => {
    mocks.isPending = true;
    renderWithI18n(<NotificationsTab />);

    for (const name of [
      "Only direct @mentions",
      "Assignments",
      "Status changes",
      "Comments & Mentions",
      "Priority & Due date",
      "Agent activity",
      "Show system notifications",
    ]) {
      expect(screen.getByRole("switch", { name })).toHaveAttribute("aria-disabled", "true");
    }
  });
});

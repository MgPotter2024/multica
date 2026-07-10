import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({
  reconcile: vi.fn(),
  enable: vi.fn(),
  test: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock("@multica/core/platform", () => ({
  initialWebPushState: () => ({
    status: "permission-required",
    permission: "default",
    subscribed: false,
  }),
  reconcileWebPushSubscription: mocks.reconcile,
  enableWebPushSubscription: mocks.enable,
  sendWebPushTest: mocks.test,
}));

vi.mock("../../platform", () => ({ isDesktopShell: () => false }));
vi.mock("sonner", () => ({
  toast: { success: mocks.success, error: mocks.error },
}));

import { BrowserNotificationSetting } from "./browser-notification-setting";

const subscribed = {
  status: "subscribed" as const,
  permission: "granted" as const,
  subscribed: true,
};

describe("BrowserNotificationSetting", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.reconcile.mockResolvedValue(subscribed);
    mocks.enable.mockResolvedValue(subscribed);
    mocks.test.mockResolvedValue(undefined);
  });

  it("shows the effective durable subscription state", async () => {
    renderWithI18n(<BrowserNotificationSetting />);

    expect(await screen.findByText("Enabled")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send test" })).toBeInTheDocument();
    expect(mocks.reconcile).toHaveBeenCalledTimes(1);
  });

  it("enables Push from the user gesture and updates the status", async () => {
    mocks.reconcile.mockResolvedValueOnce({
      status: "permission-required",
      permission: "default",
      subscribed: false,
    });
    const user = userEvent.setup();
    renderWithI18n(<BrowserNotificationSetting />);

    await user.click(await screen.findByRole("button", { name: "Enable" }));

    await waitFor(() => expect(mocks.enable).toHaveBeenCalledTimes(1));
    expect(await screen.findByText("Enabled")).toBeInTheDocument();
  });

  it("sends a user-initiated test notification", async () => {
    const user = userEvent.setup();
    renderWithI18n(<BrowserNotificationSetting />);

    await user.click(await screen.findByRole("button", { name: "Send test" }));

    await waitFor(() => expect(mocks.test).toHaveBeenCalledTimes(1));
    expect(mocks.success).toHaveBeenCalledWith("Test notification sent");
  });

  it("reports a failed test without dropping the active state", async () => {
    mocks.test.mockRejectedValueOnce(new Error("network"));
    const user = userEvent.setup();
    renderWithI18n(<BrowserNotificationSetting />);

    await user.click(await screen.findByRole("button", { name: "Send test" }));

    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("network"));
    expect(screen.getByText("Enabled")).toBeInTheDocument();
  });

  it("does not offer enable when the server has Web Push disabled", async () => {
    mocks.reconcile.mockResolvedValueOnce({
      status: "server-disabled",
      permission: "default",
      subscribed: false,
    });
    renderWithI18n(<BrowserNotificationSetting />);

    expect(
      await screen.findByText(
        "Background notifications are not configured on this server. Open-tab notifications remain available.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Enable" })).not.toBeInTheDocument();
  });
});

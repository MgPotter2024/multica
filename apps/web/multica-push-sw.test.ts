import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";

type Listener = (event: Record<string, unknown>) => void;

function loadWorker(windowClients: Array<Record<string, unknown>> = []) {
  const listeners = new Map<string, Listener>();
  const showNotification = vi.fn().mockResolvedValue(undefined);
  const openWindow = vi.fn().mockResolvedValue(undefined);
  const worker = {
    location: { origin: "https://multica.example.test" },
    registration: { showNotification },
    clients: {
      matchAll: vi.fn().mockResolvedValue(windowClients),
      openWindow,
    },
    addEventListener: (name: string, listener: Listener) => {
      listeners.set(name, listener);
    },
  };
  const source = readFileSync(
    resolve(process.cwd(), "public/multica-push-sw.js"),
    "utf8",
  );
  Function("self", source)(worker);
  return { listeners, showNotification, openWindow };
}

async function dispatch(
  listener: Listener | undefined,
  event: Record<string, unknown>,
) {
  let pending: Promise<unknown> | undefined;
  listener?.({
    ...event,
    waitUntil: (promise: Promise<unknown>) => {
      pending = promise;
    },
  });
  await pending;
}

const payload = {
  title: "Mentioned you",
  body: "in a comment",
  url: "/workspace-a/inbox?issue=issue-1",
  tag: "item-1",
};

describe("multica-push-sw", () => {
  it("suppresses a banner when any same-origin window is focused", async () => {
    const { listeners, showNotification } = loadWorker([{ focused: true }]);

    await dispatch(listeners.get("push"), {
      data: { json: () => payload },
    });

    expect(showNotification).not.toHaveBeenCalled();
  });

  it("shows an explicit test banner even when a window is focused", async () => {
    const { listeners, showNotification } = loadWorker([{ focused: true }]);

    await dispatch(listeners.get("push"), {
      data: { json: () => ({ ...payload, test: true }) },
    });

    expect(showNotification).toHaveBeenCalledTimes(1);
  });

  it("shows a banner when no window is focused", async () => {
    const { listeners, showNotification } = loadWorker([{ focused: false }]);

    await dispatch(listeners.get("push"), {
      data: { json: () => payload },
    });

    expect(showNotification).toHaveBeenCalledWith("Mentioned you", {
      body: "in a comment",
      tag: "item-1",
      data: { url: "https://multica.example.test/workspace-a/inbox?issue=issue-1" },
    });
  });

  it("ignores malformed and cross-origin payloads", async () => {
    const { listeners, showNotification } = loadWorker([]);

    await dispatch(listeners.get("push"), {
      data: { json: () => ({ ...payload, url: "https://evil.example/phish" }) },
    });
    await dispatch(listeners.get("push"), {
      data: { json: () => ({ body: "missing title" }) },
    });

    expect(showNotification).not.toHaveBeenCalled();
  });

  it("focuses and navigates an existing client on click", async () => {
    const focus = vi.fn().mockResolvedValue(undefined);
    const navigate = vi.fn().mockResolvedValue(undefined);
    const { listeners, openWindow } = loadWorker([
      { focused: false, url: "https://multica.example.test/other", focus, navigate },
    ]);
    const close = vi.fn();

    await dispatch(listeners.get("notificationclick"), {
      notification: {
        data: { url: "https://multica.example.test/workspace-a/inbox?issue=issue-1" },
        close,
      },
    });

    expect(close).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith(
      "https://multica.example.test/workspace-a/inbox?issue=issue-1",
    );
    expect(focus).toHaveBeenCalledTimes(1);
    expect(openWindow).not.toHaveBeenCalled();
  });

  it("opens a new window when no client exists", async () => {
    const { listeners, openWindow } = loadWorker([]);

    await dispatch(listeners.get("notificationclick"), {
      notification: {
        data: { url: "https://multica.example.test/workspace-a/inbox?issue=issue-1" },
        close: vi.fn(),
      },
    });

    expect(openWindow).toHaveBeenCalledWith(
      "https://multica.example.test/workspace-a/inbox?issue=issue-1",
    );
  });
});

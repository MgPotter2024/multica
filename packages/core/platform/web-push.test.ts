import { afterEach, describe, expect, it, vi } from "vitest";
import type { ApiClient } from "../api/client";
import { createAuthStore } from "../auth/store";
import type { StorageAdapter } from "../types";
import {
  base64UrlToUint8Array,
  cleanupWebPushOnLogout,
  enableWebPushSubscription,
  hasActiveWebPushSubscription,
  reconcileWebPushSubscription,
} from "./web-push";

class FakeNotification {
  static permission: NotificationPermission = "granted";
  static requestPermission = vi.fn(async () => FakeNotification.permission);
}

function makeSubscription(
  applicationServerKey: Uint8Array<ArrayBuffer>,
  overrides: Partial<PushSubscription> = {},
): PushSubscription {
  return {
    endpoint: "https://push.example.test/subscription/1",
    expirationTime: null,
    options: {
      applicationServerKey: applicationServerKey.buffer,
      userVisibleOnly: true,
    },
    getKey: (name: PushEncryptionKeyName) =>
      name === "p256dh"
        ? new Uint8Array([1, 2]).buffer
        : new Uint8Array([3, 4]).buffer,
    toJSON: () => ({}),
    unsubscribe: vi.fn().mockResolvedValue(true),
    ...overrides,
  };
}

function makeRegistration(
  subscription: PushSubscription | null,
  subscribe = vi.fn<PushManager["subscribe"]>(),
) {
  const pushManager = {
    getSubscription: vi.fn().mockResolvedValue(subscription),
    permissionState: vi.fn(),
    subscribe,
  } as unknown as PushManager;
  return {
    pushManager,
    registration: { pushManager } as ServiceWorkerRegistration,
  };
}

function installPushBrowser(
  subscription: PushSubscription | null,
  subscribe = vi.fn<PushManager["subscribe"]>(),
  options: {
    registered?: ServiceWorkerRegistration;
    ready?: Promise<ServiceWorkerRegistration>;
    persisted?: ServiceWorkerRegistration | null;
  } = {},
) {
  const { pushManager, registration } = makeRegistration(
    subscription,
    subscribe,
  );
  const registered = options.registered ?? registration;
  const ready = options.ready ?? Promise.resolve(registration);
  const persisted =
    options.persisted === undefined ? registration : options.persisted;
  const register = vi.fn().mockResolvedValue(registered);
  const getRegistration = vi.fn().mockResolvedValue(persisted);
  vi.stubGlobal("window", {
    Notification: FakeNotification,
    PushManager: class {},
  });
  vi.stubGlobal("navigator", {
    serviceWorker: { register, ready, getRegistration },
  });
  return { pushManager, register, registration, getRegistration };
}

function makeApi(overrides: Partial<ApiClient> = {}): ApiClient {
  return {
    getWebPushConfig: vi.fn().mockResolvedValue({
      enabled: true,
      publicKey: "BAECAw",
    }),
    upsertWebPushSubscription: vi.fn().mockResolvedValue(undefined),
    deleteWebPushSubscription: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  } as unknown as ApiClient;
}

afterEach(async () => {
  await cleanupWebPushOnLogout(makeApi());
  FakeNotification.permission = "granted";
  FakeNotification.requestPermission.mockClear();
  vi.unstubAllGlobals();
});

describe("base64UrlToUint8Array", () => {
  it("decodes unpadded URL-safe base64", () => {
    expect(Array.from(base64UrlToUint8Array("AQID-v8"))).toEqual([
      1, 2, 3, 250, 255,
    ]);
  });
});

describe("reconcileWebPushSubscription", () => {
  it("waits for the first service worker to activate before using PushManager", async () => {
    let activate: ((registration: ServiceWorkerRegistration) => void) | undefined;
    const ready = new Promise<ServiceWorkerRegistration>((resolve) => {
      activate = resolve;
    });
    const installing = makeRegistration(null);
    const subscription = makeSubscription(new Uint8Array([4, 1, 2, 3]));
    const subscribe = vi.fn().mockResolvedValue(subscription);
    const active = makeRegistration(null, subscribe);
    const { register } = installPushBrowser(null, vi.fn(), {
      registered: installing.registration,
      ready,
      persisted: active.registration,
    });
    const reconciliation = reconcileWebPushSubscription(makeApi());

    await vi.waitFor(() => expect(register).toHaveBeenCalledTimes(1));
    expect(installing.pushManager.getSubscription).not.toHaveBeenCalled();
    expect(active.pushManager.getSubscription).not.toHaveBeenCalled();
    activate?.(active.registration);

    await expect(reconciliation).resolves.toMatchObject({
      status: "subscribed",
    });
    expect(active.pushManager.getSubscription).toHaveBeenCalledTimes(1);
    expect(subscribe).toHaveBeenCalledTimes(1);
  });

  it("resyncs an existing matching browser subscription to the backend", async () => {
    const subscription = makeSubscription(new Uint8Array([4, 1, 2, 3]));
    const { pushManager, register } = installPushBrowser(subscription);
    const api = makeApi();

    await expect(reconcileWebPushSubscription(api)).resolves.toMatchObject({
      status: "subscribed",
      subscribed: true,
    });

    expect(register).toHaveBeenCalledWith("/multica-push-sw.js", { scope: "/" });
    expect(pushManager.subscribe).not.toHaveBeenCalled();
    expect(api.upsertWebPushSubscription).toHaveBeenCalledWith({
      endpoint: subscription.endpoint,
      keys: { p256dh: "AQI", auth: "AwQ" },
    });
    expect(hasActiveWebPushSubscription()).toBe(true);
  });

  it("renews a subscription when the VAPID public key changes", async () => {
    const stale = makeSubscription(new Uint8Array([4, 9, 9, 9]));
    const renewed = makeSubscription(new Uint8Array([4, 1, 2, 3]), {
      endpoint: "https://push.example.test/subscription/2",
    });
    const subscribe = vi.fn().mockResolvedValue(renewed);
    const { pushManager } = installPushBrowser(stale, subscribe);
    const api = makeApi();

    await reconcileWebPushSubscription(api);

    expect(stale.unsubscribe).toHaveBeenCalledTimes(1);
    expect(pushManager.subscribe).toHaveBeenCalledWith({
      userVisibleOnly: true,
      applicationServerKey: new Uint8Array([4, 1, 2, 3]),
    });
    expect(api.upsertWebPushSubscription).toHaveBeenCalledWith(
      expect.objectContaining({ endpoint: renewed.endpoint }),
    );
  });

  it("renews a subscription whose application server key is unavailable", async () => {
    const stale = makeSubscription(new Uint8Array([4, 1, 2, 3]), {
      options: { applicationServerKey: null, userVisibleOnly: true },
    });
    const renewed = makeSubscription(new Uint8Array([4, 1, 2, 3]), {
      endpoint: "https://push.example.test/subscription/2",
    });
    const subscribe = vi.fn().mockResolvedValue(renewed);
    const { pushManager } = installPushBrowser(stale, subscribe);

    await reconcileWebPushSubscription(makeApi());

    expect(stale.unsubscribe).toHaveBeenCalledTimes(1);
    expect(pushManager.subscribe).toHaveBeenCalledTimes(1);
  });

  it("does not prompt or register while permission is undecided", async () => {
    FakeNotification.permission = "default";
    const { register } = installPushBrowser(null);
    const api = makeApi();

    await expect(reconcileWebPushSubscription(api)).resolves.toMatchObject({
      status: "permission-required",
      subscribed: false,
    });

    expect(FakeNotification.requestPermission).not.toHaveBeenCalled();
    expect(register).not.toHaveBeenCalled();
  });

  it("serializes concurrent dashboard and settings reconciliation", async () => {
    const subscription = makeSubscription(new Uint8Array([4, 1, 2, 3]));
    const { register } = installPushBrowser(subscription);
    const api = makeApi();

    await Promise.all([
      reconcileWebPushSubscription(api),
      reconcileWebPushSubscription(api),
    ]);

    expect(register).toHaveBeenCalledTimes(1);
    expect(api.upsertWebPushSubscription).toHaveBeenCalledTimes(1);
  });

  it("lets logout win while backend reconciliation is pending", async () => {
    let finishUpsert: (() => void) | undefined;
    const upsert = vi.fn(
      () => new Promise<void>((resolve) => {
        finishUpsert = resolve;
      }),
    );
    const subscription = makeSubscription(new Uint8Array([4, 1, 2, 3]));
    installPushBrowser(subscription);
    const api = makeApi({ upsertWebPushSubscription: upsert });

    const reconciliation = reconcileWebPushSubscription(api);
    await vi.waitFor(() => expect(upsert).toHaveBeenCalledTimes(1));
    cleanupWebPushOnLogout(api);
    finishUpsert?.();

    await expect(reconciliation).resolves.toMatchObject({
      status: "fallback",
      subscribed: false,
    });
    expect(subscription.unsubscribe).toHaveBeenCalled();
    expect(hasActiveWebPushSubscription()).toBe(false);
  });
});

describe("enableWebPushSubscription", () => {
  it("requests permission before creating the subscription", async () => {
    FakeNotification.permission = "default";
    FakeNotification.requestPermission.mockImplementationOnce(async () => {
      FakeNotification.permission = "granted";
      return "granted";
    });
    const subscription = makeSubscription(new Uint8Array([4, 1, 2, 3]));
    const subscribe = vi.fn().mockResolvedValue(subscription);
    installPushBrowser(null, subscribe);

    await expect(enableWebPushSubscription(makeApi())).resolves.toMatchObject({
      status: "subscribed",
    });
    expect(FakeNotification.requestPermission).toHaveBeenCalledTimes(1);
    expect(subscribe).toHaveBeenCalledTimes(1);
  });
});

describe("cleanupWebPushOnLogout", () => {
  it("discovers and removes a persisted subscription when module memory is empty", async () => {
    const subscription = makeSubscription(new Uint8Array([4, 1, 2, 3]));
    const { getRegistration } = installPushBrowser(subscription);
    const api = makeApi();

    await cleanupWebPushOnLogout(api);

    expect(getRegistration).toHaveBeenCalledWith("/");
    expect(api.deleteWebPushSubscription).toHaveBeenCalledWith(
      subscription.endpoint,
    );
    expect(subscription.unsubscribe).toHaveBeenCalledTimes(1);
  });

  it("deletes the backend binding and unsubscribes the cached browser endpoint", async () => {
    const subscription = makeSubscription(new Uint8Array([4, 1, 2, 3]));
    installPushBrowser(subscription);
    const api = makeApi();
    await reconcileWebPushSubscription(api);

    await cleanupWebPushOnLogout(api);
    expect(api.deleteWebPushSubscription).toHaveBeenCalledWith(
      subscription.endpoint,
    );
    expect(subscription.unsubscribe).toHaveBeenCalledTimes(1);
    expect(hasActiveWebPushSubscription()).toBe(false);
  });

  it("issues deletion while the auth store still has valid credentials", async () => {
    let authenticated = true;
    let deleteSawValidAuth = false;
    const subscription = makeSubscription(new Uint8Array([4, 1, 2, 3]));
    installPushBrowser(subscription);
    const api = makeApi({
      deleteWebPushSubscription: vi.fn().mockImplementation(async () => {
        deleteSawValidAuth = authenticated;
      }),
      setToken: vi.fn().mockImplementation((token: string | null) => {
        if (token === null) authenticated = false;
      }),
    });
    await reconcileWebPushSubscription(api);
    const storage: StorageAdapter = {
      getItem: () => "active-token",
      setItem: vi.fn(),
      removeItem: vi.fn(),
    };
    const store = createAuthStore({
      api,
      storage,
      onBeforeLogout: () => cleanupWebPushOnLogout(api),
    });

    await store.getState().logout();

    expect(deleteSawValidAuth).toBe(true);
    expect(api.setToken).toHaveBeenCalledWith(null);
  });
});

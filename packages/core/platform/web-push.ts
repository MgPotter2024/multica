"use client";

import { api } from "../api";
import type { ApiClient } from "../api/client";
import type { WebPushSubscriptionInput } from "../types";
import {
  getWebNotificationPermission,
  isWebNotificationSupported,
  requestWebNotificationPermission,
  type WebNotificationPermission,
} from "./system-notification";

export type WebPushStatus =
  | "unsupported"
  | "fallback"
  | "server-disabled"
  | "permission-required"
  | "permission-denied"
  | "subscribed"
  | "error";

export interface WebPushState {
  status: WebPushStatus;
  permission: WebNotificationPermission;
  subscribed: boolean;
}

let activeSubscription: PushSubscription | null = null;
let pendingSubscription: PushSubscription | null = null;
let subscriptionGeneration = 0;
let reconciliationPromise: Promise<WebPushState> | null = null;

export function base64UrlToUint8Array(
  value: string,
): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (value.length % 4)) % 4);
  const base64 = (value + padding).replace(/-/g, "+").replace(/_/g, "/");
  const decoded = atob(base64);
  const buffer = new ArrayBuffer(decoded.length);
  const bytes = new Uint8Array(buffer);
  for (let index = 0; index < decoded.length; index += 1) {
    bytes[index] = decoded.charCodeAt(index);
  }
  return bytes;
}

function arrayBufferToBase64Url(value: ArrayBuffer): string {
  const bytes = new Uint8Array(value);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

function sameBytes(left: BufferSource | null, right: Uint8Array): boolean {
  if (!left) return false;
  const leftBytes = ArrayBuffer.isView(left)
    ? new Uint8Array(left.buffer, left.byteOffset, left.byteLength)
    : new Uint8Array(left);
  return (
    leftBytes.length === right.length &&
    leftBytes.every((byte, index) => byte === right[index])
  );
}

function serializeSubscription(
  subscription: PushSubscription,
): WebPushSubscriptionInput {
  const p256dh = subscription.getKey("p256dh");
  const auth = subscription.getKey("auth");
  if (!subscription.endpoint || !p256dh || !auth) {
    throw new Error("Browser returned an incomplete Push subscription");
  }
  return {
    endpoint: subscription.endpoint,
    keys: {
      p256dh: arrayBufferToBase64Url(p256dh),
      auth: arrayBufferToBase64Url(auth),
    },
  };
}

export function isWebPushSupported(): boolean {
  return (
    isWebNotificationSupported() &&
    typeof navigator !== "undefined" &&
    "serviceWorker" in navigator &&
    typeof window !== "undefined" &&
    "PushManager" in window
  );
}

function stateForPermission(
  permission: WebNotificationPermission,
): WebPushState {
  if (permission === "unsupported") {
    return { status: "unsupported", permission, subscribed: false };
  }
  if (permission === "denied") {
    return { status: "permission-denied", permission, subscribed: false };
  }
  return {
    status: permission === "granted" ? "fallback" : "permission-required",
    permission,
    subscribed: false,
  };
}

export function initialWebPushState(): WebPushState {
  return stateForPermission(getWebNotificationPermission());
}

export function hasActiveWebPushSubscription(): boolean {
  return activeSubscription !== null;
}

async function performReconciliation(apiClient: ApiClient): Promise<WebPushState> {
  const generation = subscriptionGeneration;
  const permission = getWebNotificationPermission();
  if (!isWebPushSupported()) return stateForPermission(permission);

  let candidateSubscription: PushSubscription | null = null;
  try {
    const config = await apiClient.getWebPushConfig();
    if (generation !== subscriptionGeneration) {
      return stateForPermission(getWebNotificationPermission());
    }
    if (config.enabled !== true) {
      activeSubscription = null;
      return { status: "server-disabled", permission, subscribed: false };
    }
    if (!config.publicKey) {
      activeSubscription = null;
      return { status: "error", permission, subscribed: false };
    }
    if (permission === "denied") {
      activeSubscription = null;
      return { status: "permission-denied", permission, subscribed: false };
    }
    if (permission !== "granted") {
      activeSubscription = null;
      return { status: "permission-required", permission, subscribed: false };
    }

    const applicationServerKey = base64UrlToUint8Array(config.publicKey);
    await navigator.serviceWorker.register(
      "/multica-push-sw.js",
      { scope: "/" },
    );
    if (generation !== subscriptionGeneration) {
      return stateForPermission(getWebNotificationPermission());
    }
    const registration = await navigator.serviceWorker.ready;
    if (generation !== subscriptionGeneration) {
      return stateForPermission(getWebNotificationPermission());
    }
    let subscription = await registration.pushManager.getSubscription();
    if (generation !== subscriptionGeneration) {
      if (subscription) await subscription.unsubscribe().catch(() => false);
      return stateForPermission(getWebNotificationPermission());
    }

    if (
      subscription &&
      !sameBytes(
        subscription.options.applicationServerKey,
        applicationServerKey,
      )
    ) {
      const staleEndpoint = subscription.endpoint;
      await Promise.allSettled([
        apiClient.deleteWebPushSubscription(staleEndpoint),
        subscription.unsubscribe(),
      ]);
      subscription = null;
      activeSubscription = null;
    }

    if (generation !== subscriptionGeneration) {
      return stateForPermission(getWebNotificationPermission());
    }

    subscription ??= await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey,
    });
    if (generation !== subscriptionGeneration) {
      await subscription.unsubscribe().catch(() => false);
      return stateForPermission(getWebNotificationPermission());
    }
    candidateSubscription = subscription;
    pendingSubscription = subscription;
    await apiClient.upsertWebPushSubscription(serializeSubscription(subscription));
    if (generation !== subscriptionGeneration) {
      if (pendingSubscription === subscription) pendingSubscription = null;
      void subscription.unsubscribe().catch(() => {});
      return stateForPermission(getWebNotificationPermission());
    }
    activeSubscription = subscription;
    pendingSubscription = null;
    return { status: "subscribed", permission, subscribed: true };
  } catch {
    if (pendingSubscription === candidateSubscription) pendingSubscription = null;
    return {
      status: "error",
      permission,
      subscribed: hasActiveWebPushSubscription(),
    };
  }
}

export function reconcileWebPushSubscription(
  apiClient: ApiClient = api,
): Promise<WebPushState> {
  if (reconciliationPromise) return reconciliationPromise;
  const current = performReconciliation(apiClient).finally(() => {
    if (reconciliationPromise === current) reconciliationPromise = null;
  });
  reconciliationPromise = current;
  return current;
}

export async function enableWebPushSubscription(
  apiClient: ApiClient = api,
): Promise<WebPushState> {
  const permission = await requestWebNotificationPermission();
  if (permission !== "granted") return stateForPermission(permission);
  return reconcileWebPushSubscription(apiClient);
}

async function getPersistedSubscription(): Promise<PushSubscription | null> {
  if (
    typeof navigator === "undefined" ||
    !("serviceWorker" in navigator) ||
    typeof navigator.serviceWorker.getRegistration !== "function"
  ) {
    return null;
  }
  try {
    const registration = await navigator.serviceWorker.getRegistration("/");
    return (await registration?.pushManager.getSubscription()) ?? null;
  } catch {
    return null;
  }
}

export async function cleanupWebPushOnLogout(
  apiClient: ApiClient = api,
): Promise<void> {
  subscriptionGeneration += 1;
  reconciliationPromise = null;
  const subscriptions = new Map<string, PushSubscription>();
  for (const subscription of [activeSubscription, pendingSubscription]) {
    if (subscription) subscriptions.set(subscription.endpoint, subscription);
  }
  activeSubscription = null;
  pendingSubscription = null;

  const persistedSubscription = await getPersistedSubscription();
  if (persistedSubscription) {
    subscriptions.set(persistedSubscription.endpoint, persistedSubscription);
  }

  await Promise.all(
    Array.from(subscriptions.values(), async (subscription) => {
      await Promise.allSettled([
        apiClient.deleteWebPushSubscription(subscription.endpoint),
        subscription.unsubscribe(),
      ]);
    }),
  );
}

export async function sendWebPushTest(apiClient: ApiClient = api): Promise<void> {
  await apiClient.testWebPushSubscription();
}

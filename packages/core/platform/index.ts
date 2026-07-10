export { CoreProvider } from "./core-provider";
export type { CoreProviderProps, ClientIdentity } from "./types";
export { AuthInitializer } from "./auth-initializer";
export { defaultStorage } from "./storage";
export { createPersistStorage } from "./persist-storage";
export { createWorkspaceAwareStorage, setCurrentWorkspace, getCurrentSlug, getCurrentWsId, subscribeToCurrentSlug, registerForWorkspaceRehydration } from "./workspace-storage";
export { clearWorkspaceStorage } from "./storage-cleanup";
export { isMac, modKey, enterKey, formatShortcut } from "./keyboard";
export {
  registerSystemNotificationClickHandler,
  isWebNotificationSupported,
  getWebNotificationPermission,
  requestWebNotificationPermission,
  showWebNotification,
  type SystemNotificationPayload,
  type WebNotificationPermission,
} from "./system-notification";
export {
  base64UrlToUint8Array,
  cleanupWebPushOnLogout,
  enableWebPushSubscription,
  hasActiveWebPushSubscription,
  initialWebPushState,
  isWebPushSupported,
  reconcileWebPushSubscription,
  sendWebPushTest,
  type WebPushState,
  type WebPushStatus,
} from "./web-push";

"use client";

import { useEffect, useState } from "react";
import { Bell, RotateCw, Send } from "lucide-react";
import {
  enableWebPushSubscription,
  initialWebPushState,
  reconcileWebPushSubscription,
  sendWebPushTest,
  type WebPushState,
} from "@multica/core/platform";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { toast } from "sonner";
import { isDesktopShell } from "../../platform";
import { useT } from "../../i18n";

/**
 * Web-only control for the effective background-delivery state. Desktop
 * delivers banners through Electron and does not use browser Push.
 *
 * Capability and permission are read from `window`, so the first paint defers
 * to a post-mount effect to keep SSR and client markup identical (no hydration
 * mismatch).
 */
export function BrowserNotificationSetting() {
  const { t } = useT("settings");
  const [mounted, setMounted] = useState(false);
  const [state, setState] = useState<WebPushState>(initialWebPushState);
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);

  useEffect(() => {
    setMounted(true);
    if (isDesktopShell()) return;
    let cancelled = false;
    void reconcileWebPushSubscription().then((nextState) => {
      if (!cancelled) setState(nextState);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  if (!mounted || isDesktopShell()) return null;

  const handleEnable = async () => {
    setBusy(true);
    try {
      setState(await enableWebPushSubscription());
    } finally {
      setBusy(false);
    }
  };

  const handleRetry = async () => {
    setBusy(true);
    try {
      setState(await reconcileWebPushSubscription());
    } finally {
      setBusy(false);
    }
  };

  const handleTest = async () => {
    setTesting(true);
    try {
      await sendWebPushTest();
      toast.success(t(($) => $.notifications.browser.test_success));
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.notifications.browser.test_failed),
      );
    } finally {
      setTesting(false);
    }
  };

  const statusHint = (() => {
    switch (state.status) {
      case "subscribed":
        return t(($) => $.notifications.browser.subscribed);
      case "permission-denied":
        return t(($) => $.notifications.browser.denied);
      case "unsupported":
        return t(($) => $.notifications.browser.unsupported);
      case "server-disabled":
        return t(($) => $.notifications.browser.server_disabled);
      case "fallback":
        return t(($) => $.notifications.browser.fallback);
      case "error":
        return t(($) => $.notifications.browser.error);
      case "permission-required":
      default:
        return t(($) => $.notifications.browser.hint);
    }
  })();

  const canEnable =
    state.permission === "default" &&
    (state.status === "permission-required" || state.status === "fallback");

  return (
    <Card>
      <CardContent>
        <div className="flex flex-col items-start justify-between gap-4 sm:flex-row sm:items-center">
          <div className="space-y-0.5 pr-4">
            <p className="text-sm font-medium">
              {t(($) => $.notifications.browser.label)}
            </p>
            <p className="text-xs text-muted-foreground">{statusHint}</p>
          </div>
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            {canEnable && (
              <Button
                size="sm"
                variant="outline"
                onClick={handleEnable}
                disabled={busy}
              >
                <Bell className="size-3.5" />
                {t(($) => $.notifications.browser.enable)}
              </Button>
            )}
            {state.status === "error" && (
              <Button
                size="sm"
                variant="outline"
                onClick={handleRetry}
                disabled={busy}
              >
                <RotateCw className="size-3.5" />
                {t(($) => $.notifications.browser.retry)}
              </Button>
            )}
            {state.subscribed && (
              <Button
                size="sm"
                variant="outline"
                onClick={handleTest}
                disabled={testing}
              >
                <Send className="size-3.5" />
                {testing
                  ? t(($) => $.notifications.browser.testing)
                  : t(($) => $.notifications.browser.test)}
              </Button>
            )}
            {state.permission === "granted" && state.status !== "error" && (
              <span className="shrink-0 text-xs font-medium text-muted-foreground">
                {t(($) => $.notifications.browser.enabled_badge)}
              </span>
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

/* global self, URL */

function sameOriginUrl(value) {
  if (typeof value !== "string" || !value) return null;
  try {
    const url = new URL(value, self.location.origin);
    return url.origin === self.location.origin ? url.href : null;
  } catch {
    return null;
  }
}

function parsePushPayload(event) {
  if (!event.data) return null;
  try {
    const payload = event.data.json();
    if (
      !payload ||
      typeof payload !== "object" ||
      typeof payload.title !== "string" ||
      !payload.title ||
      typeof payload.body !== "string"
    ) {
      return null;
    }
    const url = sameOriginUrl(payload.url);
    if (!url) return null;
    const tag =
      typeof payload.tag === "string"
        ? payload.tag
        : typeof payload.inbox_item_id === "string"
          ? payload.inbox_item_id
          : undefined;
    return {
      title: payload.title,
      body: payload.body,
      url,
      tag,
      test: payload.test === true,
    };
  } catch {
    return null;
  }
}

async function handlePush(event) {
  const payload = parsePushPayload(event);
  if (!payload) return;
  const windows = await self.clients.matchAll({
    type: "window",
    includeUncontrolled: true,
  });
  if (!payload.test && windows.some((client) => client.focused)) return;

  const options = {
    body: payload.body,
    data: { url: payload.url },
  };
  if (payload.tag) options.tag = payload.tag;
  await self.registration.showNotification(payload.title, options);
}

async function handleNotificationClick(event) {
  event.notification.close();
  const targetUrl = sameOriginUrl(event.notification.data?.url);
  if (!targetUrl) return;

  const windows = await self.clients.matchAll({
    type: "window",
    includeUncontrolled: true,
  });
  const client = windows.find((candidate) => {
    try {
      return new URL(candidate.url).origin === self.location.origin;
    } catch {
      return false;
    }
  });
  if (client) {
    if (typeof client.navigate === "function") await client.navigate(targetUrl);
    await client.focus();
    return;
  }
  await self.clients.openWindow(targetUrl);
}

self.addEventListener("push", (event) => {
  event.waitUntil(handlePush(event));
});

self.addEventListener("notificationclick", (event) => {
  event.waitUntil(handleNotificationClick(event));
});

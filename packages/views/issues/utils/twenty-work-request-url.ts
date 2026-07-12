const TWENTY_ORIGIN = "https://crm.aiparis.org";
const TWENTY_WORK_REQUEST_PATH = /^\/object\/workRequest\/[^/]+$/;

export const TWENTY_RECORD_URL_METADATA_KEY = "argos_twenty_record_url";

export function readTwentyWorkRequestURL(metadata: unknown): string | null {
  if (metadata === null || typeof metadata !== "object" || Array.isArray(metadata)) {
    return null;
  }

  const value = (metadata as Record<string, unknown>)[TWENTY_RECORD_URL_METADATA_KEY];
  if (typeof value !== "string") {
    return null;
  }

  try {
    const url = new URL(value);
    if (
      url.origin !== TWENTY_ORIGIN ||
      url.username !== "" ||
      url.password !== "" ||
      !TWENTY_WORK_REQUEST_PATH.test(url.pathname)
    ) {
      return null;
    }
    return url.toString();
  } catch {
    return null;
  }
}

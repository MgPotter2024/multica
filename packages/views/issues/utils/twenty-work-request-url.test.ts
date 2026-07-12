import { describe, expect, it } from "vitest";
import { readTwentyWorkRequestURL } from "./twenty-work-request-url";

describe("readTwentyWorkRequestURL", () => {
  it("returns a trusted Twenty Work Request URL", () => {
    expect(
      readTwentyWorkRequestURL({
        argos_twenty_record_url:
          "https://crm.aiparis.org/object/workRequest/1d760d12-a3e4-4f52-b34f-47d66be9bb76?view=all#activity",
      }),
    ).toBe(
      "https://crm.aiparis.org/object/workRequest/1d760d12-a3e4-4f52-b34f-47d66be9bb76?view=all#activity",
    );
  });

  it.each([
    ["missing metadata", undefined],
    ["missing key", {}],
    ["non-string value", { argos_twenty_record_url: 42 }],
    ["malformed URL", { argos_twenty_record_url: "not a url" }],
    ["non-HTTPS URL", { argos_twenty_record_url: "http://crm.aiparis.org/object/workRequest/123" }],
    ["userinfo URL", { argos_twenty_record_url: "https://user@crm.aiparis.org/object/workRequest/123" }],
    ["lookalike host", { argos_twenty_record_url: "https://crm.aiparis.org.attacker.test/object/workRequest/123" }],
    ["wrong route", { argos_twenty_record_url: "https://crm.aiparis.org/objects/workRequests/123" }],
    ["route prefix confusion", { argos_twenty_record_url: "https://crm.aiparis.org/object/workRequest-archive/123" }],
    ["missing record ID", { argos_twenty_record_url: "https://crm.aiparis.org/object/workRequest/" }],
    ["extra path segment", { argos_twenty_record_url: "https://crm.aiparis.org/object/workRequest/123/edit" }],
  ])("rejects %s", (_name, metadata) => {
    expect(readTwentyWorkRequestURL(metadata)).toBeNull();
  });
});

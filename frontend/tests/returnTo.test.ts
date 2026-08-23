import { describe, expect, it } from "vitest";
import { loginHref, safeReturnTo } from "@/lib/returnTo";

describe("safeReturnTo", () => {
  it.each([
    ["//evil.com", "protocol-relative URLs leave the origin"],
    ["/\\evil.com", "browsers normalise a backslash pair to //"],
    ["http://evil", "an absolute URL is off-origin by definition"],
    ["javascript:alert(1)", "a scheme that is not a path at all"],
    ["", "nothing to return to"],
    [null, "no parameter supplied"],
    ["/login", "returning to the login page would loop"],
    ["/login?x=1", "same, with a query string"],
  ])("rejects %j (%s)", (value: string | null, _reason: string) => {
    expect(safeReturnTo(value)).toBeNull();
  });

  it.each(["/secrets?env=prod#v2", "/", "/applications?app=payments-api"])(
    "echoes the same-origin path %j",
    (value) => {
      expect(safeReturnTo(value)).toBe(value);
    },
  );
});

describe("loginHref", () => {
  it("omits the query when the destination is the default landing page", () => {
    expect(loginHref("/")).toBe("/login");
  });

  it("encodes the path so its own query survives the round-trip", () => {
    expect(loginHref("/secrets?x=1")).toBe("/login?returnTo=%2Fsecrets%3Fx%3D1");
  });

  it("drops a destination that would leave the origin", () => {
    expect(loginHref("//evil")).toBe("/login");
  });
});

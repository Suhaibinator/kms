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
    ["/\t/evil.example", "URL parsing strips the tab, leaving //evil.example"],
    ["/\r/evil.example", "same with a carriage return"],
    ["/\n/evil.example", "same with a line feed"],
    ["/\r\n/evil.example", "same with a CRLF pair"],
    ["/\\/evil.example", "a backslash normalises to a slash: //evil.example"],
    ["/\t\\evil.example", "mixed separators: tab then backslash"],
    ["/\\\tevil.example", "mixed separators: backslash then tab"],
    ["/\u0000/evil.example", "any other control character is just as invisible to the parser"],
    ["/secrets\t?env=prod", "a control character anywhere in the value is refused, not stripped"],
  ])("rejects %j (%s)", (value: string | null, _reason: string) => {
    expect(safeReturnTo(value)).toBeNull();
  });

  it("returns the canonical path rather than the raw value", () => {
    expect(safeReturnTo("/a/../secrets?x=1#v2")).toBe("/secrets?x=1#v2");
    expect(safeReturnTo("/a/../../evil.example")).toBe("/evil.example"); // still this origin
    expect(safeReturnTo("/secrets?x=1&y=a b")).toBe("/secrets?x=1&y=a%20b");
  });

  it("keeps a still-encoded separator as the same-origin path it is", () => {
    // Only a decoded control character reaches the parser as a separator; a
    // literal "%09" stays a path segment on this origin.
    expect(safeReturnTo("/%09/evil.example")).toBe("/%09/evil.example");
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

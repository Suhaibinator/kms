import { readFile } from "node:fs/promises";
import { describe, expect, it } from "vitest";

import { codecs, decodeGroup, encodeGroup, field, group } from "../src/configstore/index.js";

enum Role {
  Low = -1,
  Unspecified = 0,
  Admin = 3,
  // biome-ignore lint/suspicious/noDuplicateEnumValues: aliases are the behavior under test.
  AdminAlias = 3,
}
enum Protocol {
  Empty = "",
  Space = " ",
  Http = "http",
  Proto = "__proto__",
  Constructor = "constructor",
}

interface Config {
  entries: Map<Role, string> | null;
}
const enumCodec = codecs.enumMap<Role, string>(
  [
    ["LOW", Role.Low],
    ["UNSPECIFIED", Role.Unspecified],
    ["ADMIN", Role.Admin],
    ["ADMIN_ALIAS", Role.AdminAlias],
  ],
  codecs.string,
);
const descriptor = group<Config>([field("entries", "entries", enumCodec)]);

const shared = JSON.parse(
  await readFile(new URL("../../testdata/enum-key-maps.json", import.meta.url), "utf8"),
) as {
  cases: {
    name: string;
    key_kind: "integer" | "string";
    members: { name: string; number?: number }[];
    document: string;
    valid: boolean;
  }[];
};

describe("enum-keyed map codecs", () => {
  for (const testCase of shared.cases) {
    it(`shared SDK case: ${testCase.name}`, () => {
      interface Shared {
        entries: Map<string | number, string> | null;
      }
      const codec = codecs.enumMap<string | number, string>(
        testCase.members.map((member) => [
          member.name,
          testCase.key_kind === "integer" ? (member.number as number) : member.name,
        ]),
        codecs.string,
      );
      const contract = group<Shared>([field("entries", "entries", codec)]);
      if (!testCase.valid) {
        expect(() => decodeGroup(testCase.document, contract)).toThrow();
        return;
      }
      const decoded = decodeGroup(testCase.document, contract);
      expect(decoded.entries === null || decoded.entries instanceof Map).toBe(true);
      expect(JSON.parse(encodeGroup(decoded, contract))).toEqual(JSON.parse(testCase.document));
    });
  }

  it("uses typed numeric members, first canonical alias, and sorted wire names", () => {
    const decoded = decodeGroup(
      '{"entries":{"UNSPECIFIED":"zero","ADMIN":"admin","LOW":"low"}}',
      descriptor,
    );
    expect(decoded.entries?.get(Role.Unspecified)).toBe("zero");
    expect(decoded.entries?.get(Role.AdminAlias)).toBe("admin");
    expect(encodeGroup(decoded, descriptor)).toBe(
      '{"entries":{"ADMIN":"admin","LOW":"low","UNSPECIFIED":"zero"}}',
    );
    expect(() => decodeGroup('{"entries":{"ADMIN_ALIAS":"admin"}}', descriptor)).toThrow(
      /unknown enum map key/u,
    );
    expect(() => encodeGroup({ entries: new Map([[99 as Role, "private"]]) }, descriptor)).toThrow(
      /not a declared member/u,
    );
  });

  it("handles exact string enum values including special object names", () => {
    const codec = codecs.enumMap<Protocol, string>(
      [
        ["", ""],
        [" ", " "],
        ["http", "http"],
        ["__proto__", "__proto__"],
        ["constructor", "constructor"],
      ],
      codecs.string,
    );
    const node = codec.encodeNode(
      new Map([
        [Protocol.Proto, "proto"],
        [Protocol.Empty, "empty"],
      ]),
      "$",
    );
    const decoded = codec.decodeNode(node, "$");
    expect(decoded?.get(Protocol.Proto)).toBe("proto");
    expect(decoded?.get(Protocol.Empty)).toBe("empty");
  });

  it("keeps wide integer keys exact and never accepts string numeric spellings", () => {
    const codec = codecs.enumMap<9007199254740993n | 9223372036854775807n, string>(
      [
        ["WIDE", 9007199254740993n],
        ["MAX", 9223372036854775807n],
      ],
      codecs.string,
    );
    const value = new Map<9007199254740993n | 9223372036854775807n, string>([
      [9007199254740993n, "exact"],
    ]);
    expect(codec.decodeNode(codec.encodeNode(value, "$"), "$")?.get(9007199254740993n)).toBe(
      "exact",
    );
    expect(() =>
      codec.encodeNode(new Map([["9007199254740993", "wrong"]]) as unknown as typeof value, "$"),
    ).toThrow();
  });

  it("reports only wildcard paths for untrusted names and nested values", () => {
    const unknown = "PRIVATE_UNKNOWN_KEY";
    expect(() => decodeGroup(`{"entries":{"${unknown}":"hidden"}}`, descriptor)).toThrow(
      /^configstore: unknown enum map key at \$\.entries$/u,
    );
    expect(() => decodeGroup('{"entries":{"ADMIN":42}}', descriptor)).toThrow(/\$\.entries\[\*\]/u);
    expect(() => decodeGroup('{"entries":{"ADMIN":"one","ADMIN":"two"}}', descriptor)).toThrow(
      /duplicate map key/u,
    );
    expect(() =>
      enumCodec.encodeNode({ ADMIN: "object" } as unknown as Map<Role, string>, "$"),
    ).toThrow(/does not match source/u);
  });

  it("validates and captures member tables without retaining caller-owned state", () => {
    const members: [string, number][] = [
      ["FIRST", 1],
      ["ALIAS", 1],
    ];
    const codec = codecs.enumMap<number, string>(members, codecs.string);
    members[0] = ["CHANGED", 2];
    expect(codec.encodeNode(new Map([[1, "one"]]), "$")).toEqual({
      kind: "object",
      properties: [{ name: "FIRST", value: { kind: "string", value: "one" } }],
    });
    expect(() => codecs.enumMap([], codecs.string)).toThrow(/at least one/u);
    expect(() =>
      codecs.enumMap<number, string>([["BAD", 9007199254740992]], codecs.string),
    ).toThrow(/exact signed int64/u);
    expect(() =>
      codecs.enumMap<bigint, string>([["BAD", 9223372036854775808n]], codecs.string),
    ).toThrow(/exact signed int64/u);
    expect(() =>
      codecs.enumMap<number, string>(
        [
          ["A", 1],
          ["A", 2],
        ],
        codecs.string,
      ),
    ).toThrow(/unique/u);
    expect(() =>
      codecs.enumMap<number, string>(
        [
          ["A", 1],
          ["ALIAS", 1],
          ["ALIAS", 2],
        ],
        codecs.string,
      ),
    ).toThrow(/unique/u);
    expect(() => codecs.enumMap([["wrong", "value"]], codecs.string)).toThrow(
      /canonical member name/u,
    );
    expect(() =>
      codecs.enumMap<string | number, string>(
        [
          ["number", 1],
          ["string", "string"],
        ],
        codecs.string,
      ),
    ).toThrow(/one primitive key type/u);
  });
});

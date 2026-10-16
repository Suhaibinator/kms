import { readFile } from "node:fs/promises";
import { describe, expect, it } from "vitest";

import { generate, normalizeDescriptor, parseDescriptor } from "../src/configgen/index.js";
import {
  type AppliedReport,
  type DefaultMismatchReport,
  decodeGroup,
  parseDefaultsArtifact,
} from "../src/configstore/index.js";
import type { ManagedReleaseClient } from "../src/configstore/manager.js";
import type { ReleaseLoader } from "../src/releases/loader.js";
import {
  type PrepareRelease,
  ReleaseEntryMetadata,
  ReleaseParameter,
  ReleaseSnapshot,
} from "../src/releases/types.js";
import { type Config, Protocol, Role } from "./fixtures/configgen-enum-map/config.js";
import {
  encodeDefaultsArtifact,
  encodeParameterGroups,
  groupCodecs,
  Store,
} from "./fixtures/configgen-enum-map/config.generated.js";

const fixture = new URL("./fixtures/configgen-enum-map/", import.meta.url);
const descriptor = parseDescriptor(await readFile(new URL("config.kms.json", fixture), "utf8"));

function config(): Config {
  return {
    roles: new Map([
      [Role.Admin, { enabled: false }],
      [Role.Unspecified, { enabled: true }],
    ]),
    protocols: new Map([
      [Protocol.Proto, "special"],
      [Protocol.Http, "web"],
    ]),
    wide: new Map([[9007199254740993n, "exact"]]),
    nested: { maps: [new Map([[Role.Low, "nested"]]), null] },
  };
}

function withKey(key: unknown): unknown {
  return {
    ...descriptor,
    groups: [
      {
        alias: "runtime",
        fields: [
          {
            property: "roles",
            jsonName: "roles",
            reload: "hot",
            views: ["worker"],
            type: { kind: "enumMap", key, value: { kind: "string" } },
          },
        ],
      },
    ],
  };
}

describe("generated enum-keyed maps", () => {
  it("keeps checked-in binding/schema/contract deterministic and precise", async () => {
    const artifacts = generate(descriptor, {
      runtimeImport: "../../../src/configstore/index.js",
      coreImport: "../../../src/index.js",
    });
    expect(artifacts.binding).toBe(await readFile(new URL("config.generated.ts", fixture), "utf8"));
    expect(artifacts.schema).toBe(await readFile(new URL("runtime.schema.json", fixture), "utf8"));
    expect(artifacts.contract).toBe(
      await readFile(new URL("runtime.contract.json", fixture), "utf8"),
    );
    expect(artifacts.schema).toContain('"WIDE": 9007199254740993');
    expect(artifacts.schema).toContain('"MAX": 9223372036854775807');
    expect(artifacts.schema).not.toContain("ADMIN_ALIAS");
    expect(artifacts.binding).toContain("9007199254740993n");
    expect(artifacts.binding).not.toContain("import { Role");
    expect(
      JSON.parse(artifacts.contract).fields.filter(
        (field: { encoding: string }) => field.encoding === "nullable-enum-key-map",
      ),
    ).toHaveLength(3);
    const schema = JSON.parse(artifacts.schema);
    expect(schema.properties.runtime.properties.roles.anyOf[0]).toMatchObject({
      type: "object",
      propertyNames: {
        type: "string",
        enum: ["LOW", "UNSPECIFIED", "ADMIN"],
        "x-kms-enum-numbers": { LOW: -1, UNSPECIFIED: 0, ADMIN: 3 },
      },
      additionalProperties: { type: "object", properties: { enabled: { type: "boolean" } } },
    });
  });

  it("selects first aliases before ordering numeric members and preserves exact string declaration order", () => {
    const numeric = normalizeDescriptor(
      withKey({
        kind: "integer",
        bits: 32,
        members: [
          { name: "FIRST", value: "3" },
          { name: "ALIAS", value: "3" },
          { name: "ZERO", value: "0" },
        ],
      }),
    );
    expect(numeric.groups[0]?.fields[0]?.type).toMatchObject({
      key: {
        members: [
          { name: "ZERO", value: "0" },
          { name: "FIRST", value: "3" },
        ],
      },
    });
    const string = normalizeDescriptor(
      withKey({
        kind: "string",
        members: [
          { name: " ", value: " " },
          { name: "", value: "" },
          { name: "__proto__", value: "__proto__" },
        ],
      }),
    );
    expect(string.groups[0]?.fields[0]?.type).toMatchObject({
      key: {
        members: [
          { name: " ", value: " " },
          { name: "", value: "" },
          { name: "__proto__", value: "__proto__" },
        ],
      },
    });
  });

  it.each([
    { kind: "integer", bits: 64, representation: "number", members: [{ name: "A", value: "1" }] },
    { kind: "integer", bits: 64, members: [{ name: "A", value: Number("9007199254740993") }] },
    { kind: "integer", bits: 64, members: [{ name: "A", value: "01" }] },
    { kind: "integer", bits: 64, members: [{ name: "A", value: "-0" }] },
    { kind: "integer", bits: 8, members: [{ name: "A", value: "128" }] },
    { kind: "integer", bits: 8, unsigned: true, members: [{ name: "A", value: "-1" }] },
    {
      kind: "integer",
      bits: 64,
      unsigned: true,
      members: [{ name: "A", value: "9223372036854775808" }],
    },
    { kind: "string", members: [{ name: "A", value: "different" }] },
    { kind: "string", members: [] },
    { kind: "float", members: [{ name: "A", value: "1" }] },
    {
      kind: "integer",
      members: [
        { name: "A", value: "1" },
        { name: "A", value: "2" },
      ],
    },
  ])("rejects invalid enum key descriptors %#", (key) => {
    expect(() => normalizeDescriptor(withKey(key))).toThrow();
  });

  it("encodes defaults and roundtrips numeric/string/bigint/nested Maps", () => {
    const defaults = config();
    const document = encodeParameterGroups(defaults).runtime;
    expect(document).toBe(
      '{"nested":{"maps":[{"LOW":"nested"},null]},"protocols":{"__proto__":"special","http":"web"},"roles":{"ADMIN":{"enabled":false},"UNSPECIFIED":{"enabled":true}},"wide":{"WIDE":"exact"}}',
    );
    expect(decodeGroup(document ?? "", groupCodecs.runtime)).toEqual(defaults);
    const artifact = parseDefaultsArtifact(encodeDefaultsArtifact("local", defaults));
    expect(artifact.parameters[0]?.value).toBe(document);
    expect(encodeParameterGroups({ ...defaults, roles: null }).runtime).toContain('"roles":null');
    expect(encodeParameterGroups({ ...defaults, roles: new Map() }).runtime).toContain(
      '"roles":{}',
    );
  });

  it("defensively clones Map values in local snapshots and typed views", async () => {
    const defaults = config();
    const { store } = await Store.createLocal(defaults, () => undefined);
    const originalAdmin = defaults.roles?.get(Role.Admin);
    if (originalAdmin) originalAdmin.enabled = true;
    const escaped = store.current().worker().roles;
    const escapedAdmin = escaped?.get(Role.Admin);
    if (escapedAdmin) escapedAdmin.enabled = true;
    escaped?.clear();
    expect(store.current().worker().roles?.get(Role.Admin)).toEqual({ enabled: false });
    expect(store.current().config().wide?.get(9007199254740993n)).toBe("exact");
  });

  it("compares order independently, projects report objects, and rejects restart changes", async () => {
    const defaults = config();
    const first = config();
    first.roles?.set(Role.Admin, { enabled: true });
    first.nested.maps?.[0]?.set(Role.Admin, "added");
    const loader = new InlineLoader(snapshot(first, 1n));
    const mismatches: DefaultMismatchReport[] = [];
    const applied: AppliedReport[] = [];
    const store = new Store(defaults, () => undefined);
    const manager = await store.start(loader.client(), {
      release: "runtime",
      onDefaultMismatch: (report) => {
        mismatches.push(report);
      },
      onApplied: (report) => {
        applied.push(report);
      },
    });
    expect(mismatches[0]?.fields()).toEqual([
      {
        path: "runtime.nested",
        expected: { maps: [{ LOW: "nested" }, null] },
        actual: { maps: [{ ADMIN: "added", LOW: "nested" }, null] },
      },
      {
        path: "runtime.roles",
        expected: { ADMIN: { enabled: false }, UNSPECIFIED: { enabled: true } },
        actual: { ADMIN: { enabled: true }, UNSPECIFIED: { enabled: true } },
      },
    ]);
    expect(JSON.stringify(mismatches[0])).toContain('"ADMIN":{"enabled":true}');
    const reordered = config();
    reordered.roles = new Map([...(first.roles ?? [])].reverse());
    reordered.nested = first.nested;
    reordered.protocols = new Map([...(first.protocols ?? [])].reverse());
    await loader.apply(snapshot(reordered, 2n));
    expect(applied[1]?.changed()).toEqual([]);
    const changed = config();
    changed.roles?.delete(Role.Admin);
    await loader.apply(snapshot(changed, 3n));
    expect(applied[2]?.changed()).toContainEqual({
      path: "runtime.roles",
      previous: { ADMIN: { enabled: true }, UNSPECIFIED: { enabled: true } },
      current: { UNSPECIFIED: { enabled: true } },
    });
    expect(JSON.stringify(applied[2])).toContain('"previous":{"ADMIN"');
    changed.protocols = new Map([[Protocol.Http, "restart"]]);
    await expect(loader.apply(snapshot(changed, 4n))).rejects.toMatchObject({
      category: "restart_required",
    });
    expect(store.current().release.version).toBe(3n);
    manager.stop();
    await manager.wait();
  });
});

function snapshot(value: Config, version: bigint): ReleaseSnapshot {
  const entry = new ReleaseEntryMetadata({
    alias: "runtime",
    kind: "parameter",
    version,
    contentType: "json",
    path: "groups/runtime",
  });
  return new ReleaseSnapshot({
    namespace: "prod/app",
    name: "runtime",
    version,
    activationRevision: version,
    digest: `digest-${version}`,
    entries: new Map([["runtime", entry]]),
    parameters: new Map([
      ["runtime", new ReleaseParameter(encodeParameterGroups(value).runtime ?? "", entry)],
    ]),
    secrets: new Map(),
  });
}

class InlineLoader {
  #prepare: PrepareRelease | undefined;
  readonly #first: ReleaseSnapshot;
  readonly #done: Promise<void>;
  #finish: () => void = () => undefined;
  constructor(first: ReleaseSnapshot) {
    this.#first = first;
    this.#done = new Promise((resolve) => {
      this.#finish = resolve;
    });
  }
  client(): ManagedReleaseClient {
    return { createReleaseLoader: async () => this as unknown as ReleaseLoader };
  }
  async run(prepare: PrepareRelease): Promise<void> {
    this.#prepare = prepare;
    await this.apply(this.#first);
    await this.#done;
  }
  async apply(candidate: ReleaseSnapshot): Promise<void> {
    if (!this.#prepare) throw new Error("loader not started");
    const prepared = await this.#prepare(candidate, new AbortController().signal);
    prepared.commit();
  }
  stop(): void {
    this.#finish();
  }
}

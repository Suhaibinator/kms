export enum Role {
  Low = -1,
  Unspecified = 0,
  Admin = 3,
  // biome-ignore lint/suspicious/noDuplicateEnumValues: generated codecs select the first alias.
  AdminAlias = 3,
}
export enum Protocol {
  Http = "http",
  Proto = "__proto__",
  Empty = "",
  Space = " ",
}

export interface Config {
  roles: Map<Role, { enabled: boolean }> | null;
  protocols: ReadonlyMap<Protocol, string> | null;
  wide: Map<9007199254740993n | 9223372036854775807n, string> | null;
  nested: { maps: (Map<Role, string> | null)[] | null };
}

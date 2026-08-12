export interface Parameter {
  readonly env: string;
  readonly app: string;
  readonly key: string;
  readonly value: string;
  readonly contentType: string;
  readonly version: bigint;
  readonly metadataJson: string;
  readonly createdBy: string;
  readonly createdAtUnixMs: bigint;
  readonly labels: Readonly<Record<string, bigint>>;
  readonly namespace: string;
  readonly path: string;
}

export interface SecretVersion {
  readonly version: bigint;
  readonly state: string;
  readonly createdBy: string;
  readonly createdAtUnixMs: bigint;
  readonly destroyedAtUnixMs: bigint;
  readonly expiresAtUnixMs: bigint;
  readonly metadataJson: string;
}

export interface SecretInfo {
  readonly env: string;
  readonly app: string;
  readonly key: string;
  readonly contentType: string;
  readonly clientBound: boolean;
  readonly hasAccessToken: boolean;
  readonly metadataJson: string;
  readonly createdAtUnixMs: bigint;
  readonly updatedAtUnixMs: bigint;
  readonly labels: Readonly<Record<string, bigint>>;
  readonly versions: readonly SecretVersion[];
  readonly namespace: string;
  readonly path: string;
}

export interface PutResult {
  readonly version: bigint;
  readonly revision: bigint;
}

export interface PutSecretResult extends PutResult {
  readonly accessToken: string;
}

export interface Page<T> {
  readonly items: readonly T[];
  readonly nextPageToken: string;
}

export interface WhoAmI {
  readonly identity: string;
  readonly kind: string;
  readonly namespace?: string;
  readonly authMethod: string;
}

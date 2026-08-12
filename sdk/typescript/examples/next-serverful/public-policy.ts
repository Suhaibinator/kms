/** The complete browser-visible policy contract. Keep this intentionally small. */
export interface PublicPasswordPolicy {
  readonly [key: string]: number;
  readonly minLength: number;
}

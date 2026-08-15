import { codecs, type GroupCodec, type ValueCodec } from "@suhaibinator/kms/configstore";

const nullableReadonlyArray: ValueCodec<readonly string[] | null> = codecs.array<
  string,
  readonly string[]
>(codecs.string);
const nullableMutableArray: ValueCodec<string[] | null> = codecs.array<string, string[]>(
  codecs.string,
);
const readonlyPair: ValueCodec<readonly [string, string]> = codecs.fixedArray<
  string,
  2,
  readonly [string, string]
>(codecs.string, 2);
const mutablePair: ValueCodec<[string, string]> = codecs.fixedArray<string, 2, [string, string]>(
  codecs.string,
  2,
);
const mutableRecord: ValueCodec<Record<string, string> | null> = codecs.record<
  string,
  Record<string, string>
>(codecs.string);
const nullableRecord: ValueCodec<Readonly<Record<string, string>> | null> = codecs.record<
  string,
  Readonly<Record<string, string>>
>(codecs.string);

// A variable-array descriptor must preserve JSON null and must not masquerade
// as a fixed tuple merely because both values are JavaScript arrays.
// @ts-expect-error Variable arrays decode JSON null.
const nonnullableArray: ValueCodec<readonly string[]> = codecs.array<string, readonly string[]>(
  codecs.string,
);
// @ts-expect-error Variable arrays cannot satisfy fixed-length tuple fields.
codecs.array<string, readonly [string, string]>(codecs.string);

declare const brand: unique symbol;
type BrandedArray = readonly string[] & { readonly [brand]: true };
type BrandedPair = readonly [string, string] & { readonly [brand]: true };
type RestTuple = readonly [string, ...string[]];

// @ts-expect-error Decoders create plain arrays, not branded containers.
codecs.array<string, BrandedArray>(codecs.string);
// @ts-expect-error Rest tuples are not plain variable arrays.
codecs.array<string, RestTuple>(codecs.string);

// @ts-expect-error The declared source tuple length must equal the descriptor length.
const wrongFixedLength = codecs.fixedArray<string, 2, readonly [string, string, string]>(
  codecs.string,
  2,
);
// @ts-expect-error Decoders create plain tuples, not branded tuples.
codecs.fixedArray<string, 2, BrandedPair>(codecs.string, 2);
// @ts-expect-error Fixed-array codecs cannot promise a rest tuple.
codecs.fixedArray<string, 2, RestTuple>(codecs.string, 2);
// @ts-expect-error Every fixed-array position is decoded as the element type.
codecs.fixedArray<string, 2, readonly ["first", "second"]>(codecs.string, 2);

// @ts-expect-error Record descriptors decode JSON null.
const nonnullableRecord: ValueCodec<Readonly<Record<string, string>>> = codecs.record<
  string,
  Readonly<Record<string, string>>
>(codecs.string);
type RequiredMap = Readonly<Record<string, string>> & { readonly required: string };
// @ts-expect-error A record codec cannot promise a required key absent from arbitrary JSON objects.
codecs.record<string, RequiredMap>(codecs.string);

interface CompleteObject {
  readonly first: string;
  readonly second: number;
}

declare const completeCodec: GroupCodec<CompleteObject>;

function exactObject<T extends object, K extends keyof T & string>(
  codec: IsUnion<T> extends true
    ? never
    : Exclude<keyof T, K> extends never
      ? GroupCodec<T>
      : never,
): GroupCodec<T> {
  return codec;
}

exactObject<CompleteObject, "first" | "second">(completeCodec);
// @ts-expect-error Generated nested-object descriptors must cover every source property.
exactObject<CompleteObject, "first">(completeCodec);

function assertRootCoverage<T extends object, K extends PropertyKey>(
  ...missing: IsUnion<T> extends true
    ? [never]
    : [Exclude<keyof T, K>, Exclude<K, keyof T>] extends [never, never]
      ? []
      : [never]
): void {
  void missing;
}

assertRootCoverage<CompleteObject, "first" | "second">();
// @ts-expect-error Generated root descriptors must cover every source property.
assertRootCoverage<CompleteObject, "first">();
// @ts-expect-error Generated root descriptors cannot name nonexistent source properties.
assertRootCoverage<CompleteObject, "first" | "second" | "third">();

type Variant =
  | { readonly shared: string; readonly left: number }
  | { readonly shared: string; readonly right: boolean };
declare const variantCodec: GroupCodec<Variant>;
// @ts-expect-error Union source objects cannot prove exhaustive descriptor coverage.
exactObject<Variant, "shared">(variantCodec);
// @ts-expect-error Union root configs cannot prove exhaustive descriptor coverage.
assertRootCoverage<Variant, "shared">();

type IsUnion<T, Whole = T> = T extends unknown ? ([Whole] extends [T] ? false : true) : never;

void nullableReadonlyArray;
void nullableMutableArray;
void readonlyPair;
void mutablePair;
void mutableRecord;
void nullableRecord;
void nonnullableArray;
void wrongFixedLength;
void nonnullableRecord;

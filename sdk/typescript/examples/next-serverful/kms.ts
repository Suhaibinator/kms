import {
  ClassifiedReleaseError,
  createClient,
  definePublicProjection,
  mtlsFromFiles,
  type PolicySnapshot,
  type ReleaseSnapshot,
} from "@suhaibinator/kms";
import { createNextKms } from "@suhaibinator/kms/next/server";
import type { PublicPasswordPolicy } from "./public-policy.js";

interface PasswordPolicy {
  readonly minLength: number;
}

interface PasswordInput {
  readonly password: string;
}

interface PasswordErrors {
  readonly [key: string]: string;
  readonly password: string;
}

let active: PolicySnapshot<PasswordPolicy> | undefined;

const projection = definePublicProjection<
  PasswordPolicy,
  { readonly minLength: (policy: Readonly<PasswordPolicy>) => number }
>({
  minLength: (policy) => policy.minLength,
});

function requiredEnvironment(name: string): string {
  const value = process.env[name];
  if (value === undefined || value.length === 0) throw new Error(`${name} is required`);
  return value;
}

export const kms = createNextKms<
  PasswordPolicy,
  PublicPasswordPolicy,
  PasswordInput,
  PasswordErrors
>({
  projection,
  validate(policy, input) {
    return input.password.length >= policy.minLength
      ? { valid: true }
      : {
          valid: false,
          errors: { password: `Use at least ${policy.minLength} characters` },
        };
  },
  async initialize() {
    const client = createClient({
      endpoint: requiredEnvironment("KMS_ENDPOINT"),
      credentials: mtlsFromFiles(
        requiredEnvironment("KMS_CLIENT_CERT"),
        requiredEnvironment("KMS_CLIENT_KEY"),
        requiredEnvironment("KMS_SERVER_CA"),
      ),
    });

    try {
      const loader = await client.createReleaseLoader({
        name: "runtime",
        secretTokenProvider(alias) {
          return alias === "password_pepper" ? process.env.KMS_PASSWORD_PEPPER_TOKEN : undefined;
        },
      });
      let closing = false;
      const running = loader
        .run((snapshot: ReleaseSnapshot) => {
          const rawMinLength = snapshot.parameter("password_min_length")?.value();
          const minLength = Number(rawMinLength);
          if (!Number.isInteger(minLength) || minLength < 8 || minLength > 1024) {
            throw new ClassifiedReleaseError("config_validation_failed");
          }

          // Resolving the private entry proves that the complete release is usable,
          // but its plaintext is deliberately absent from PasswordPolicy.
          if (
            snapshot.entry("password_pepper") !== undefined &&
            !snapshot.secret("password_pepper")
          ) {
            throw new ClassifiedReleaseError("config_validation_failed");
          }

          const candidate: PolicySnapshot<PasswordPolicy> = Object.freeze({
            revision: snapshot.activationRevision,
            value: Object.freeze({ minLength }),
          });
          return {
            commit() {
              active = candidate;
            },
            abort() {},
          };
        })
        .catch(() => {
          if (!closing) {
            // Keep logs bounded: candidate and transport errors may contain application data.
            console.error("KMS release loader stopped unexpectedly");
          }
        });

      return {
        source: {
          current: () => active,
        },
        async close() {
          closing = true;
          loader.stop();
          await running;
          await client.close();
        },
      };
    } catch (error) {
      await client.close();
      throw error;
    }
  },
});

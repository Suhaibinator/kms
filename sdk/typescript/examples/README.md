# TypeScript SDK examples

These examples are compile-checked by `npm run test:types` and the repository
TypeScript CI job. They expect an existing KMS namespace, identity, and active
release; they do not provision server resources.

- [`basic.ts`](basic.ts) demonstrates an mTLS client, explicit secret access,
  and declarative hot-reloaded parameters.
- [`release.ts`](release.ts) demonstrates atomic preparation and commit of an
  active release.
- [`next-serverful`](next-serverful) is a minimal Next.js App Router
  integration with a Node-only release owner, public-policy Route Handler,
  Server Component, Server Action, and browser stale-policy recovery.

Run the core examples with a TypeScript runner after setting the environment
variables described in each file. The Next.js files are intended to be copied
into a serverful Next.js application; they are not a standalone app and must
not be copied into this repository's static-export admin frontend.

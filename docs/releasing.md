# Releasing

Stable repository releases are lockstep: one `X.Y.Z` version identifies the
server binaries, Go module, container image, Python SDK, and TypeScript SDK.
Prerelease tags are intentionally unsupported.

## Release from GitHub

1. Merge the release changes to `main` and wait for its ordinary CI run to pass.
2. On the repository's **Releases** page, choose **Draft a new release**.
3. Create a tag named `vX.Y.Z`, target `main`, add the release notes, and
   publish the release.

The tag is the source of truth for every published version. CI stamps `X.Y.Z`
into the Python and TypeScript package metadata in its temporary build
workspace; package manifests do not need a version-change commit for each
release.

The tag must match `vX.Y.Z` and point to a commit reachable from `main`.
Lightweight and annotated tags are both supported; prerelease tags are not.

## What CI publishes

The tag workflow reruns the complete repository CI matrix. After it passes, CI
publishes:

- archives containing `parameter-store` and `kms-config-gen` for Linux amd64,
  Linux arm64, macOS amd64, macOS arm64, and Windows amd64;
- a Python wheel and source distribution as GitHub Release assets;
- `@suhaibinator/kms@X.Y.Z` in GitHub's npm registry;
- `ghcr.io/suhaibinator/kms` for Linux amd64 and arm64, tagged `X.Y.Z`,
  `sha-<12-character commit>`, `X.Y`, `X`, and `latest`;
- `SHA256SUMS` plus GitHub provenance attestations for downloadable files and
  the container digest.

Immutable package and image versions publish first, then CI promotes the
mutable npm and container aliases. A release created in GitHub's UI is already
public while CI populates it. Failed or interrupted runs can be retried from
GitHub Actions; CI records a completion marker only after every publisher and
verification step succeeds.

## Install from GitHub

Download an archive and verify its checksum and provenance:

```bash
curl -LO https://github.com/Suhaibinator/kms/releases/download/v0.1.0/kms_0.1.0_linux_amd64.tar.gz
curl -LO https://github.com/Suhaibinator/kms/releases/download/v0.1.0/SHA256SUMS
grep 'kms_0.1.0_linux_amd64.tar.gz' SHA256SUMS | sha256sum --check
gh attestation verify kms_0.1.0_linux_amd64.tar.gz --repo Suhaibinator/kms
```

GitHub does not provide a PyPI-compatible package registry, so install the
Python wheel directly from the release:

```bash
python -m pip install \
  https://github.com/Suhaibinator/kms/releases/download/v0.1.0/kms_paramstore-0.1.0-py3-none-any.whl
```

GitHub's npm registry requires authentication even for this public package. Use
a classic personal access token with `read:packages` for local installation:

```bash
npm config set @suhaibinator:registry https://npm.pkg.github.com
npm config set //npm.pkg.github.com/:_authToken "$GITHUB_PACKAGES_TOKEN"
npm install @suhaibinator/kms@0.1.0
```

Pull and verify the multi-platform container image:

```bash
docker pull ghcr.io/suhaibinator/kms:0.1.0
gh attestation verify oci://ghcr.io/suhaibinator/kms:0.1.0 \
  --repo Suhaibinator/kms
```

Initialize separate database and key volumes, then start the service:

```bash
docker volume create kms-data
docker volume create kms-key
docker run --rm -it \
  -v kms-data:/data -v kms-key:/key \
  ghcr.io/suhaibinator/kms:0.1.0 \
  init --db /data/kms.db --master-key-file /key/master.key --admin ops

docker run --rm \
  -p 8080:8080 -p 8443:8443 \
  -v kms-data:/data -v kms-key:/key \
  ghcr.io/suhaibinator/kms:0.1.0
```

Back up the database and master-key volumes separately. For networked
deployments, configure TLS as described in [operations.md](operations.md).

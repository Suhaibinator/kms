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

If the release workflow itself is fixed after a failed release, open
**Actions**, choose **Release**, select **Run workflow**, and enter the existing
tag. The manual run uses the current workflow while rebuilding and verifying
artifacts from the tagged commit.

## Install from GitHub

Download an archive and verify its checksum and provenance:

```bash
VERSION=0.1.15
curl -LO "https://github.com/Suhaibinator/kms/releases/download/v${VERSION}/kms_${VERSION}_linux_amd64.tar.gz"
curl -LO "https://github.com/Suhaibinator/kms/releases/download/v${VERSION}/SHA256SUMS"
grep "kms_${VERSION}_linux_amd64.tar.gz" SHA256SUMS | sha256sum --check
gh attestation verify "kms_${VERSION}_linux_amd64.tar.gz" --repo Suhaibinator/kms
```

GitHub does not provide a PyPI-compatible package registry, so install the
Python wheel directly from the release:

```bash
VERSION=0.1.15
python -m pip install \
  "https://github.com/Suhaibinator/kms/releases/download/v${VERSION}/kms_paramstore-${VERSION}-py3-none-any.whl"
```

GitHub's npm registry requires authentication even for this public package. Use
a classic personal access token with `read:packages` for local installation:

```bash
VERSION=0.1.15
npm config set @suhaibinator:registry https://npm.pkg.github.com
npm config set //npm.pkg.github.com/:_authToken "$GITHUB_PACKAGES_TOKEN"
npm install "@suhaibinator/kms@${VERSION}"
```

Pull and verify the multi-platform container image:

```bash
VERSION=0.1.15
docker pull "ghcr.io/suhaibinator/kms:${VERSION}"
gh attestation verify "oci://ghcr.io/suhaibinator/kms:${VERSION}" \
  --repo Suhaibinator/kms
```

Initialize separate database and key volumes, then start the service:

```bash
VERSION=0.1.15
docker volume create kms-data
docker volume create kms-key
docker run --rm -it \
  -v kms-data:/data -v kms-key:/key \
  "ghcr.io/suhaibinator/kms:${VERSION}" \
  init --db /data/kms.db --master-key-file /key/master.key --admin ops

docker run --rm \
  -p 8080:8080 -p 8443:8443 \
  -v kms-data:/data -v kms-key:/key \
  "ghcr.io/suhaibinator/kms:${VERSION}"
```

Back up the database and master-key volumes separately. For networked
deployments, configure TLS as described in [operations.md](operations.md).

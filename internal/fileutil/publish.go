// Package fileutil contains small filesystem primitives whose security
// semantics are stronger than the portable os helpers expose directly.
package fileutil

// PublishNoReplace atomically publishes a fully-written staging file at dst
// while refusing to replace any existing directory entry. Where the platform
// has an exclusive rename the staging entry is moved; elsewhere dst is created
// as a hard link and the staging entry remains, so callers remove it once the
// call returns (on every path — it is harmless after a rename). Staging and
// destination must be on the same filesystem.
func PublishNoReplace(staging, dst string) error {
	return publishNoReplace(staging, dst)
}

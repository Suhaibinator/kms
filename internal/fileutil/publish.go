// Package fileutil contains small filesystem primitives whose security
// semantics are stronger than the portable os helpers expose directly.
package fileutil

// PublishNoReplace atomically moves a fully-written staging file to dst while
// refusing to replace any existing directory entry. Staging and destination
// must be on the same filesystem.
func PublishNoReplace(staging, dst string) error {
	return publishNoReplace(staging, dst)
}

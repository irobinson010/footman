//go:build !unix

package api

// checkDir has nothing to check here yet; Windows will use a named pipe with
// its own access control.
func checkDir(dir string) error { return nil }

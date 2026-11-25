//go:build unix

package api

import (
	"fmt"
	"os"
	"syscall"
)

// checkDir makes sure dir is a real directory that only the current user can
// enter, so nobody else can reach, replace or pre-create the socket in it
// (which matters for the fallback under the shared temp directory).
func checkDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible to other users (mode %v); it must be 0700", dir, st.Mode().Perm())
	}
	return nil
}

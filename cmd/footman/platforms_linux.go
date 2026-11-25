package main

// The real Linux platform registers itself as "linux"; the daemon and setup
// pick it by runtime.GOOS.
import _ "github.com/irobinson010/footman/internal/platform/linux"

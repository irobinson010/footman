// Package platform defines the operating system layer: the Platform interface
// and its sub-interfaces for input, windows, launching, notifications, the
// clipboard, audio devices, the background service, secrets and paths. Each
// OS has its own subpackage (linux, windows, darwin, chosen by build tags)
// that registers itself by name; fake holds a recording platform for tests.
package platform

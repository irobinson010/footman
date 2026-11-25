// Package install holds the setup wizard, the install receipt and the
// uninstall logic, so that removing footman leaves nothing behind (design §11).
//
// Everything Footman puts on the machine is recorded in the install receipt
// (<data>/install-receipt.json): files, folders, the service unit, keychain
// entries and models. Uninstall works only from the receipt, so it removes
// exactly what was installed.
//
// The service unit and the keychain are reached through the platform
// interfaces (platform.Service, platform.Secrets, platform.Paths), so the
// package runs against the fake platform in tests. Other components that put
// things on disk (the daemon, plugin and persona installs, the model
// downloader) add them with Record.
package install

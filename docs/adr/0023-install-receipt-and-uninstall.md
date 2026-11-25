# 0023. Install receipt and uninstall semantics

- Status: Accepted
- Date: 2026-11-25

## Context
Design §11 says uninstall works from an install receipt, lists what it will remove by group, and offers keep choices, but leaves open how entries are stored, what each keep choice means for each group, how secrets are confirmed, and what happens when a script runs it with no terminal. S1-02 has to pin these down so that "leaves nothing behind" and "keeps exactly what it says" are testable.

## Decision
- **Receipt:** `<data>/install-receipt.json`, version 1, a list of entries. Each entry has a `kind` (`file`, `dir`, `tree`, `model`, `service`, `secret`), a `group` (`program`, `models`, `config`, `data`, `personas`, `secrets`, `cache`, matching the listing in §11) and a `path` or `name`. Secret values are never recorded. Any component that installs something adds an entry with `install.Record`.
- **Removal rules:** `file` is one file (a `.db` file takes its `-wal`, `-shm` and `-journal` files with it); `tree` and `model` are removed with their contents; `dir` is removed only when empty, so files Footman did not install are left alone, reported, and never deleted. The loader refuses entries that are relative, not clean, a filesystem root, or the home folder or above it, because the receipt is a user-editable file.
- **Keep choices** map to groups: the program, service, models and cache are always removed. `--keep-config` keeps **Config**. `--keep-data` keeps **Data** and **Personas** (models live under the data folder but are not kept). "Remove everything" keeps neither. `--purge` is "remove everything" including secrets, with no questions, and conflicts with the keep flags.
- **Receipt after a partial uninstall:** if data is kept, the receipt stays and is rewritten to list only what remains, so a later uninstall finishes the job. If data is not kept, the receipt is removed with the data folder; anything kept elsewhere (config, secrets) is reported with its path in the summary.
- **Secrets:** kept unless `--purge` or `--remove-secrets`, or the user answers yes to a separate question. They are never part of a keep menu answer.
- **No terminal:** `footman uninstall` with no terminal and none of `--purge`, `--keep-config`, `--keep-data` exits with an error rather than guessing. `--dry-run` works with the flags and changes nothing, including the service.
- **Setup and the hotkey:** `footman setup` suggests F8 (ADR-0022) and takes the key as typed text (`--hotkey`, or a prompt). Asking the user to *press* the key needs the Linux input portal, which is a separate task; the wizard will switch to that when it lands. On an existing config, setup changes only the keys that were given, and writes the F8 default only into a config it creates.
- **API keys** reach setup through `--api-key-stdin` or a hidden prompt, never a command-line flag, so they stay out of `ps` and shell history. The config holds only `keychain:<provider>`.

## Consequences
Uninstall is exactly as complete as the receipt, so every installer must record what it adds. Files a user keeps inside Footman's folders survive uninstall, which can leave a folder behind (it is reported). Windows cannot delete a running program, so removing the binary there will need a follow-up.

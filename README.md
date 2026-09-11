# CPA Updater

A small Linux updater for existing CPA and Usage Keeper systemd services.
One executable, a numbered terminal menu, and downloads through GNU `wget`.
The server needs neither Go nor jq. The updater has no database, configuration
file, daemon, or systemd unit of its own.

| Target | Release repository | Installation directory | Versions |
| --- | --- | --- | --- |
| Personal CPA | 7ongOrz/CLIProxyAPI | /etc/cli-proxy-api | Latest stable |
| Official CPA | router-for-me/CLIProxyAPI | /etc/cli-proxy-api | Latest or specified |
| Usage Keeper | Willxup/cpa-usage-keeper | /etc/cpa-usage-keeper | Latest or specified |
| Updater itself | This repository | Executable's actual directory | Latest stable |

Official and personal CPA replace the **same installation**, rather than creating
two services. Existing services must use the executable paths shown above.

## Use

Download the binary matching the server CPU from Releases:

- `cpa-updater_linux_amd64`: x86-64 / amd64.
- `cpa-updater_linux_arm64`: ARM64 / aarch64.

Rename it to `cpa-updater` and keep it in a directory of your choice:

```sh
chmod +x cpa-updater
./cpa-updater
```

Service updates require root. Self-update requires write access to the executable's
directory. Installing into `/usr/local/bin` is optional; there are no aliases or
wrapper scripts. The current updater architecture determines download assets;
native execution matches the host architecture. Emulated execution retains the
updater architecture instead of switching architectures implicitly.

The interactive menu offers 10 releases per GitHub page, numeric selection,
`n`/`p` navigation, a full version tag, Enter for latest stable, and `0` to cancel.
Drafts and prereleases are filtered from the list, so some pages show fewer than
10 entries. Exact version input can select a published prerelease. Searching
means direct tag lookup, rather than downloading and indexing the entire history.

The menu shows the installed version and labels upgrades, downgrades, same-version
reinstalls, and official/personal source switches. CPA's version comes from its
help banner with an empty config; Usage Keeper uses `--version`. Older builds
without a version query show an unknown version. Ordering is defined for stable
`vX.Y.Z` and personal `vX.Y.Z-7ong.N` tags; other formats show unknown ordering.
The confirmation page shows the exact target release, architecture, and path.

Optional direct commands install immediately, without an interactive confirmation:

```sh
./cpa-updater cli-proxy-api
./cpa-updater official-cpa v7.2.158
./cpa-updater cpa-usage-keeper v1.15.4
./cpa-updater self
./cpa-updater --version
```

Omitting the version selects latest stable. Numeric versions accept an optional
`v` prefix. Personal CPA and updater self-update always select latest stable.
Selecting the installed version performs a reinstall; the explicit choice is honored.

The server needs Linux amd64/arm64, GNU wget, systemctl, and an existing service
installation. This tool updates executables; it does not install service units.
Run updates while conversations are idle: restarting a service interrupts its
active requests. Public GitHub API requests are unauthenticated and subject to
GitHub rate limits. A rate limit or network error stops the operation; retry later.

## Update and cleanup behavior

1. Resolve release metadata in memory and show the chosen version. After
   confirmation, acquire an exclusive advisory lock on the installation directory.
   No lock file or background service is created.
2. Remove the previous `update-tmp` directory and create it with mode 0700.
   This name is reserved for the updater; keep your own files elsewhere.
3. Download the exact architecture asset and checksums.txt.
   Check SHA256 before extracting only the expected executable. Releases whose
   assets/checksums are still being uploaded are rejected; retry later.
4. Sync the staged executable, preserve one `<binary>.previous` hard-link
   backup for a service update, and atomically replace the executable on the same filesystem.
5. Restart the existing systemd service and check `is-active`. On failure,
   restore the previous executable and attempt to restart it.
6. Remove `update-tmp` on success, failure, or handled cancellation, after any
   wget subprocess has exited. wget HSTS persistence is disabled.

Before installation, Ctrl+C, SIGTERM, or SSH SIGHUP cancels the download and
cleans up. Once installation starts, the short replace/restart phase completes
despite these signals. SIGKILL, power loss, or filesystem errors can leave
`update-tmp`; the next confirmed update of that target cleans it after acquiring
the lock. Exiting at the menu leaves existing crash leftovers in place. You can
also remove it manually while the updater is stopped:

```text
/etc/cli-proxy-api/update-tmp/
/etc/cpa-usage-keeper/update-tmp/
<updater-directory>/<updater-filename>.update-tmp/
```

The `.previous` executable is an intentional, single-version backup, not a
download cache. Configuration, credentials, and usage databases are untouched
by the updater. A newly started service may migrate its own database: binary
rollback does not reverse database migrations. Keep independent data backups
and review service release notes. `is-active` checks systemd state, not full
application health. An interrupted installation can leave the new executable
installed while the previous process is still running; inspect the service
before manually restarting it.

Self-update downloads a checksummed raw executable and atomically replaces the
running updater, even when launched through a symlink or from a different working
directory. The new version runs on the next invocation. It leaves one executable
and calls no systemd service. Self-update creates no persistent backup.

Filesystem sync improves crash durability, but hardware and filesystem failures
still require manual recovery. A persistent SSH connection is not required for
the install phase; server reboot or killing the entire process terminates it.

## Source, tests, and Actions

Go 1.26+, standard library only. Layout:

```text
cmd/cpa-updater/       application source
  *_test.go           focused unit tests (excluded from builds)
tests/                black-box CLI tests using a prebuilt executable
.github/workflows/     native amd64 and arm64 builds, tests, and tag releases
```

GitHub Actions builds and tests this updater on native amd64 and arm64 runners.
Unit tests cover package layout, version selection/comparison, checksums, locking,
cleanup, service rollback, and self-update. CLI tests exercise the actual binary
with a test-only wget substitute, including real SIGINT/SIGTERM/SIGHUP cancellation,
cleanup, and running-binary replacement. Tests operate in temporary directories
and use mocked service control. They never restart deployed services.

Pushes to `main` and pull requests run CI and upload artifacts retained for seven
days. A `v*` tag publishes both tested binaries and a combined `checksums.txt`.
The repository used for self-update is injected from `GITHUB_REPOSITORY` at build
time. Manual dispatch produces development artifacts, with no release on a branch.

All builds/tests for this project run in Actions. This project never builds or
tests CLIProxyAPI or CPA Usage Keeper themselves. Test fixtures and Go source
files are excluded from the released executables.

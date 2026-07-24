# Keith Feed Migration and Installer Recovery Design

**Date:** 2026-07-23

## Goal

Make Wattline and Starwatch coexist in the shared signed `keithah` OpenWrt feed without package file clashes or recursive `opkg` failures. Normal `opkg upgrade` must migrate safely, and rerunning either product installer must recover interrupted or partially configured installations.

## Problem

Wattline 0.1.4 and Starwatch 0.1.3 both ship `/usr/libexec/keithah-feed-migrate` from their daemon packages. A router with Starwatch installed therefore rejects a new Wattline installation because two packages claim the same file.

The shared helper also runs `opkg print-architecture`. When a package `postinst` invokes it, the outer `opkg install`, `opkg upgrade`, or `opkg configure` transaction already owns `/var/lock/opkg.lock`. The nested `opkg` command fails, leaving packages unpacked or partially configured.

The reported GL-XE3000 installation was a new Wattline installation, but it encountered both defects because Starwatch was already installed and was processed during the same package transaction.

## Considered Approaches

### Product-specific helpers — selected

Ship the same reviewed migration logic at product-owned paths:

- `/usr/libexec/wattline-feed-migrate`
- `/usr/libexec/starwatch-feed-migrate`

The helpers never invoke `opkg`. Package scripts supply their known package architecture explicitly, while standalone installers perform architecture discovery before starting a package transaction.

This avoids ownership collisions, preserves automatic migration during upgrades, and gives each package a clean lifecycle.

### Shared feed-support package

A `keithah-feed-config` package could own one helper and the signing key. This is conceptually tidy after migration, but bootstrapping it conflicts with the already-owned shared path and introduces dependency and replacement ordering risk on constrained routers. It does not remove the need for a transitional rename, so it is not selected.

### Inline migration in every `postinst`

Duplicating the migration body inside each control script avoids shared files, but makes a security-sensitive atomic file update harder to test and keep identical. It is not selected.

## Package Architecture

Wattline and Starwatch retain independent daemon packages and product-specific installers. They share source-level migration behavior and the same feed identity, key, and URL, but do not share an installed payload path.

Each daemon package installs its product-specific helper and invokes it from `postinst` only for a live root. Image-root installation remains a no-op for runtime migration. The package script passes `aarch64_cortex-a53` explicitly; `opkg` has already admitted the package for that architecture, so querying the package manager again is unnecessary and unsafe.

The helper validates the supplied architecture before modifying any files. It then atomically:

1. installs or refreshes the Keith feed public key;
2. removes only managed `starwatch`, `wattline`, and duplicate `keithah` feed records;
3. writes one `src/gz keithah https://keithah.github.io/openwrt-packages` record;
4. preserves unrelated feed records and existing file metadata;
5. leaves a valid configuration if it is run repeatedly.

No package lifecycle script may execute `opkg`.

On upgrade, removal of the old product version removes its legacy `/usr/libexec/keithah-feed-migrate` payload. The replacement version installs only its product-specific helper. A fresh Wattline installation beside an older Starwatch release no longer collides because Wattline does not claim the legacy path.

## Installer and Recovery Flow

The one-line installers remain the primary bootstrap and repair interface. Before invoking `opkg install`, an installer:

1. determines and validates router architecture while no package transaction is active;
2. installs the shared key and feed configuration atomically;
3. runs `opkg update`;
4. detects already-installed Keith daemon packages;
5. upgrades only those already-installed daemons to feed versions containing the safe migration scripts;
6. installs or upgrades the requested product packages;
7. lets the product's existing service verification and diagnostics run.

Step 5 does not install Starwatch merely because Wattline is requested, or Wattline merely because Starwatch is requested. It only repairs a Keith product already present on the router. This is needed for routers left with an old daemon in an unpacked or failed-configuration state: replacing that daemon first removes its recursive `postinst` and legacy shared-file ownership.

The recovery operations are idempotent. Rerunning `install-wattline.sh` after the reported failure is therefore supported and should finish the Wattline installation while repairing the already-present Starwatch package. A routine `opkg update && opkg upgrade` also works without requiring an installer rerun once the fixed releases are in the feed.

## Failure Handling

The installer stops before package mutation on an unsupported architecture or invalid feed setup. It reports which phase failed: architecture preflight, feed migration, index update, installed-product repair, requested-product installation, or service verification.

If feed-file or key replacement fails, temporary files are removed and the preexisting destination remains intact. Package configuration failures remain visible; the installer must not hide `opkg` errors or claim success based only on downloaded packages.

The recovery flow must tolerate these starting states:

- Starwatch configured, Wattline absent;
- Starwatch unpacked with a failed legacy `postinst`, Wattline absent or partially attempted;
- Wattline configured, Starwatch absent;
- either or both fixed products already configured;
- legacy and shared feed records both present;
- an unrelated final feed record without a trailing newline.

## Cross-Repository Scope

The implementation spans:

- `openwrt-wattline`: product-specific helper, lifecycle wiring, installer recovery, and tests;
- `openwrt-starwatch`: the corresponding helper, lifecycle wiring, installer recovery, version bump, and tests;
- `openwrt-packages`: a publisher validation that rejects duplicate non-directory payload paths across published packages.

The publisher validation is the durable guard against another cross-product ownership collision. Intentional shared directories are ignored; regular files and symbolic links must have exactly one owning package unless an explicit, reviewed exception is added.

## Testing

Both product repositories will test:

- helper rejection of unsupported or missing explicit architecture before mutation;
- successful and idempotent feed/key migration without an `opkg` executable available;
- preservation of unrelated feed content and metadata;
- absence of `opkg` calls from package lifecycle scripts;
- package payload ownership of only the product-specific helper;
- clean fresh installation with the other product represented as already installed;
- recovery ordering for a simulated legacy or failed-configured peer product;
- installer failure propagation and phase-specific diagnostics;
- generated IPK format and metadata requirements, including gzip-compressed ustar members.

The shared publisher will unpack fixture IPKs and fail when two packages own the same non-directory path. A fixture covering the original `/usr/libexec/keithah-feed-migrate` collision will prove the guard detects this regression.

Before release, build both product package sets, assemble the shared feed, and run their complete shell and application test suites. On a GL.iNet router, verify a fresh Wattline install with Starwatch already present, an ordinary `opkg upgrade`, an interrupted-install recovery by rerunning the installer, daemon startup, and preservation of both applications.

## Release and Router Recovery

Fixed Wattline and Starwatch releases must be published to the shared feed together before advising users to recover. The shared feed index must expose the fixed versions before either new installer is published.

For the currently affected GL-XE3000, the supported recovery is to rerun the published Wattline installer after the coordinated release. The installer upgrades the already-installed Starwatch daemon first, then completes Wattline. Manual deletion of package database records, forced overwrites, and rebooting during package configuration are explicitly avoided.

## Success Criteria

- Starwatch and Wattline packages have no conflicting installed file paths.
- No package lifecycle script recursively invokes `opkg`.
- `opkg update && opkg upgrade` performs feed migration and completes successfully.
- A fresh Wattline install succeeds when Starwatch is already installed.
- Rerunning the Wattline installer repairs the observed partial installation.
- The publisher rejects future cross-package file collisions before deployment.

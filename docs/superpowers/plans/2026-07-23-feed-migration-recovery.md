# Keith Feed Migration Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the Starwatch/Wattline feed-helper collision and recursive `opkg` lock failure, make every published Keith installer repair affected routers, and prevent duplicate IPK payload ownership from reaching the shared feed.

**Architecture:** Wattline and Starwatch ship equivalent migration logic under product-owned helper paths and pass their fixed package architecture into helpers that never execute `opkg`. The shared publisher prepends one tested recovery preamble to every product installer and rejects duplicate regular-file paths across all candidate IPKs before publishing an atomic feed.

**Tech Stack:** POSIX shell, OpenWrt `opkg`, gzip-compressed POSIX ustar IPKs, Python 3.13 `tarfile`/`unittest`, GNU Make, GitHub Actions.

## Global Constraints

- No package lifecycle script may execute `opkg`.
- Preserve unrelated `/etc/opkg/customfeeds.conf` content and metadata.
- Keep `src/gz keithah https://keithah.github.io/openwrt-packages` and signing key `f6c72c675c844b91` unchanged.
- Support only `aarch64_cortex-a53`; reject unsupported architecture before filesystem mutation.
- Do not install Wattline or Starwatch merely to repair the other product; repair only Keith daemons already present.
- IPKs remain gzip-compressed ustar wrappers, never ar/deb containers.
- Publish the fixed Starwatch release before publishing installers that promise automatic recovery.
- Do not reboot or modify the affected router until fixed signed-feed artifacts are available.

## Repository Preparation

Use isolated worktrees during execution. Wattline local `main` contains unreleased work and is behind the published v0.1.4 release commits, so merge `origin/main` into the feature branch without rebasing or dropping either history. Start Starwatch from its current `origin/main`; start the publisher from `codex/feed-publisher` after confirming whether that branch has already merged to its `main`.

Expected release versions are Wattline `0.1.5` and Starwatch `0.1.4`. If either version already exists as an immutable GitHub release when executing this plan, increment only that product to the next patch version and update its tests and tag consistently.

---

### Task 1: Make Wattline's package migration helper transaction-safe

**Files:**
- Rename: `package/keithah-feed-migrate.sh` → `package/wattline-feed-migrate.sh`
- Modify: `package/wattlined/CONTROL/postinst`
- Modify: `package/Makefile`
- Modify: `package/tests/feed-migrate-test.sh`
- Modify: `package/tests/release-inventory_test.sh`

**Interfaces:**
- Consumes: argument 1 containing the package architecture; `KEITHAH_ROOT` optionally redirecting `/` for tests.
- Produces: `/usr/libexec/wattline-feed-migrate aarch64_cortex-a53`, which atomically installs the shared feed/key and never invokes `opkg`.

- [ ] **Step 1: Rewrite the helper tests to require explicit architecture and no `opkg`**

Change the test harness to run:

```sh
env -i PATH="$case_dir/bin:$PATH" KEITHAH_ROOT="$case_dir/root" \
	/bin/sh "$script" "${1:-aarch64_cortex-a53}"
```

Do not create an `opkg` mock. Add cases asserting that missing and `all` arguments fail before either managed file changes, and that the supported argument succeeds when `PATH` contains no `opkg`. Change package-contract assertions to require:

```sh
grep -F 'wattline-feed-migrate.sh $(OUT)/stage/usr/libexec/wattline-feed-migrate' "$makefile"
grep -F '/usr/libexec/wattline-feed-migrate aarch64_cortex-a53' "$postinst"
! grep -F '/usr/libexec/keithah-feed-migrate' "$postinst"
```

- [ ] **Step 2: Run the focused test and confirm the red state**

Run: `sh package/tests/feed-migrate-test.sh`

Expected: FAIL because the old helper discovers architecture through `opkg` and the package still owns `/usr/libexec/keithah-feed-migrate`.

- [ ] **Step 3: Rename the helper and replace architecture discovery**

At the start of `package/wattline-feed-migrate.sh`, retain `set -eu`, `KEITHAH_ROOT`, atomic replacement, metadata preservation, and cleanup traps, but replace every `opkg` check with:

```sh
[ "$#" -eq 1 ] || fail 'expected package architecture argument'
[ "$1" = aarch64_cortex-a53 ] || fail 'this feed supports aarch64_cortex-a53 only'
```

Make no other feed/key transformation changes.

- [ ] **Step 4: Wire only the product-owned helper into the package**

Change `package/Makefile` staging to:

```make
cp wattline-feed-migrate.sh $(OUT)/stage/usr/libexec/wattline-feed-migrate
chmod 0755 $(OUT)/stage/usr/libexec/wattline-feed-migrate
```

Change the live-root portion of `package/wattlined/CONTROL/postinst` to:

```sh
/usr/libexec/wattline-feed-migrate aarch64_cortex-a53
```

Keep the `IPKG_INSTROOT` guard before this call and Wattline initialization after it.

- [ ] **Step 5: Update release inventory coverage**

Extract `./usr/libexec/wattline-feed-migrate` from the `wattlined` IPK, compare it with `package/wattline-feed-migrate.sh`, verify mode `-rwxr-xr-x`, and fail if `./usr/libexec/keithah-feed-migrate` appears in `data.tar.gz`.

- [ ] **Step 6: Run Wattline package and application verification**

Run:

```bash
sh package/tests/feed-migrate-test.sh
go test ./... -count=1
make -C package all
sh package/tests/release-inventory_test.sh package/out 0.1.5
```

Expected: all tests PASS; five 0.1.5 IPKs build; `wattlined_0.1.5_aarch64_cortex-a53.ipk` owns only `/usr/libexec/wattline-feed-migrate`.

- [ ] **Step 7: Commit the Wattline package fix**

```bash
git add package/Makefile package/wattlined/CONTROL/postinst package/wattline-feed-migrate.sh \
  package/tests/feed-migrate-test.sh package/tests/release-inventory_test.sh
git add -u package/keithah-feed-migrate.sh
git commit -m "fix(package): make feed migration transaction-safe"
```

---

### Task 2: Make Starwatch's package migration helper transaction-safe

**Files:**
- Rename: `package/keithah-feed-migrate.sh` → `package/starwatch-feed-migrate.sh`
- Modify: `package/starwatchd/CONTROL/postinst`
- Modify: `package/Makefile`
- Modify: `package/tests/feed-migrate-test.sh`
- Modify: `package/tests/release-inventory-test.sh`

**Interfaces:**
- Consumes: the same explicit architecture and `KEITHAH_ROOT` contract as Task 1.
- Produces: `/usr/libexec/starwatch-feed-migrate aarch64_cortex-a53`, with no path overlap with Wattline.

- [ ] **Step 1: Add the Starwatch red tests**

Remove the mock `opkg` dependency from `package/tests/feed-migrate-test.sh`. Invoke the helper with an explicit argument, assert missing/unsupported arguments leave both managed files unchanged, and require the package contract:

```sh
grep -F 'starwatch-feed-migrate.sh $(OUT)/stage/usr/libexec/starwatch-feed-migrate' "$makefile"
grep -F '/usr/libexec/starwatch-feed-migrate aarch64_cortex-a53' "$postinst"
! grep -F '/usr/libexec/keithah-feed-migrate' "$postinst"
```

- [ ] **Step 2: Run the focused Starwatch test and confirm failure**

Run from `/home/keith/src/openwrt-starwatch`: `sh package/tests/feed-migrate-test.sh`

Expected: FAIL on the explicit-architecture/no-old-path contract.

- [ ] **Step 3: Implement the Starwatch helper rename and postinst call**

Use the same argument validation as Task 1:

```sh
[ "$#" -eq 1 ] || fail 'expected package architecture argument'
[ "$1" = aarch64_cortex-a53 ] || fail 'this feed supports aarch64_cortex-a53 only'
```

Stage it as `/usr/libexec/starwatch-feed-migrate`, call it with `aarch64_cortex-a53` after the live-root guard, and retain existing UCI-default/service ordering.

- [ ] **Step 4: Bump Starwatch and verify its exact IPK payload**

Set `VERSION := 0.1.4` in `package/Makefile`. Update `package/tests/release-inventory-test.sh` to extract and compare `./usr/libexec/starwatch-feed-migrate`, verify executable mode, and reject the legacy helper path.

- [ ] **Step 5: Run Starwatch verification**

Run:

```bash
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
make -C ../package test
make -C ../package VERSION=0.1.4 all
sh ../package/tests/release-inventory-test.sh ../package/out 0.1.4
```

Working directory for the first four commands: `/home/keith/src/openwrt-starwatch/router`.

Expected: all tests PASS; three 0.1.4 IPKs build; Starwatch owns only `/usr/libexec/starwatch-feed-migrate`.

- [ ] **Step 6: Commit the Starwatch package fix**

```bash
git add package/Makefile package/starwatchd/CONTROL/postinst package/starwatch-feed-migrate.sh \
  package/tests/feed-migrate-test.sh package/tests/release-inventory-test.sh
git add -u package/keithah-feed-migrate.sh
git commit -m "fix(package): avoid recursive opkg feed migration"
```

---

### Task 3: Reject cross-package payload collisions in the publisher

**Files:**
- Modify: `scripts/assemble_feed.py`
- Modify: `tests/test_assemble_feed.py`

**Interfaces:**
- Consumes: normalized regular-file names returned from each candidate IPK's `data.tar.gz`.
- Produces: `PackageRecord.installed_paths: tuple[str, ...]`; `assemble()` raises `FeedError("installed path collision: <path> owned by <a> and <b>")` before replacing output.

- [ ] **Step 1: Add a collision regression test**

Add a helper:

```python
def data_archive(*paths):
    return gzip.compress(
        _tar([(f"./{path}", path.encode(), "file") for path in paths]),
        mtime=0,
    )
```

Rewrite the Starwatch and Wattline fixture IPKs with `data_override=data_archive("usr/libexec/keithah-feed-migrate")`, call `assert_rejected`, and require an error containing the path plus both package names. Add a passing case where those IPKs instead own `usr/libexec/starwatch-feed-migrate` and `usr/libexec/wattline-feed-migrate`.

- [ ] **Step 2: Run the collision test and confirm it fails**

Run:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest \
  tests.test_assemble_feed.AssembleFeedTest.test_rejects_cross_package_installed_path_collision -v
```

Expected: FAIL because assembly currently validates archive safety but discards installed paths.

- [ ] **Step 3: Retain normalized data paths in package records**

Add this immutable field:

```python
@dataclass(frozen=True)
class PackageRecord:
    # existing fields remain in their existing order
    installed_paths: tuple[str, ...]
```

In `_read_ipk`, retain `_ustar_members(...)` as `data_members`, normalize one leading `./`, reject duplicate names within one archive, and return `tuple(sorted(data_paths))` in `PackageRecord`. Do not render `installed_paths` into `Packages`.

- [ ] **Step 4: Reject ownership overlap before staging output**

In `assemble`, maintain `owners: dict[str, str]`. For every `record.installed_paths`, set the first owner to `record.package`; when another package owns the same path, raise:

```python
raise FeedError(
    f"installed path collision: {path} owned by {owner} and {record.package}"
)
```

Directories remain excluded because `_ustar_members(..., allow_directories=True)` already omits them. Existing symlink rejection remains unchanged.

- [ ] **Step 5: Run publisher assembly tests**

Run from the publisher worktree:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest tests.test_assemble_feed -v
```

Expected: all assembly tests PASS, including rejection of the original shared-helper collision.

- [ ] **Step 6: Commit the publisher collision guard**

```bash
git add scripts/assemble_feed.py tests/test_assemble_feed.py
git commit -m "feat(feed): reject cross-package file collisions"
```

---

### Task 4: Wrap every published installer with shared recovery

**Files:**
- Create: `scripts/installer_recovery.sh`
- Create: `tests/installer_recovery_test.sh`
- Modify: `scripts/assemble_feed.py`
- Modify: `tests/test_assemble_feed.py`
- Modify: `tests/inventory_test.py`
- Modify: `README.md`

**Interfaces:**
- Consumes: `KEITHAH_ROOT` (default `/`), the fixed feed URL/key, `opkg print-architecture`, and upstream installer bytes.
- Produces: every `pages/install-*.sh` beginning with marker `# keithah-installer-recovery-v1`; it repairs installed `starwatchd` and `wattlined` before the upstream installer body runs.

- [ ] **Step 1: Add shell tests for the recovery preamble**

Create a mock `opkg` that logs commands and implements `print-architecture`, `update`, `status <package>`, and `install <package>`. Concatenate `scripts/installer_recovery.sh` with a body that appends `upstream` to the log. Cover these exact sequences:

```text
no peers:       print-architecture → update → status starwatchd → status wattlined → upstream
Starwatch peer: print-architecture → update → status starwatchd → install starwatchd → status wattlined → upstream
both peers:     ... → install starwatchd → ... → install wattlined → upstream
repair failure: ... → install starwatchd, then stop without upstream
bad arch:       print-architecture, then stop before feed mutation/update/upstream
```

Represent installed peers in the mock with `MOCK_INSTALLED='starwatchd wattlined'`. Represent failure with `MOCK_INSTALL_FAIL=starwatchd`.

- [ ] **Step 2: Add publisher tests requiring generated wrappers**

Change `test_assembles_deterministic_sorted_index_and_exact_inventory` so each output installer must start with the marker, contain the source installer's bytes exactly once after the preamble, and be mode `0755`. Update `tests/inventory_test.py` to reject any deployable `install-*.sh` missing that marker.

- [ ] **Step 3: Run the new tests and confirm the red state**

Run:

```bash
sh tests/installer_recovery_test.sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest \
  tests.test_assemble_feed tests.inventory_test -v
```

Expected: FAIL because the recovery script does not exist and assembly currently copies upstream installers verbatim.

- [ ] **Step 4: Implement the standalone recovery preamble**

Create `scripts/installer_recovery.sh` as a POSIX fragment with `set -eu` and marker `# keithah-installer-recovery-v1`. Prefix all variables and functions with `_keithah_`. It must:

```sh
_keithah_root=${KEITHAH_ROOT:-/}
_keithah_feed_url=https://keithah.github.io/openwrt-packages
_keithah_supported_arch=aarch64_cortex-a53
_keithah_daemons='starwatchd wattlined'
```

Validate root, `opkg`, `wget`, and architecture before mutation. Reuse the proven atomic feed/key algorithm from the product installers, with prefixed temporary variables and cleanup trap. Run `opkg update`; then, for each daemon, run `opkg status "$daemon"`. If present, run `opkg install "$daemon"` in its own transaction and abort with `keithah installer recovery: installed-product repair failed: <daemon>` on failure. Missing daemons are skipped. Finish by clearing the trap and temporary variables, but do not `exit`, so the appended upstream installer executes.

- [ ] **Step 5: Make assembly prepend the recovery bytes**

Add `recovery_script: Path = Path("scripts/installer_recovery.sh")` to `assemble`. Validate it is a regular non-symlink file within `MAX_INSTALLER_SIZE`. Replace the installer copy with:

```python
payload = recovery_script.read_bytes().rstrip(b"\n") + b"\n\n" + installer.read_bytes()
if len(payload) > MAX_INSTALLER_SIZE:
    raise FeedError(f"wrapped installer exceeds size limit for {spec.product}")
(staging / spec.installer).write_bytes(payload)
```

Add CLI option `--installer-recovery`, defaulting to `scripts/installer_recovery.sh`, and pass it through from `main()`.

- [ ] **Step 6: Document automatic recovery and its boundary**

In `README.md`, state that every published installer repairs only already-present `starwatchd`/`wattlined` packages before installing its own product, that it never installs absent peer products, and that ordinary `opkg update && opkg upgrade` remains supported after fixed versions enter the feed.

- [ ] **Step 7: Run the full publisher suite**

Run:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -p '*.py' -v
for test_script in tests/*.sh; do sh "$test_script"; done
for shell_file in scripts/*.sh tests/*.sh; do sh -n "$shell_file"; done
```

Expected: all Python and shell tests PASS; all four assembled installers carry the recovery marker.

- [ ] **Step 8: Commit shared installer recovery**

```bash
git add scripts/installer_recovery.sh scripts/assemble_feed.py tests/installer_recovery_test.sh \
  tests/test_assemble_feed.py tests/inventory_test.py README.md
git commit -m "fix(feed): repair failed daemon configuration from every installer"
```

---

### Task 5: Verify coordinated artifacts before publication

**Files:**
- Verify unchanged: `.github/workflows/release.yml` in Wattline and Starwatch
- Verify unchanged: `.github/workflows/pages.yml` in `openwrt-packages`
- Verify unchanged: product README version and installation instructions

**Interfaces:**
- Consumes: fixed 0.1.5 Wattline IPKs, fixed 0.1.4 Starwatch IPKs, existing Ookla releases, shared signing secret in GitHub Actions.
- Produces: one candidate signed feed whose packages have disjoint installed paths and whose four installers contain recovery v1.

- [ ] **Step 1: Run clean product builds**

Remove only generated `package/out` directories, rebuild, and run the exact full suites already required by each release workflow. Confirm with archive inspection:

```bash
tar -xOzf package/out/wattlined_0.1.5_aarch64_cortex-a53.ipk ./data.tar.gz | \
  tar -tzf - | grep 'usr/libexec/.*feed-migrate'
```

Expected Wattline output: only `./usr/libexec/wattline-feed-migrate`. Run the equivalent Starwatch command and expect only `./usr/libexec/starwatch-feed-migrate`.

- [ ] **Step 2: Assemble a local four-product feed**

Place the new product IPKs/installers and current immutable Ookla assets under the publisher's `downloads/<product>/` layout, then run:

```bash
python3 -m scripts.assemble_feed \
  --manifest sources.json \
  --downloads downloads \
  --output pages
python3 -m tests.inventory_test pages
```

Expected: PASS; exact package inventory; no payload collision; four wrapped installers.

- [ ] **Step 3: Exercise installers against a disposable opkg fixture**

Run each generated `pages/install-*.sh` with the shell-test mock environment. Seed Starwatch as present/unconfigured and verify every installer logs `opkg install starwatchd` before its upstream product install. Seed no Keith daemon and verify no peer package is installed.

- [ ] **Step 4: Review release workflow ordering**

Confirm product tag workflows create immutable IPK releases and the publisher fetches releases only after both new tags exist. If no workflow change is required, leave workflow files untouched and record that conclusion in the execution report.

- [ ] **Step 5: Record the no-change workflow decision**

Run `git diff -- .github/workflows README.md` in all three repositories and expect no output from this task. If artifact verification disproves the documented release ordering, stop and revise this plan instead of introducing an unplanned workflow change.

---

### Task 6: Publish, recover the GL-XE3000, and verify normal upgrades

**Files:**
- No source changes expected; release tags, GitHub releases, Pages deployment, and router state are external artifacts.

**Interfaces:**
- Consumes: reviewed commits from Tasks 1–5 and access to the GL-XE3000.
- Produces: fixed signed feed, configured Starwatch/Wattline/Ookla packages, and captured router verification evidence.

- [ ] **Step 1: Push reviewed product and publisher branches**

Push each repository branch without force. Confirm CI passes before merging into its release branch. Do not tag from a dirty worktree or from a commit absent on the remote.

- [ ] **Step 2: Publish Starwatch first**

Tag the verified Starwatch commit `v0.1.4`, push the tag, wait for its release workflow, and verify the release contains exactly three expected IPKs. Do not proceed until the shared publisher can fetch that immutable release.

- [ ] **Step 3: Publish Wattline and rebuild the shared feed**

Tag the verified Wattline commit `v0.1.5`, push the tag, verify its five-IPK release, then dispatch the publisher Pages workflow. Confirm `Packages.gz`, its signature, all fixed IPKs, and recovery-marked installers are live before touching the router.

- [ ] **Step 4: Recover by rerunning the Wattline installer**

On the GL-XE3000:

```sh
wget -qO- https://keithah.github.io/openwrt-packages/install-wattline.sh | sh
```

Expected ordering: feed update; installed Starwatch repair/upgrade; Wattline installation; service checks. There must be no lock error, file-clash error, or failed `starwatchd.postinst`.

- [ ] **Step 5: Verify package and daemon state**

Run:

```sh
opkg status starwatchd wattlined ookla-speedtest-cli
opkg files starwatchd | grep feed-migrate
opkg files wattlined | grep feed-migrate
/etc/init.d/starwatch status
/etc/init.d/wattlined status
logread -e starwatch
logread -e wattline
```

Expected: all installed packages report `Status: install user installed`; Starwatch and Wattline own distinct helper paths; both services are running; logs have no recursive-lock or migration failure.

- [ ] **Step 6: Prove unrelated installer and ordinary upgrade paths**

Rerun the Ookla web installer, then run `opkg update && opkg upgrade`. Expected: both commands complete without retrying a broken `starwatchd.postinst`, without installing absent peer products, and without changing the configured feed more than once.

- [ ] **Step 7: Capture recovery evidence**

Record released versions, workflow URLs, relevant package status, helper ownership, and pass/fail results in the Wattline verification documentation. Redact router credentials, bearer tokens, and pairing PINs from committed logs.

---

### Publication-gate follow-up approved after Task 5 verification

Task 5 found that the original plan's assumption of manual two-tag ordering was not enforceable: the hourly Pages workflow independently fetched each product's latest release. The following two focused tasks are required before Task 6.

### Task 5A: Synchronize candidate versions in product tests and documentation

**Files:**
- Modify: Wattline `package/tests/rtl8761b-lifecycle_test.sh`
- Modify: Wattline `README.md`
- Modify: Starwatch `README.md`

- [ ] Change the Wattline lifecycle fixture expectation and all candidate examples from 0.1.4 to 0.1.5.
- [ ] Change Starwatch public-feed, upgrade, package, build, and release examples from 0.1.3 to 0.1.4.
- [ ] Run each product's exact release workflow test sequence and package inventory.
- [ ] Commit product changes independently with version-specific subjects.

### Task 5B: Add a fail-closed publisher release floor

**Files:**
- Modify: publisher `sources.json`
- Modify: publisher `scripts/assemble_feed.py` or `scripts/fetch_releases.py`
- Modify: publisher `tests/test_fetch_releases.py` and/or `tests/test_assemble_feed.py`
- Modify: publisher `README.md`

- [ ] Add a strict `minimum_tag` manifest field for all four products: `v0.1.4` Starwatch, `v0.1.5` Wattline, `v1.2.0` CLI Speedtest, and `v1.2.0` Web Speedtest.
- [ ] Validate tags as semver and reject any stable latest release below its product floor before downloading or assembling artifacts.
- [ ] Add tests proving a lower Starwatch or Wattline tag fails closed and all current fixture tags pass.
- [ ] Document that Pages will not deploy a mixed-generation feed; the floors are raised with each coordinated product rollout.
- [ ] Run the full publisher Python/shell/syntax suites and assemble the local four-product candidate.
- [ ] Do not require GitHub's optional `immutable` boolean for the current Ookla releases; the existing tag, asset allowlist, canonical installer, hash, signature, and inventory checks remain the integrity boundary.
- [ ] Commit the publisher gate independently and re-run Task 5 verification before release publication.

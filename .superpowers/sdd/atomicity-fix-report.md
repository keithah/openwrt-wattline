# Atomic feed/key replacement fix report

## RED evidence

Added focused fault-injection cases to Wattline, Starwatch, and publisher recovery tests. Each intercepts the second (feed) destination rename after the key replacement. Before the implementation, the tests failed because the key remained replaced (and the publisher case reported the changed key), demonstrating mixed old/new state.

## Implementation

- Stage same-directory backups for each existing feed and key destination.
- Mark the transaction active before either replacement; cleanup traps remove partial destinations and restore backups (or leave absent destinations absent) on errors and HUP/INT/TERM.
- Remove backups only after both replacements succeed; preserve existing metadata and all prior migration behavior.
- Added release workflow comment update from v0.1.4 to v0.1.5.

## GREEN and verification

- Wattline `package/tests/feed-migrate-test.sh`: passed.
- Starwatch `package/tests/feed-migrate-test.sh`, release-inventory-test.sh, feed-artifact-test.sh, install-test.sh, uci-defaults-route-test.sh: passed.
- Publisher `tests/installer_recovery_test.sh`: passed; `python3 -m pytest -q tests/test_*.py`: 49 passed, 19 subtests passed.
- Shell syntax (`sh -n`) for all three helpers: passed.
- `git diff --check` in all three repositories: passed.
- Wattline `release-readiness_test.sh`: passed.

## Commits

- Wattline: `a79df28893bd816a2657294ba8908a2bd9ca594c`
- Starwatch: `7488bdcf8801b3b2a0b5f778f65e730e0cbf24bd`
- Publisher: `bb36c8828f1e6249a787086f05f2146cbe8ea0fb`

## Concern

Wattline `release-inventory_test.sh package/out 0.1.5` reports the packaged helper differs from source because `package/out` is stale/not rebuilt in this worktree; no generated package artifacts were modified.

## Residual rollback fix

Follow-up fault injection failed during staging of the second backup before the fix; the first destination backup was left unrestored. Rollback is now armed before the first backup move, and cleanup independently restores only backups that actually exist while preserving an untouched second destination. Focused second-backup tests and the prior second-replacement tests pass for all three products.

Attempted `make -j2 package/out` before rerunning Wattline inventory; the inventory still reports the pre-existing packaged-helper mismatch, so generated artifacts remain untouched.

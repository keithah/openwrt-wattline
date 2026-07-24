# Task 5A Report

## Scope

Synchronized candidate-version assertions and README examples only:

- Wattline candidate `0.1.5` in the RTL8761B lifecycle assertion and README.
- Starwatch candidate `0.1.4` in README examples.
- Starwatch's historical `0.1.3` migration-bridge note was preserved.

No package behavior, workflows, release tags, publisher files, router state, or remote state were changed.

## TDD evidence

### RED (before edits)

Command:

```sh
sh package/tests/rtl8761b-lifecycle_test.sh
```

Result: failed with exit status `1` and no stdout. The stale assertion expected `Version: 0.1.4` while the candidate build path is `0.1.5`.

### GREEN (after edits)

The same lifecycle test passed:

```text
RTL8761B lifecycle tests passed
```

## Verification

### Wattline

All exact release checks passed:

- `go test ./... -count=1` — all Go packages passed.
- All 11 shell tests listed in `.github/workflows/release.yml` — passed, including RTL8761B lifecycle and artifact/driver tests.
- `node package/tests/luci_behavior_test.js` — passed.
- `node package/tests/power_loss_behavior_test.js` — passed.
- `make -C package VERSION=0.1.5 all` — built all five IPKs and metadata checks passed.
- `sh package/tests/release-inventory_test.sh package/out 0.1.5` — passed.
- `sh -n package/install.sh` — passed.
- `git diff --check` — passed.

### Starwatch

All exact release checks passed:

- `go test -race ./...` — passed.
- `go vet ./...` — passed.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` — passed.
- `make -C ../package test` from `router/` — passed (UCI route, feed migration, installer, feed artifact, and release workflow tests).
- `make -C package VERSION=0.1.4 all` — built all three IPKs.
- `sh package/tests/release-inventory-test.sh package/out 0.1.4` — passed.
- `sh -n package/install.sh` — passed.
- `git diff --check` — passed.

Both worktrees were clean after their independent commits; no unintended files remained.

## Commits

- Wattline: `421118bd8a1699fcbc85c481d52ab7df8a7cb119` — `test: synchronize Wattline 0.1.5 candidate`
- Starwatch: `613db2d7a4f7f9ca24cc92398a42c7d5bca91c40` — `docs: synchronize Starwatch 0.1.4 candidate`

## Concerns

None. The only remaining `0.1.3` README reference is the explicitly historical frozen migration bridge.

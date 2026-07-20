# Shared Keith OpenWrt Feed Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish Starwatch and Wattline through one signed `keithah` opkg feed while retaining product-specific one-line installers.

**Architecture:** A dedicated `keithah/openwrt-packages` GitHub Pages repository aggregates the latest release IPKs from both product repositories, generates one package index, signs it with the existing publisher key, and publishes the two installer scripts. Both products register the same `src/gz keithah` entry and remove their legacy managed entries.

**Tech Stack:** POSIX shell, OpenWrt opkg/usign, GitHub Actions, GitHub Pages.

## Global Constraints

- Preserve all unrelated entries in `/etc/opkg/customfeeds.conf` byte-for-byte.
- Keep global opkg signature verification enabled.
- Reuse the existing Starwatch feed key as the Keith OpenWrt publisher key.
- Wattline installs and activates `wattline-rtl8761b` only when USB ID `2357:0604` or `0bda:8771` is present.
- Product release workflows publish release assets only; the shared feed owns GitHub Pages.

---

### Task 1: Migrate product installers

**Files:**
- Modify: `openwrt-wattline/package/install.sh`
- Modify: `openwrt-starwatch/package/install.sh`
- Modify: installer tests in both repositories

- [ ] Change both installers to `https://keithah.github.io/openwrt-packages` and feed name `keithah`.
- [ ] Install the shared public key with a publisher-neutral comment.
- [ ] Remove legacy `starwatch`, `wattline`, and duplicate `keithah` managed entries atomically.
- [ ] Run both installer test suites.
- [ ] Commit each repository independently.

### Task 2: Create shared feed publisher

**Files:**
- Create: `openwrt-packages/.github/workflows/pages-feed.yml`
- Create: `openwrt-packages/scripts/build-feed.sh`
- Create: `openwrt-packages/keithah-feed.pub`
- Create: `openwrt-packages/README.md`

- [ ] Download the latest release assets from `keithah/openwrt-starwatch` and `keithah/openwrt-wattline`.
- [ ] Generate a single deterministic `Packages` and `Packages.gz` containing every IPK exactly once.
- [ ] Copy `install-starwatch.sh` and `install-wattline.sh` into the Pages artifact.
- [ ] Sign `Packages` with `OPENWRT_FEED_USIGN_PRIVATE_KEY` and verify with `keithah-feed.pub`.
- [ ] Deploy the artifact through GitHub Pages.
- [ ] Add fixture-based tests for duplicate packages, missing installers, and unsigned output.

### Task 3: Stop product-owned feed deployment and update docs

**Files:**
- Modify: Wattline and Starwatch release workflows
- Modify: Wattline and Starwatch README files

- [ ] Remove product-specific GitHub Pages publication while retaining release IPKs.
- [ ] Update one-line installer URLs to the shared Pages repository.
- [ ] Document migration and ordinary `opkg update`/`opkg upgrade` behavior.
- [ ] Run both repositories' full package tests and workflow syntax checks.

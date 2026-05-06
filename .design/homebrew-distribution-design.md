# Homebrew Distribution Design for Scion CLI

**Status:** Draft  
**Author:** Design Agent  
**Date:** 2026-05-06  
**Scope:** Community-provided macOS Homebrew distribution for the Scion CLI

---

## 1. Context

Scion is a Go CLI tool (`github.com/GoogleCloudPlatform/scion`) that includes an embedded web frontend built with Vite/TypeScript. The project currently distributes pre-built binaries via GitHub Releases for four platform/architecture combinations: `darwin/amd64`, `darwin/arm64`, `linux/amd64`, and `linux/arm64`. There is no Homebrew distribution today.

This document designs what a community-provided Homebrew distribution would look like, covering formula structure, tap layout, release automation, dependency management, multi-architecture support, community maintainer model, and testing.

### Current Build System Summary

| Aspect | Detail |
|--------|--------|
| Module path | `github.com/GoogleCloudPlatform/scion` |
| Go version | 1.25.4 |
| Entry point | `cmd/scion/main.go` → `cmd.Execute()` |
| Web frontend | Vite/TypeScript in `web/`, embedded via `//go:embed` |
| CGO | Disabled for releases (`CGO_ENABLED=0`) |
| SQLite | Pure Go via `modernc.org/sqlite` (no C deps) |
| Version injection | `hack/version.sh` → `-ldflags -X pkg/version.{Version,Commit,BuildTime}` |
| Build tags | `no_embed_web` (skip web embed), `no_sqlite` (skip SQLite) |
| Release workflow | GitHub Actions on tag push, produces `.tar.gz` per platform |
| Shell completions | Cobra-generated at runtime (bash, zsh, fish) |
| License | Apache 2.0 (Google LLC) |

---

## 2. Decision: Formula vs Cask

### Options Considered

| Approach | Description | Pros | Cons |
|----------|-------------|------|------|
| **Formula (source build)** | Build from source tarball with Go + Node | Transparent build; eligible for homebrew-core | Slow install (~2 min); requires Node + Go as build deps; web build adds complexity |
| **Formula (pre-built binary)** | Download release tarball, install binary | Fast install; simple formula; no build deps | Not eligible for homebrew-core; must self-host tap |
| **Cask** | Download pre-built binary via Cask DSL | Clean multi-arch syntax; GoReleaser native support | Traditionally for `.app`/`.pkg`; less common for CLI tools; no bottles |

### Recommendation: Formula downloading pre-built binaries, in a custom tap

**Primary distribution:** A custom tap (`GoogleCloudPlatform/homebrew-scion`) with a formula that downloads pre-built binaries from GitHub Releases. This mirrors the HashiCorp/Terraform model.

**Rationale:**

1. **Web frontend build is a barrier to source builds.** A source-build formula requires both Go and Node.js as build dependencies, plus `npm ci && npm run build` before `go build`. This is fragile across Homebrew environments and slow for users.

2. **Pre-built binaries match the release model.** The project already produces CGO-free, statically linked binaries with embedded web assets. These are the canonical artifacts.

3. **Custom tap gives release control.** The project can update the formula on its own schedule, without waiting for homebrew-core review cycles. This is important for a rapidly evolving tool.

4. **homebrew-core is a future option.** If the project reaches sufficient popularity (>=225 GitHub stars for self-submitted projects) and the community wants it, a source-build formula can be submitted to homebrew-core separately. The tap can coexist.

5. **Formula over Cask for CLI tools.** While GoReleaser's `homebrew_casks` feature is available, formulas are the conventional packaging for CLI tools. Casks are primarily for GUI applications. A formula also provides the `test do` block and `depends_on` system.

---

## 3. Tap Structure

### Repository: `GoogleCloudPlatform/homebrew-scion`

```
homebrew-scion/
├── Formula/
│   └── scion.rb                    # Main formula
├── .github/
│   └── workflows/
│       └── test.yml                # CI: brew audit + brew test
├── README.md                       # Install instructions
├── CONTRIBUTING.md                 # Contribution guidelines
├── LICENSE                         # Apache 2.0
└── tap_migrations.json             # Future: migration targets if formula moves
```

**Installation:**
```bash
brew tap GoogleCloudPlatform/scion
brew install scion
```

**Short form (auto-taps):**
```bash
brew install GoogleCloudPlatform/scion/scion
```

### Naming Considerations

- The formula is named `scion` (matching the binary name).
- If a naming conflict arises with homebrew-core in the future, users can always use the fully-qualified name `GoogleCloudPlatform/scion/scion`.
- The tap includes `conflicts_with` declarations if needed.

---

## 4. Formula Design

### `Formula/scion.rb`

```ruby
class Scion < Formula
  desc "Developer agent orchestration CLI"
  homepage "https://github.com/GoogleCloudPlatform/scion"
  license "Apache-2.0"
  version "0.1.0"

  on_macos do
    on_intel do
      url "https://github.com/GoogleCloudPlatform/scion/releases/download/v#{version}/scion-darwin-amd64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_DARWIN_AMD64"
    end
    on_arm do
      url "https://github.com/GoogleCloudPlatform/scion/releases/download/v#{version}/scion-darwin-arm64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_DARWIN_ARM64"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/GoogleCloudPlatform/scion/releases/download/v#{version}/scion-linux-amd64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_LINUX_AMD64"
    end
    on_arm do
      url "https://github.com/GoogleCloudPlatform/scion/releases/download/v#{version}/scion-linux-arm64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_LINUX_ARM64"
    end
  end

  def install
    bin.install "scion"

    generate_completions_from_executable(bin/"scion", "completion")
  end

  test do
    assert_match "scion version", shell_output("#{bin}/scion version 2>&1")
    assert_match version.to_s, shell_output("#{bin}/scion version 2>&1")
  end
end
```

### Key Design Decisions in the Formula

1. **Platform blocks (`on_macos`/`on_linux` + `on_intel`/`on_arm`):** Each platform/arch combination downloads the correct pre-built binary. This is the standard pattern for pre-built binary formulas (used by HashiCorp, etc.).

2. **Shell completions via `generate_completions_from_executable`:** Cobra supports `scion completion {bash,zsh,fish}`. Homebrew's built-in helper runs these commands at install time and places the output in the correct completion directories. No pre-generated files needed.

3. **No runtime dependencies:** The binary is statically linked (`CGO_ENABLED=0`) with no external library dependencies. `depends_on` is empty.

4. **Test block:** Validates the binary runs and reports the correct version. Kept intentionally minimal since the binary requires hub connectivity for most operations.

5. **License declaration:** `license "Apache-2.0"` uses the SPDX identifier, which Homebrew requires.

---

## 5. Multi-Architecture Support

### How It Works

The formula uses Homebrew's conditional DSL:

```
on_macos → on_intel  → scion-darwin-amd64.tar.gz
on_macos → on_arm    → scion-darwin-arm64.tar.gz
on_linux → on_intel  → scion-linux-amd64.tar.gz
on_linux → on_arm    → scion-linux-arm64.tar.gz
```

This maps exactly to the four binaries produced by `build-release.yml`.

### Rosetta / Translation Layer

On Apple Silicon Macs running x86_64 binaries under Rosetta 2, Homebrew detects the native architecture (`arm64`), not the translated one. The formula always installs the native binary. No special handling is needed.

### `cellar: :any_skip_relocation`

Since the binary is statically linked with no shared library dependencies, bottles (if ever built) are relocatable. This means the formula works correctly regardless of the Homebrew prefix (`/opt/homebrew` on ARM, `/usr/local` on Intel).

---

## 6. Release Automation

### Goal

When a new version tag is pushed to `GoogleCloudPlatform/scion`, the tap formula should be updated automatically with new URLs and checksums.

### Option A: GitHub Action in the main repo (Recommended)

Add a step to the existing `build-release.yml` workflow that updates the tap formula after the release is created.

```yaml
# Appended to build-release.yml, after the release job
update-homebrew:
  name: Update Homebrew Formula
  needs: release
  runs-on: ubuntu-latest
  if: "!contains(github.ref_name, '-')"  # Skip pre-releases (e.g., v1.0.0-rc1)
  steps:
    - name: Checkout tap
      uses: actions/checkout@v6
      with:
        repository: GoogleCloudPlatform/homebrew-scion
        token: ${{ secrets.HOMEBREW_TAP_TOKEN }}

    - name: Update formula
      env:
        VERSION: ${{ github.ref_name }}
      run: |
        # Strip leading 'v' for formula version
        FORMULA_VERSION="${VERSION#v}"

        # Download release artifacts and compute SHA256
        for ARCH in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
          URL="https://github.com/GoogleCloudPlatform/scion/releases/download/${VERSION}/scion-${ARCH}.tar.gz"
          SHA=$(curl -sL "$URL" | sha256sum | cut -d' ' -f1)
          declare "SHA_$(echo $ARCH | tr '-' '_')=$SHA"
        done

        # Update formula using sed
        sed -i "s/version \".*\"/version \"${FORMULA_VERSION}\"/" Formula/scion.rb
        sed -i "s|PLACEHOLDER_SHA256_DARWIN_AMD64|${SHA_darwin_amd64}|" Formula/scion.rb
        sed -i "s|PLACEHOLDER_SHA256_DARWIN_ARM64|${SHA_darwin_arm64}|" Formula/scion.rb
        sed -i "s|PLACEHOLDER_SHA256_LINUX_AMD64|${SHA_linux_amd64}|" Formula/scion.rb
        sed -i "s|PLACEHOLDER_SHA256_LINUX_ARM64|${SHA_linux_arm64}|" Formula/scion.rb

        # Also update the URL version references
        # (URLs use v-prefixed version via #{version} interpolation,
        #  but the download URL uses the tag name directly)

    - name: Commit and push
      run: |
        git config user.name "scion-bot"
        git config user.email "scion-bot@google.com"
        git add Formula/scion.rb
        git commit -m "scion ${VERSION}"
        git push
```

**Required setup:**
- A GitHub PAT (`HOMEBREW_TAP_TOKEN`) with write access to the tap repository, stored as a secret in the main repo.
- The bot account needs push access to `GoogleCloudPlatform/homebrew-scion`.

### Option B: GoReleaser integration

If the project adopts GoReleaser in the future, the `brews` or `homebrew_casks` section can auto-generate and push the formula. However, the project currently uses a custom GitHub Actions build pipeline without GoReleaser, so this would require a build system migration.

### Option C: Dedicated update script

A standalone script (`hack/update-homebrew.sh`) that can be run manually or via CI:

```bash
#!/bin/bash
# Usage: ./hack/update-homebrew.sh v1.2.3
set -euo pipefail

VERSION="${1:?Usage: $0 <tag>}"
FORMULA_VERSION="${VERSION#v}"
TAP_DIR="$(mktemp -d)"

git clone git@github.com:GoogleCloudPlatform/homebrew-scion.git "$TAP_DIR"

for ARCH in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
  URL="https://github.com/GoogleCloudPlatform/scion/releases/download/${VERSION}/scion-${ARCH}.tar.gz"
  SHA=$(curl -sL "$URL" | sha256sum | cut -d' ' -f1)
  PLACEHOLDER="PLACEHOLDER_SHA256_$(echo "$ARCH" | tr '[:lower:]-' '[:upper:]_')"
  sed -i "s/${PLACEHOLDER}/${SHA}/" "$TAP_DIR/Formula/scion.rb"
done

sed -i "s/version \".*\"/version \"${FORMULA_VERSION}\"/" "$TAP_DIR/Formula/scion.rb"

cd "$TAP_DIR"
git commit -am "scion ${VERSION}"
git push
rm -rf "$TAP_DIR"
```

### Recommendation

**Use Option A** (GitHub Action in main repo). It integrates with the existing release pipeline, requires no new tooling, and ensures the tap is always updated when a release is published. Option C can serve as a fallback for manual updates.

### Pre-release Handling

The `if: "!contains(github.ref_name, '-')"` guard ensures pre-release tags (e.g., `v1.0.0-rc1`, `v1.0.0-beta.2`) do not update the Homebrew formula. Only stable releases propagate to the tap.

---

## 7. Dependencies

### Runtime Dependencies

**None.** The Scion binary is statically linked (`CGO_ENABLED=0`) with all dependencies compiled in, including:
- Pure Go SQLite (`modernc.org/sqlite`)
- Embedded web frontend (`//go:embed`)
- All Go library dependencies

No `depends_on` is needed in the formula.

### Build Dependencies (source-build formula only)

If a source-build formula is ever needed (e.g., for homebrew-core submission), the dependencies would be:

```ruby
depends_on "go" => :build
depends_on "node" => :build

def install
  # Build web frontend
  cd "web" do
    system "npm", "ci"
    system "npm", "run", "build"
  end

  # Build Go binary with embedded web assets
  ldflags = %W[
    -X github.com/GoogleCloudPlatform/scion/pkg/version.Version=#{version}
    -X github.com/GoogleCloudPlatform/scion/pkg/version.Commit=#{tap.user}
    -X github.com/GoogleCloudPlatform/scion/pkg/version.BuildTime=#{time.iso8601}
  ]
  system "go", "build", *std_go_args(ldflags:), "./cmd/scion"

  generate_completions_from_executable(bin/"scion", "completion")
end
```

This is significantly more complex than the pre-built binary formula and is not recommended for the initial distribution.

### Optional External Tools

Scion may interact with external tools at runtime (Docker, kubectl, git, etc.), but these are user-environment dependencies, not formula dependencies. They should be documented in the README, not declared via `depends_on`, since the CLI operates in degraded mode without them rather than failing entirely.

---

## 8. Community Maintainer Model

### Ownership Model

The tap is maintained by the Scion project team under the `GoogleCloudPlatform` GitHub organization. Community contributions are welcome via pull requests.

### Roles

| Role | Responsibility | Access |
|------|---------------|--------|
| **Tap owner** (Scion team) | Formula updates, release automation, tap CI, review PRs | Write access to tap repo |
| **Community contributors** | Bug reports, formula improvements, testing on new macOS versions | Fork + PR |
| **Release bot** (GitHub Actions) | Automated formula updates on new releases | PAT with write access |

### Contribution Guidelines (`CONTRIBUTING.md`)

```markdown
# Contributing to homebrew-scion

## Reporting Issues
- If `brew install scion` fails, open an issue with:
  - `brew config` output
  - `brew doctor` output
  - macOS version and architecture (`uname -m`)

## Formula Changes
- Test locally: `brew install --build-from-source ./Formula/scion.rb`
- Run audit: `brew audit --strict Formula/scion.rb`
- Run tests: `brew test scion`
- Submit a PR with a description of the change

## Version Bumps
Version bumps are automated. Do not submit PRs that only change
the version and checksums — these will be overwritten by automation.
```

### Versioning Policy

- The tap formula version tracks the Scion release version exactly.
- Formula-only changes (e.g., fixing completions, adding a `caveat`) are committed without a version bump.
- The tap does not maintain its own version numbering.

---

## 9. Testing

### Formula-Level Tests

The `test do` block in the formula runs during `brew test scion`:

```ruby
test do
  assert_match "scion version", shell_output("#{bin}/scion version 2>&1")
  assert_match version.to_s, shell_output("#{bin}/scion version 2>&1")
end
```

This validates:
1. The binary executes on the installed platform.
2. The version string matches the formula version (catches packaging errors where the wrong binary is bundled).

The test intentionally does not exercise hub connectivity, authentication, or agent operations — these require infrastructure that won't be available in the test environment.

### Tap CI (`.github/workflows/test.yml`)

```yaml
name: Test Formula
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  test:
    strategy:
      matrix:
        os: [macos-13, macos-14, macos-15]  # Intel, M1, M2+
    runs-on: ${{ matrix.os }}
    steps:
      - name: Checkout
        uses: actions/checkout@v6

      - name: Install formula
        run: |
          brew tap-new --no-git scion/test
          cp Formula/scion.rb "$(brew --repository scion/test)/Formula/"
          brew install scion/test/scion

      - name: Audit formula
        run: brew audit --strict Formula/scion.rb

      - name: Test formula
        run: brew test scion/test/scion

      - name: Verify completions
        run: |
          brew completions link
          # Verify completion files were generated
          test -f "$(brew --prefix)/share/zsh/site-functions/_scion" || \
          test -f "$(brew --prefix)/share/bash-completion/completions/scion"
```

### Testing Matrix

| Runner | Architecture | macOS Version | Purpose |
|--------|-------------|---------------|---------|
| `macos-13` | x86_64 (Intel) | Ventura | Legacy Intel Mac support |
| `macos-14` | arm64 (M1) | Sonoma | Apple Silicon support |
| `macos-15` | arm64 (M2+) | Sequoia | Latest macOS |

This ensures the correct binary is downloaded and executes on each architecture.

### Local Testing Checklist

For maintainers testing formula changes locally:

```bash
# 1. Audit the formula for style issues
brew audit --strict --online Formula/scion.rb

# 2. Install from the local formula
brew install --formula ./Formula/scion.rb

# 3. Verify installation
brew info scion
scion version
which scion

# 4. Test completions
scion completion zsh > /dev/null
scion completion bash > /dev/null
scion completion fish > /dev/null

# 5. Run formula tests
brew test scion

# 6. Uninstall cleanly
brew uninstall scion
```

---

## 10. Future Considerations

### Path to homebrew-core

If the project wants to submit to `homebrew-core` in the future:

1. **Popularity threshold:** Needs >=225 GitHub stars (self-submitted) or >=75 stars (community-submitted).
2. **Source build required:** homebrew-core requires building from source, not pre-built binaries. The formula would need `depends_on "go" => :build` and `depends_on "node" => :build`.
3. **BrewTestBot:** All bottles are built by BrewTestBot across all supported platforms. The build must be reproducible.
4. **Coexistence:** The custom tap can coexist with a homebrew-core formula. Users who want the official signed binary use the tap; users who prefer source builds use homebrew-core.
5. **The two formulas would need different names** or a `conflicts_with` declaration.

### GoReleaser Adoption

If the project migrates to GoReleaser:

- The `homebrew_casks` section (GoReleaser v2.10+) can auto-generate and push a Cask to the tap.
- The `brews` section (deprecated but functional) can auto-generate a Formula.
- GoReleaser handles checksum computation, multi-arch URL templating, and git push to the tap automatically.
- This would replace the custom GitHub Action in the release workflow.

### macOS Code Signing and Notarization

Currently, the darwin binaries are not code-signed or notarized. On macOS, users may see a Gatekeeper warning on first run. Options:

1. **Apple Developer ID signing** in the release workflow (requires Apple Developer account, $99/year).
2. **Ad-hoc signing** (`codesign -s -`) — removes "unknown developer" warning but doesn't pass notarization.
3. **Document the workaround** in the tap README:
   ```
   If you see "scion can't be opened because Apple cannot check it for malicious software":
     xattr -d com.apple.quarantine $(brew --prefix)/bin/scion
   ```

For a Google-maintained project, Apple Developer ID signing is recommended. The `gh` CLI does this via GoReleaser post-hooks.

### Linux Homebrew (Linuxbrew)

The formula already supports Linux (`on_linux` blocks for `amd64`/`arm64`). Homebrew on Linux is officially supported, so users on Linux can also install via the tap. No additional work is needed.

### Man Pages

The project does not currently generate man pages. If man pages are added in the future (e.g., via `cobra-doc` or a custom generator), the formula can install them:

```ruby
man1.install "scion.1"
```

---

## 11. Implementation Plan

### Phase 1: Tap Setup (Day 1)

1. Create `GoogleCloudPlatform/homebrew-scion` repository.
2. Add `Formula/scion.rb` with the formula from Section 4.
3. Add `README.md` with installation instructions.
4. Add `CONTRIBUTING.md` with contribution guidelines.
5. Add `LICENSE` (Apache 2.0).
6. Add `.github/workflows/test.yml` for CI.

### Phase 2: Release Automation (Day 2)

1. Create a GitHub PAT for the release bot.
2. Add the `update-homebrew` job to `build-release.yml` in the main repo.
3. Add `hack/update-homebrew.sh` as a manual fallback.
4. Test end-to-end with a release candidate tag.

### Phase 3: Documentation and Announcement (Day 3)

1. Add Homebrew installation instructions to the main repo README.
2. Add a `INSTALLING.md` or equivalent with all distribution options.
3. Announce on relevant channels.

### Phase 4: Hardening (Week 2+)

1. Monitor issues from early adopters.
2. Consider macOS code signing / notarization.
3. Evaluate homebrew-core submission when popularity thresholds are met.
4. Evaluate GoReleaser adoption for unified release management.

---

## 12. Appendix: Comparison with Peer Projects

| Aspect | gh (GitHub CLI) | terraform | kubectl | **scion (proposed)** |
|--------|----------------|-----------|---------|---------------------|
| **Tap location** | homebrew-core | hashicorp/tap | homebrew-core | GoogleCloudPlatform/scion |
| **Formula type** | Formula (source) | Formula (binary) | Formula (source) | Formula (binary) |
| **Build deps** | Go | None | Go, rsync, bash, coreutils | None |
| **Multi-arch** | Bottles (BrewTestBot) | `on_macos`/`on_linux` conditionals | Bottles (BrewTestBot) | `on_macos`/`on_linux` conditionals |
| **Release automation** | GoReleaser + bump-homebrew-formula-action | HashiCorp internal | Community/livecheck | GitHub Actions (custom) |
| **Code signed** | Yes (Apple Developer ID) | Yes (HashiCorp) | N/A (source build) | Not yet (recommended) |
| **Completions** | Generated at install | None | Generated at install | Generated at install (Cobra) |
| **Test block** | Version + subcommand checks | Version check | Version + tree state check | Version check |

# Self-contained macOS bundle

A plan to package the standalone Panaudia server as a relocatable, self-contained
macOS distribution that includes every runtime dependency, so it can be installed
on a Mac that has never had the build toolchain, Homebrew, or the native libraries.

This is a planning record. Detailed progress is in [Status](#status) at the end.

## ⏯ Picking up where we left off (as of 2026-06-17)

**Done & verified:** Phases 1 (relocation/bundle) and 2 (config paths + cert
auto-gen) are complete and tested. The `.app` builds self-contained, double-click
→ Terminal works (user-confirmed), and the server auto-generates a self-signed
cert on first launch (into Application Support in a bundle, `./keys` elsewhere).

**Uncommitted in the working tree** (nothing committed yet — start tomorrow by
deciding branch + commit):
- `main.go` — config-root resolution, `ensureTLSCert`/`generateSelfSignedCert`,
  ticket-key-optional-when-unticketed, `PANAUDIA_TLS_AUTOGEN` config field + banner.
- `scripts/bundle_macos` — builds `Panaudia.app` (launcher + `panaudia-server` +
  8 relocated dylibs); ships no config.
- `.gitignore` — added `/dist/`.
- `plan/macos-bundle/plan.md` — this file.

**Next actions (pick up here):**
1. **Doc update** — add `PANAUDIA_TLS_AUTOGEN` to `docs/environment-variables.md`;
   document cert auto-gen + the config dir in the README.
2. **Commit Phases 1–2** — branch off `main` first (currently on `main`, clean
   before this work).
3. **Phase 4** — `scripts/sign_and_notarize_macos` (Developer ID + hardened
   runtime + notarize + staple `.app` + `.dmg`). Open: entitlement
   `com.apple.security.cs.disable-library-validation`?; script-main-executable
   notarization (compiled-launcher fallback noted in §"Double-click UX").
4. **Phase 3** — clean-machine test on a bare Mac (no Homebrew/libs); re-run
   [runtime check 2](#verifying-self-containment) *before* Phase 4 hardened-runtime
   signing (DYLD_PRINT_* is suppressed afterwards).

**⚠ Security action (separate from the bundle work):** `keys/server.key` in the
working tree is the **real Sectigo `*.panaudia.com` production private key**.
Consider rotating it and removing it from the repo tree — see the security finding
in [§B](#b-asset-path-resolution--env-var-config-phase-2).

## Decisions (agreed)

| Decision | Choice | Consequence |
|---|---|---|
| Distribution audience | **Public / arbitrary Macs** | Code signing (Developer ID) + Apple notarization + stapling are in scope. |
| Architecture | **Apple Silicon only (arm64)** | No universal rebuild of native libs; matches the Accelerate-built libs already in `/usr/local/lib`. |
| Form factor | **Signed `.app` bundle** | A `.app` is a directory convention, **not** a GUI commitment — the inner server binary stays headless, runnable directly via `Panaudia.app/Contents/MacOS/panaudia-server`; double-click goes through a launcher that opens Terminal. Chosen over a loose folder because it is a direct staple target, uses the canonical `Contents/Frameworks/` layout, and gets the standard Gatekeeper UX. (Superseded an earlier "relocatable CLI folder" idea — see [Why .app over a loose folder](#why-app-over-a-loose-folder).) |

## Background: what gets linked, and how

The build (`go build -tags=accelerate .`) produces a single Go binary. The heavy
DSP code is **statically** absorbed and needs no packaging:

- `libsaf.a`, `libsaf_example_ambi_bin.a` (Spatial Audio Framework)
- `libpanaudia_utils.a`
- openfec (static, via roc-toolkit)
- Apple **Accelerate** framework (system-provided, links via `-framework`)

The problem is the **dynamic** tail. Each of these resolves today to an absolute
path on the build machine (`/usr/local/lib` via the rpath baked in
`spacer/flags.go:12`, or `/opt/homebrew/...` Homebrew prefixes). They must be
copied into the bundle and have their install names rewritten.

### Dylib closure to bundle (8 libraries)

| Dylib | Source today | Pulled in by |
|---|---|---|
| `libroc.0.4.dylib` | `/usr/local/lib` (rpath) | roc-go binding (linked unconditionally) |
| `libuv.1.dylib` | `/opt/homebrew/opt/libuv` | libroc |
| `libssl.3.dylib` | `/opt/homebrew/opt/openssl@3` | libroc |
| `libcrypto.3.dylib` | `/opt/homebrew/Cellar/openssl@3` | via libssl |
| `libspeexdsp.1.dylib` | `/opt/homebrew/opt/speexdsp` | libroc |
| `libopus.0.dylib` | `/opt/homebrew/Cellar/opus` | `hraban/opus` binding |
| `libopusfile.0.dylib` | `/opt/homebrew/opt/opusfile` | opus binding |
| `libogg.0.dylib` | `/opt/homebrew/opt/libogg` | via opusfile |

> **Do not bundle** `libc++.1.dylib` or `libSystem.B.dylib`. They are OS-provided,
> present on every Mac, and bundling them breaks notarization / library validation.

### Why `.app` over a loose folder

The gate on loading these dylibs is **library validation**, not file location.
Under the hardened runtime (on by default, and required for notarization), a
binary will only load a dylib signed by Apple **or by the same Team ID** as the
binary. Location is irrelevant — a loose folder isn't rejected for being
"arbitrary", it'd be rejected for holding *unsigned* dylibs.

Since notarization requires every Mach-O to be Developer-ID signed with hardened
runtime regardless, **all 8 dylibs must be signed either way.** The form factor
saves no signing work — it only changes the result. The `.app` wins because:

- It is a **direct staple target** (a loose binary/folder cannot be stapled and
  would need a `.dmg`/`.pkg` wrapper just to staple).
- It uses the canonical `Contents/Frameworks/` layout and the standard
  `@executable_path/../Frameworks` rpath that `codesign`/`notarytool` expect.
- It gets the normal Gatekeeper first-launch UX rather than a quarantined
  Terminal binary.

It is **not** a GUI commitment: the inner binary remains a headless server, still
runnable directly via `Panaudia.app/Contents/MacOS/panaudia-server`.

### Runtime assets loaded relative to CWD

`main.go` opens these by working-directory-relative default paths:

- `keys/server.crt`  (`PANAUDIA_TLS_CTR_PATH`, `main.go:48`)
- `keys/server.key`  (`PANAUDIA_TLS_KEY_PATH`, `main.go:49`)
- `keys/panaudia_key.pub` (`PANAUDIA_TICKET_KEY_PATH`, `main.go:50`)
- optional `.env` (loaded from CWD if present)

A relocated binary launched from an arbitrary CWD will fail to find these unless
the launcher fixes the working directory or sets the env vars to absolute
in-bundle paths.

## The three hard problems

### A. Install-name / rpath rewriting

Every bundled dylib carries an absolute `LC_ID_DYLIB` install name, and the
binary references libroc by absolute path plus an rpath hard-coded to
`/usr/local/lib`. For relocation:

- Rewrite each dylib's own ID to `@rpath/<name>` (`install_name_tool -id`).
- Rewrite every inter-library reference (e.g. libssl → libcrypto, libroc → libuv)
  to `@rpath/<name>` (`install_name_tool -change`).
- Rewrite the binary's references and replace its `/usr/local/lib` rpath with
  `@executable_path/../Frameworks` (`install_name_tool -rpath` / `-add_rpath`).

This is mechanical but must be scripted as a recursive `otool -L` walk and then
verified — a single missed reference crashes on a clean machine.

### B. Asset path resolution & env-var config (Phase 2)

A Finder-launched `.app` inherits **no shell environment** and runs with CWD `/`,
so two things that work from a shell break in a bundle: CWD-relative key paths,
and a CWD `.env`. (A Terminal-launched `Contents/MacOS/panaudia-server` is
unaffected — it inherits the shell, so power users keep working as today.)

Resolved with a **config root** that depends on context, plus a **universal cert
fallback** — all in the standalone `main.go` only (`core`/cloud-mixer untouched):

- **Config root (`configRoot`).** Inside a `.app` (detected by
  `Contents/Info.plist` next to `Contents/MacOS/`), the root is the per-user
  `~/Library/Application Support/Panaudia/` (`os.UserConfigDir`). Everywhere else
  — dev checkout, Linux, Docker — the root is the CWD, i.e. defaults are left
  exactly as before. So Application Support is **bundle-only**.
- **Keys / ticket key.** When the matching env var (`PANAUDIA_TLS_CTR_PATH`,
  `PANAUDIA_TLS_KEY_PATH`, `PANAUDIA_TICKET_KEY_PATH`) is unset, the default
  `keys/…` is rebased to `<configRoot>/keys/…`; an explicitly-set path is honoured
  verbatim (the production/operator override, incl. the file-path contract
  cloud-mixer relies on).
- **`.env`.** `PANAUDIA_ENV_FILE` if set, else `<configRoot>/.env` in a bundle,
  else `./.env`. Precedence unchanged (`godotenv.Load` never overwrites a set
  var): **real env > `.env` > defaults**.
- **TLS cert auto-generation (`ensureTLSCert`).** After resolving the paths, if
  the cert/key files are missing the server generates a fresh ECDSA P-256
  self-signed pair *at that path* (SAN: `localhost`, `127.0.0.1`, `::1`, plus
  `PANAUDIA_ICE_HOST` if set). Universal — a fresh checkout, Linux/Docker, or the
  `.app` all get a working cert. Pure fallback: existing files are never touched
  (production always provides them), and it's disabled by `PANAUDIA_TLS_AUTOGEN=0`.
  Because it writes the file to disk, the shared core just `LoadX509KeyPair`s the
  path and finds it — no core change.
- **Ticket key is optional when unticketed.** The JWT public key only *verifies*
  tickets; when `PANAUDIA_UNTICKETED=1` (default) and no key file is present, the
  server starts without it instead of panicking. When ticketed, a missing key is
  still a clear error. Operator-supplied; never bundled, never generated.

Net effect: **nothing config-related ships in the `.app`.** Resources is empty.

> **Why nothing in the bundle?** Editing any file under `Contents/` after signing
> invalidates the code signature → Gatekeeper rejects the app. Config/data must
> live outside it; the per-user config dir also survives app updates.

> **Security finding (2026-06-17).** The repo's `keys/server.crt` was **not**
> self-signed — it is a real **Sectigo production wildcard** for `*.panaudia.com`
> (valid to Aug 2026) with its private key in `keys/server.key`. The earlier
> "ship the certs in Resources" plan would have **bundled that production private
> key into a downloadable `.app`** — extractable by anyone. This is why we pivoted
> to auto-generation: the production key is never bundled, and each install owns a
> unique self-signed cert. (`keys/` is gitignored; the private key should arguably
> not live in the working tree at all.)

### C. Public distribution → signing + notarization

For arbitrary Macs, Gatekeeper requires:

1. **Developer ID Application** certificate (needs a paid Apple Developer account).
2. **Hardened runtime** signing of *every* Mach-O — the binary and all 8 dylibs —
   signed inside-out (dylibs first, binary last).
3. **Notarize** via `notarytool`, then **staple**. The ticket staples directly
   onto the `.app`. For download distribution the stapled `.app` ships inside a
   `.dmg` (also stapled) so the ticket survives transit and first launch needs no
   network.

> **Open question — entitlements.** Hardened runtime may require
> `com.apple.security.cs.disable-library-validation` (we load dylibs not signed
> by Apple). Confirm during the notarization test pass.

## Proposed deliverables

```
Panaudia.app/
└── Contents/
    ├── Info.plist              CFBundleExecutable=panaudia, LSUIElement
    ├── MacOS/
    │   ├── panaudia            launcher script (entry point) — opens Terminal
    │   └── panaudia-server     the headless Go server binary (pristine)
    ├── Frameworks/             the 8 rewritten + signed dylibs
    └── Resources/              empty — no config/keys shipped (see §B)
```

Config/data lives outside the bundle in `~/Library/Application Support/Panaudia/`
(`keys/` auto-generated on first launch, optional `.env`) — see [§B](#b-asset-path-resolution--env-var-config-phase-2).

**Double-click UX (decided).** A `.app` launched by Finder has no controlling
terminal, so a headless server appears to "do nothing". Rather than teach the
server binary about Terminal (it must stay headless for launchd/Docker/CLI), the
bundle's entry point is a tiny **shell-script launcher** (`MacOS/panaudia`) that
runs `open -a Terminal …/panaudia-server`, showing the banner/logs in a window.
The server binary (`panaudia-server`) is unchanged and is the entry point for all
headless use. `LSUIElement` keeps the launcher Dock-icon-free; it exits right
after spawning Terminal. (Considered: a compiled launcher or AppleScript app —
both notarize a touch more cleanly; the script was chosen for simplicity, with
the compiled-stub fallback noted for Phase 4 if notarization balks at a script
main executable.)

- `scripts/bundle_macos` — orchestrator: `go build -tags=accelerate`, assemble the
  `.app` layout, recursive dylib copy + install-name rewrite, write the launcher +
  `Info.plist`. Ships no keys/config (server auto-generates on first launch).
- `Info.plist` — minimal (`CFBundleExecutable`, identifier, version from `version`).
- `scripts/sign_and_notarize_macos` — codesign inside-out (Frameworks dylibs first,
  then the `.app`) with hardened runtime, submit to notarytool, staple the `.app`,
  build + staple the `.dmg`.
- `docs/build-macos-bundle.md` — user-facing build/release doc.

> **Asset paths & config.** Resolved in Phase 2 (standalone `main.go` only) via a
> bundle-only config root (`~/Library/Application Support/Panaudia/`) and a
> universal self-signed cert auto-generated on first launch. Nothing config is
> bundled; production path-loading is preserved. See [§B](#b-asset-path-resolution--env-var-config-phase-2).

## Phases & rough effort

| Phase | Work | Effort |
|---|---|---|
| 1. Relocation core ✅ | `bundle_macos`: assemble `.app` + recursive dylib copy/rewrite; verified by both [self-containment checks](#verifying-self-containment) | done |
| 2. Asset paths ✅ | `main.go` config-root (bundle-only) + universal self-signed cert auto-gen; ticket key optional when unticketed; nothing bundled | done |
| 3. Clean-machine test | run on a Mac that has never had Homebrew or the libs and re-run [runtime check 2](#verifying-self-containment) — the only proof on a truly bare host | ~0.5 day + clean VM/Mac |
| 4. Sign + notarize ✅ | `sign_and_notarize_macos`: Developer ID inside-out, hardened runtime, notarytool, staple `.app` + `.dmg`; both Accepted, `spctl` accepted | done |

## Verifying self-containment

Two complementary checks. The first proves *intent* (static), the second proves
*reality* (which file dyld actually loads) — and the second is the one that
matters, because `/usr/local/lib` and the Homebrew copies still exist on the
build box, so the only way to know the bundle isn't silently using them is to
ask the loader.

**1. Static audit (built into `bundle_macos`).** No `@rpath`-resolved dependency
should still point at an external prefix. The build fails if any remain:

```sh
for m in dist/Panaudia.app/Contents/MacOS/panaudia-server dist/Panaudia.app/Contents/Frameworks/*.dylib; do
  otool -L "$m" | tail -n +2 | awk '{print $1}' | grep -E '^/usr/local/|^/opt/' \
    && echo "LEAK: $m"
done   # expect no output
```

**2. Authoritative runtime check — ask dyld where it loaded each lib from.**
Run the (ad-hoc-signed) bundle and let the loader print resolved absolute paths:

```sh
DYLD_PRINT_LIBRARIES=1 DYLD_PRINT_RPATHS=1 \
  dist/Panaudia.app/Contents/MacOS/panaudia-server 2>&1 \
  | grep -iE 'libroc|libuv|libssl|libcrypto|libspeexdsp|libopus|libopusfile|libogg'
```

Every one of the 8 must resolve to `…/Panaudia.app/Contents/Frameworks/`, and
none to `/usr/local/` or `/opt/homebrew/`. **Expect a red herring:**
`/usr/lib/libssl.48.dylib` and `/usr/lib/libcrypto.46.dylib` also appear — those
are Apple's *own* system TLS libs (note the `.48`/`.46` majors vs our `.3`),
pulled in by a system framework, and live in SIP-protected `/usr/lib`. They are
**not** our openssl@3 and must stay system-provided.

> **Hardened-runtime caveat (Phase 4).** macOS ignores `DYLD_PRINT_*` for binaries
> signed with the hardened runtime, so run check 2 **before** the Phase 4 signing
> (i.e. on the ad-hoc bundle `bundle_macos` produces). After hardened-runtime
> signing, verify load paths with `sudo dyld_usage` or on the clean machine.

Status: checks 1 and 2 both **passed** on the dev box (2026-06-17) — all 8 libs
loaded from `Frameworks/`. Phase 3 re-runs check 2 on a clean machine.

## Risks / open questions

- **Apple Developer account + Developer ID cert** — ✅ account confirmed available;
  still need to generate/install the Developer ID Application cert before phase 4.
- **Entitlements** for hardened runtime with non-Apple dylibs (see problem C).
- **Clean-room test machine** is non-negotiable; the dev box masks missing-dylib
  bugs via its existing `/usr/local/lib` and Homebrew prefixes.
- **TLS cert** — ✅ decided: **not bundled**; auto-generated per-install on first
  launch (`ensureTLSCert`), into the config dir. Production overrides via
  `PANAUDIA_TLS_*` (untouched). Reversed the earlier "ship the certs" plan after
  finding the repo cert is a real production wildcard key (see [§B](#b-asset-path-resolution--env-var-config-phase-2)).
- `libroc` as built is arm64-only — consistent with the arm64-only decision, so no
  rebuild needed.

## Status

Legend: ☐ not started · ◐ in progress · ☑ done

- ☑ Phase 1 — relocation core (`scripts/bundle_macos`)
  - ☑ build binary with `-tags=accelerate`
  - ☑ recursive `otool -L` dylib closure walk (BFS, bash-3.2 compatible)
  - ☑ assemble `.app` skeleton + `Info.plist` (version from `version`, `LSUIElement`)
  - ☑ shell-script launcher (`MacOS/panaudia`) opens Terminal → `panaudia-server`; server binary stays headless/pristine
  - ☑ copy 8 dylibs into `Contents/Frameworks/`, rewrite IDs + inter-lib refs to `@rpath`
  - ☑ rewrite binary refs + rpath to `@executable_path/../Frameworks` (drop `/usr/local/lib`)
  - ☑ ships no keys/config (`Resources/` empty — server self-provisions on first launch)
  - ☑ ad-hoc sign inside-out (so the bundle runs locally; Phase 4 re-signs)
  - ☑ self-containment audit (fails build if any `/usr/local` or `/opt` ref remains)
  - ☑ verified: 8 dylibs bundled; link tables all `@rpath`+system; runs from clean
    CWD/HOME with no dyld errors
- ☑ Phase 2 — asset paths & config (standalone `main.go` only; core/cloud untouched)
  - ☑ `configRoot` / `runningInBundle` — Application Support in a bundle, CWD elsewhere
  - ☑ rebase keys/ticket paths into config dir (`resolveKeyPaths`); explicit env overrides honoured
  - ☑ `loadDotEnv` → `<configRoot>/.env` in bundle, else `./.env`
  - ☑ universal self-signed cert auto-gen (`ensureTLSCert`), gated by `PANAUDIA_TLS_AUTOGEN`
  - ☑ ticket key optional when unticketed (no panic on missing key)
  - ☑ verified: CWD run generates `./keys` (SAN incl. ICE host); idempotent; `AUTOGEN=0` gate; bundle run generates into Application Support; production path-loading untouched
- ☐ Phase 3 — clean-machine test on a fresh Mac/VM (re-run runtime check 2 from
  [Verifying self-containment](#verifying-self-containment))
- ☑ Phase 4 — sign + notarize + staple (`scripts/sign_and_notarize_macos`, 2026-06-17)
  - ☑ Developer ID Application cert present: `Glowinthedark Ltd (63PT2H4G8K)`
  - ☑ codesign inside-out (8 dylibs → server Mach-O → `.app`; hardened runtime `flags=0x10000`)
  - ☑ entitlements resolved: **none needed** — all dylibs signed with the same Team ID
    satisfy library validation, so `disable-library-validation` stays off
    (`scripts/macos.entitlements` ships it commented-out as a fallback)
  - ☑ verified signed binary launches under hardened runtime: loads all 8 dylibs,
    auto-gens cert, listens on UDP+TCP — no library-validation crash
  - ☑ notarytool submit (Accepted) + staple `.app`; `spctl` → `accepted / Notarized Developer ID`
  - ☑ build + sign + notarize (Accepted) + staple `.dmg` → `dist/Panaudia-<version>.dmg`
  - reused existing keychain profile `notarytool-password` (same as Panaudia Link releases)

## References

- Build-from-source guide: `docs/install-macos.md`
- Link flags / rpath: `spacer/flags.go`
- Asset path defaults: `main.go:48-50`, dotenv loader `main.go:67-90`

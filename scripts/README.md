# scripts/

Build and packaging helpers for the standalone Panaudia server. All are stock
`/bin/bash` and meant to be run from anywhere (they resolve the repo root
themselves).

## macOS distributable (`.app` + `.dmg`)

Two steps, run in order. The split is deliberate: you can iterate on the bundle,
or do a signature-only pass, without spending an Apple notarization round trip.

### 1. `bundle_macos`
Builds the relocatable, self-contained `dist/Panaudia.app`:
- `go build -tags=accelerate` the server binary,
- copies every non-system dynamic library it links into `Contents/Frameworks/`
  and rewrites their install names to `@rpath`,
- generates the app icon from `assets/icon.png` (or uses `assets/icon.icns`),
- bundles `LICENSE` + `third-party-licences.md` into `Contents/Resources/`,
- writes the Terminal launcher + `Info.plist`,
- **ad-hoc** signs everything so it runs locally, and audits self-containment.

No config/keys are shipped — the server self-provisions (TLS cert, optional
`.env`) into `~/Library/Application Support/Panaudia/` on first launch.

```sh
scripts/bundle_macos
```

### 2. `sign_and_notarize_macos`
Takes the bundle from step 1 and makes it publicly distributable:
- Developer ID codesign inside-out (dylibs → server → seal the `.app`) with the
  hardened runtime and `macos.entitlements`,
- notarize the `.app` via `notarytool`, then staple,
- build, sign, notarize, and staple `dist/Panaudia-<version>.dmg`.

Run signature-only (stops before notarization) by omitting the profile:

```sh
scripts/sign_and_notarize_macos                                  # sign + verify only
scripts/sign_and_notarize_macos --notary-profile notarytool-password   # full release
```

Credentials are a keychain profile stored once with
`xcrun notarytool store-credentials <name> --apple-id … --team-id 63PT2H4G8K --password …`.
The signing identity auto-detects the first "Developer ID Application" in the
keychain; override with `PANAUDIA_SIGN_IDENTITY`.

### `macos.entitlements`
Hardened-runtime entitlements for the server binary. Currently empty —
library validation is satisfied because every bundled dylib is signed with the
same Team ID as the binary. Ships `disable-library-validation` commented-out as a
documented fallback.

Full design notes: [`../plan/macos-bundle/plan.md`](../plan/macos-bundle/plan.md).

## Native build helpers

### `build_swig_wrapper`
Regenerates the SWIG cgo wrapper in `spacer/` from `spacer.i`. Run after
changing the C interface.

### `build_libs_mac`
Builds the static SAF / panaudia-utils libraries into `/usr/local/lib`.
**⚠ May be out of date** (e.g. it targets `x86_64`) — verify before relying on
it; see the build-from-source guide in `docs/`.

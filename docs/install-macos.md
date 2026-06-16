# Building & running Panaudia locally on macOS

This guide covers building the standalone Panaudia server from source on macOS
(Apple Silicon and Intel). It produces the all-in-one server binary described in
the [README](../README.md): MOQ (UDP) + WebRTC (TCP) on one port, with Panaudia
Link (ROC) available as a runtime opt-in.

> **Why so many steps?** Panaudia links several native (C) libraries through
> cgo: the [Spatial Audio Framework](https://github.com/leomccormack/Spatial_Audio_Framework)
> (SAF) and `panaudia-utils` for ambisonic/binaural DSP, and
> [Roc Toolkit](https://roc-streaming.org) (plus its `openfec` dependency) for
> object-audio streaming. These are not Go modules — you build them once and
> install their static/shared libraries into `/usr/local/lib`, then Go links
> against them.
>
> **Roc is required to *build*, even if you never use Link.** The `roc-go`
> binding is imported unconditionally, so the binary always links `libroc`. The
> `PANAUDIA_ENABLE_LINK_IN/OUT` flags only gate ROC at *runtime*.

---

## 1. Prerequisites

### Xcode command-line tools (C/clang toolchain)

```sh
xcode-select --install
```

### Homebrew packages

```sh
brew install go cmake swig scons pkg-config \
  libuv speexdsp openssl@3 ragel gengetopt \
  opus opusfile
```

| Tool / lib | Used for |
|---|---|
| `go` (≥ 1.26) | builds the server (`go.mod` requires 1.26.0) |
| `cmake` | builds SAF, `panaudia-utils`, `openfec` |
| `swig` | regenerates the `spacer` cgo wrapper (see step 4) |
| `scons` | builds `roc-toolkit` |
| `libuv`, `speexdsp`, `openssl@3` | runtime/link deps of `roc-toolkit` |
| `ragel`, `gengetopt` | build-time codegen for `roc-toolkit` |
| `opus`, `opusfile` | Opus codec libs linked by the `hraban/opus.v2` Go binding via `pkg-config` |

Confirm Go is on your `PATH`:

```sh
go version   # go version go1.26.x darwin/arm64
```

---

## 2. Lay out the sibling repositories

The build expects the four native dependency repos to sit **next to** the
`panaudia` repo (the SWIG interface and the helper scripts reference them with
`../../` paths). Clone them into the *parent* directory of this repo:

```sh
# cd into the directory that CONTAINS your panaudia checkout
cd /Users/paul/Dropbox/glowinthedark/panaudia/code/panaudia

git clone --branch panaudia --depth 1 https://github.com/panaudia/Spatial_Audio_Framework.git
git clone --depth 1                    https://github.com/panaudia/panaudia-utils.git
git clone --depth 1                    https://github.com/paulharter/openfec.git
git clone --branch without-staircase --depth 1 https://github.com/paulharter/roc-toolkit.git
```

The resulting layout:

```
code/panaudia/
├── panaudia/                 ← this repo
├── Spatial_Audio_Framework/  ← branch: panaudia
├── panaudia-utils/
├── openfec/
└── roc-toolkit/              ← branch: without-staircase
```

---

## 3. Build the native libraries

All static/shared libraries land in `/usr/local/lib`, which is on the default
link path baked into `spacer/flags.go` (`-L /usr/local/lib`).

### 3a. Spatial Audio Framework + panaudia-utils (Apple Accelerate)

On macOS, link against Apple's **Accelerate** framework for BLAS/LAPACK — this is
what the `accelerate` build tag (used in step 5) expects, and it needs no extra
math library. Build both repos with the `SAF_USE_APPLE_ACCELERATE` performance
backend and your machine's native architecture:

```sh
cd ../Spatial_Audio_Framework
cmake -S . -B build \
  -DSAF_PERFORMANCE_LIB=SAF_USE_APPLE_ACCELERATE \
  -DSAF_ENABLE_SIMD=0 \
  -DSAF_BUILD_TESTS=0 \
  -Dsaf_example_list=ambi_bin
cmake --build build
sudo cp build/framework/libsaf.a                       /usr/local/lib/libsaf.a
sudo cp build/examples/libsaf_example_ambi_bin.a       /usr/local/lib/libsaf_example_ambi_bin.a

cd ../panaudia-utils
cmake -S . -B build \
  -DSAF_PERFORMANCE_LIB=SAF_USE_APPLE_ACCELERATE \
  -DSAF_ENABLE_SIMD=0 \
  -DSAF_BUILD_TESTS=0 -DSAF_BUILD_EXTRAS=0 -DSAF_BUILD_EXAMPLES=0
cmake --build build
sudo cp build/libpanaudia_utils.a                      /usr/local/lib/libpanaudia_utils.a
```

> **Intel / OpenBLAS alternative.** The repo also ships
> `scripts/build_libs_mac`, which builds SAF and `panaudia-utils` against
> **OpenBLAS** for **x86_64**. That path requires `brew install openblas lapack`
> and pairs with `-tags 'openblas lapacke'` instead of `-tags=accelerate` in
> step 5. Use it only if you specifically need the OpenBLAS backend; the
> Accelerate path above is recommended for Apple Silicon.

### 3b. openfec (Roc dependency)

```sh
cd ../openfec
cmake -S . -B build -DDEBUG:STRING=OFF -DOF_USE_LDPC_STAIRCASE_CODEC=OFF
cmake --build build
sudo cmake --install build
```

### 3c. roc-toolkit

Roc builds with SCons. Point it at the openfec sources/libs you just built. (If
SCons can't find OpenSSL/libuv, add Homebrew's prefix to `PKG_CONFIG_PATH`, e.g.
`export PKG_CONFIG_PATH="$(brew --prefix openssl@3)/lib/pkgconfig"`.)

```sh
cd ../roc-toolkit
scons -Q \
  --with-openfec-includes=../openfec/src \
  --with-includes=../openfec/src/lib_common \
  --with-libraries=../openfec/bin/Release \
  --disable-sox --disable-pulseaudio --disable-sndfile \
  PKG_CONFIG=pkg-config

sudo scons -Q \
  --with-openfec-includes=../openfec/src \
  --with-includes=../openfec/src/lib_common \
  --with-libraries=../openfec/bin/Release \
  --disable-sox --disable-pulseaudio --disable-sndfile \
  PKG_CONFIG=pkg-config install
```

This installs `libroc` into `/usr/local/lib`. See the
[Roc macOS build docs](https://roc-streaming.org/toolkit/docs/building/user_cookbook.html)
if your environment needs different flags.

---

## 4. Regenerate the SWIG wrapper

`spacer/spacer.go` and `spacer/spacer_wrap.c` are generated from `spacer/spacer.i`
and are **git-ignored**, so you must generate them before the first build. The
repo includes a helper:

```sh
cd ../panaudia
./scripts/build_swig_wrapper
```

(Equivalent to `cd spacer && swig -go -cgo -intgosize 64 spacer.i`.) The
interface includes headers from `../../Spatial_Audio_Framework` and
`../../panaudia-utils`, which is why those repos must be laid out as in step 2.

---

## 5. Build & run the server

From the repo root, build/run with the `accelerate` tag so the cgo math backend
links the Accelerate framework:

```sh
# run directly
go run -tags=accelerate .

# …or build a binary
go build -tags=accelerate -o panaudia .
./panaudia
```

The server defaults to TLS certs and the JWT public key under `keys/`
(`keys/server.crt`, `keys/server.key`, `keys/panaudia_key.pub`), so running from
the repo root works out of the box. It listens on `PANAUDIA_PORT` (default
`4443`) for MOQ (UDP) + HTTPS/WebRTC (TCP).

See **[environment-variables.md](environment-variables.md)** for the full config
reference. Panaudia Link (ROC) stays off unless you set
`PANAUDIA_ENABLE_LINK_IN=1` / `PANAUDIA_ENABLE_LINK_OUT=1`.

### Quick sanity check

```sh
go test -tags=accelerate -v -run TestE2E ./core/moq/ -timeout 60s
```

---

## 6. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `ld: library 'saf' not found` (or `panaudia_utils`, `saf_example_ambi_bin`) | SAF / utils libs not in `/usr/local/lib`. Re-run step 3a; check `ls /usr/local/lib/libsaf*.a`. |
| `ld: library 'roc' not found` / `libroc` missing | roc-toolkit not installed. Re-run step 3c; check `ls /usr/local/lib/libroc*`. |
| `spacer/spacer.go: no such file` or undefined `spacer` symbols | SWIG wrapper not generated — run step 4. |
| `fatal error: 'ambi_bin.h' file not found` during SWIG/cgo | Sibling repos missing or misplaced — verify the layout in step 2. |
| Architecture mismatch / `building for macOS-arm64 but … x86_64` | You built the OpenBLAS/x86_64 libs (`scripts/build_libs_mac`) but are linking with `-tags=accelerate` on Apple Silicon. Rebuild the libs per step 3a (Accelerate, native arch). |
| `swig: command not found` / `scons: command not found` | Install via Homebrew (step 1). |

> **Tip:** if you'd rather not build the native stack at all, the repo ships
> Docker images and build scripts under [`../docker/`](../docker) (arm64 and
> amd64). See the [README](../README.md#docker-build).

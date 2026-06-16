# Panaudia — Environment Variables

All runtime configuration for the Panaudia server is supplied through environment
variables. Most are read at startup into the config struct in `main.go`; a few
(state-cache tuning and `QLOGDIR`) are read directly where they are used.

**Conventions**

- **Boolean flags are integers** — `1` = on, `0` = off (e.g. `PANAUDIA_UNTICKETED`).
- **Defaults** are shown below; an unset variable uses its default.
- The values in force are printed in the **Config** banner when the server starts.

**Loading from a `.env` file (optional)**

On startup, before reading the variables below, the server loads a `.env` file
from the working directory **if one exists** — a missing file is not an error.
Copy [`.env.example`](../.env.example) to `.env` and uncomment what you need.
Override the path with `PANAUDIA_ENV_FILE` (which must itself be a real
environment variable). Precedence is **real environment > `.env` file >
default**: the loader never overwrites a variable that is already set, so a `.env`
only fills in what the environment hasn't. Keep secrets out of version control —
`.env` is git-ignored; `.env.example` is not.

---

## Network & transport

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `PANAUDIA_HOST` | string | `0.0.0.0` | Address/interface the listeners bind to. `0.0.0.0` = all interfaces. |
| `PANAUDIA_PORT` | int | `4443` | Single shared port: MOQ over **UDP** (QUIC / WebTransport) **and** HTTPS + WebRTC signalling over **TCP**. |
| `PANAUDIA_ICE_HOST` | string | _(empty)_ | Public/NAT IP to advertise in WebRTC ICE host candidates (`SetNAT1To1IPs`). Empty = advertise the local interface IPs. Set this when the server is behind NAT. |
| `PANAUDIA_ICE_PORT` | int | `0` | Port to advertise in WebRTC ICE candidates. `0` = no rewrite (use the real listening port). Set to the public port (e.g. `443`) only when a proxy/load-balancer terminates that port and forwards to `PANAUDIA_PORT`. |

## TLS & authentication

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `PANAUDIA_TLS_CTR_PATH` | string | `keys/server.crt` | Path to the TLS **certificate** (PEM), used for HTTPS and WebRTC. *(Note the env name is spelled `CTR`.)* |
| `PANAUDIA_TLS_KEY_PATH` | string | `keys/server.key` | Path to the TLS **private key** (PEM). |
| `PANAUDIA_TICKET_KEY_PATH` | string | `keys/panaudia_key.pub` | Path to the **Ed25519 public key** used to verify JWT connection tickets. |
| `PANAUDIA_UNTICKETED` | int (0/1) | `1` | `1` = accept connections **without** a JWT ticket; `0` = require a valid ticket. |

## Spatial & rendering

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `PANAUDIA_SPACE_SIZE` | int | `40` | Side length of the cubic world used for distance/attenuation scaling (world units). Node positions are normalised `0..1` across this extent. |
| `PANAUDIA_SPACE_ORDER` | int | `3` | Ambisonic order. Normally `2`–`5`; **must be `2` or `3` when `PANAUDIA_ENABLE_LINK_OUT=1`** (ROC ambisonic output supports only order 2/3). Invalid values cause the server to exit at startup. |
| `PANAUDIA_SPACE_MAX_SOURCES` | int | `128` | Maximum simultaneous sources/outputs. Sizes the pre-built binaural decoder pool (one decoder per possible output) and the MOQ client cap, and caps the test fixtures. Raising it increases startup cost and memory. |
| `PANAUDIA_REVERB_PRESET` | int | `0` | Selects a reverb preset. `0` = default/none. |

## Panaudia Link (ROC)

ROC is **opt-in** and off by default. Its signalling runs on a **separate**
plain-HTTP port; ROC media flows over its own negotiated RTP/UDP ports (not the
shared `PANAUDIA_PORT`).

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `PANAUDIA_ENABLE_LINK_IN` | int (0/1) | `0` | `1` = enable Panaudia Link (ROC) audio **input**. |
| `PANAUDIA_ENABLE_LINK_OUT` | int (0/1) | `0` | `1` = enable Panaudia Link (ROC) audio **output**. Requires `PANAUDIA_SPACE_ORDER` to be `2` or `3`. |
| `PANAUDIA_LINK_PORT` | int | `80` | Dedicated plain-HTTP (`ws://`) port for ROC signalling, separate from `PANAUDIA_PORT`. Only used when a link is enabled. **Binding port 80 needs root / `CAP_NET_BIND_SERVICE`.** |

## Runtime & performance

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `PANAUDIA_GOMAXPROCS` | int | `4` | Caps the number of OS threads Go runs simultaneously (`runtime.GOMAXPROCS`). |

## State-cache tuning

The state cache answers "what state should a newly connected client receive?".
These rarely need changing. Each is applied only when set to a value `> 0`.

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `PANAUDIA_CACHE_RING_SIZE` | int | `16` | Number of segments in the state-cache ring. |
| `PANAUDIA_CACHE_SEGMENT_CAPACITY` | int | `128` | Number of op slots per cache segment. |
| `PANAUDIA_CACHE_TOMBSTONE_TTL_SEC` | int (seconds) | `30` | How long tombstones (deletions) are retained in snapshots. |

## Logging & diagnostics

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `PANAUDIA_LOG_LEVEL` | int | `2` | Verbosity threshold: `0` VERBOSE, `1` DEBUG, `2` INFO, `3` WARN, `4` ERROR, `5` CRITICAL. **Lower = more output.** A message is emitted when `LOG_LEVEL ≤ its level`, so `2` shows INFO and above. |
| `PANAUDIA_LOG_MS` | int (0/1) | `0` | `1` = print the total render time per second (ms), plus a warning if a second's rendering exceeds 1000 ms. |
| `QLOGDIR` | string | _(unset)_ | If set, write QUIC `qlog` traces to this directory (deep transport diagnostics). Unset = disabled. |

## Test & load-generation fixtures

These inject synthetic nodes at startup to exercise/benchmark the engine; the audio
output is discarded. Useful for performance testing.

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `PANAUDIA_TEST_TONE` | int (0/1) | `0` | `1` = add a single fixed test-tone source. |
| `PANAUDIA_TEST_PEOPLE` | int (count) | `0` | Add N synthetic "people" — each a tone source **plus** a binaural decode-and-discard output, so they exercise **both** ambisonic mixing and binaural rendering. |
| `PANAUDIA_TEST_VOICES` | int (count) | `0` | Add N synthetic "voices" — tone sources only, exercising ambisonic **mixing**. |
| `PANAUDIA_STEREO_TEST` | int (0/1) | `0` | `1` = replace the binaural output with 440 Hz (left) / 880 Hz (right) test tones, for diagnosing channel routing. |

> `PANAUDIA_TEST_PEOPLE + PANAUDIA_TEST_VOICES` is clamped to
> `PANAUDIA_SPACE_MAX_SOURCES` (people are kept in preference to voices); exceeding
> it logs a warning rather than overrunning the mixer/decoder pool.

---

## Related (dependency-controlled)

Not read by Panaudia itself, but affects behaviour via a dependency:

| Variable | Description |
|----------|-------------|
| `MOQ_LOG_LEVEL` | Controls the `Eyevinn/moqtransport` library's logger (`debug` / `info` / `warn` / `error`). Default is silent. |

## Build tags (not env vars)

For completeness — these are **compile-time** `-tags`, not environment variables:
`accelerate` (link the SAF C code via Apple Accelerate on macOS), `openblas` /
`lapacke` (BLAS/LAPACK backends, used in the Linux/Docker builds), `ipp`.

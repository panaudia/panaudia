# Panaudia
### A network spatial audio engine

[panaudia.com](https://panaudia.com/)

Panaudia is a network spatial audio engine: a server for connecting people across the network in shared, virtual audio spaces. 
To achieve this Panaudia combines a WebRTC and Media Over QUIC audio server with a powerful ambisonic mixer and a binaural rendering engine.

There are TypeScript and Unreal Engine clients, and a test page for manual testing in [panaudia-client](https://github.com/panaudia/panaudia-client)

It also uses [Roc Toolkit](https://roc-streaming.org) to stream RTP uncompressed object-based audio in, and multichannel ambisonic audio out.



## Install & Build

- **[Install & build on macOS](docs/install-macos.md)** — set up the native
  dependencies and build the server from source.
- **[Linux Docker builds](/docker/readme.md)**  - build scripts for arm64 and amd64 linux docker images

## Running the server

There is a single entry point at the repo root (`main.go`): a unified server
that serves **MOQ** (UDP) and **WebRTC** (TCP) over one port, plus **Panaudia
Link (ROC)** when enabled.

> **First time on macOS?** Follow **[docs/install-macos.md](docs/install-macos.md)**
> first — it installs the native (cgo) dependencies the build needs. Once those
> are in place, build and run from source:

```
cd /Users/paul/Dropbox/glowinthedark/panaudia/code/panaudia/panaudia
go run -tags=accelerate .

# or build a binary:
go build -tags=accelerate -o panaudia .
./panaudia
```

The TLS certs and JWT public key default to `keys/server.crt`, `keys/server.key`,
and `keys/panaudia_key.pub`, so running from the repo root works out of the box.
Override with `PANAUDIA_TLS_CTR_PATH` / `PANAUDIA_TLS_KEY_PATH` /
`PANAUDIA_TICKET_KEY_PATH` if needed.

### Config (env vars)

See **[docs/environment-variables.md](docs/environment-variables.md)** for the full
reference — every variable, its type, default, and description. The most commonly
used:

- `PANAUDIA_PORT` — single port for MOQ (UDP) + HTTPS/WebRTC (TCP), default `4443`
- `PANAUDIA_ICE_HOST` — public/NAT IP to advertise in WebRTC ICE host candidates (`SetNAT1To1IPs`), default empty (advertise local interface IPs). Set when the server is behind NAT.
- `PANAUDIA_ICE_PORT` — port to advertise in WebRTC ICE host candidates, default `0` (no rewrite — use the real listening port). Set to the public port (e.g. `443`) only when a proxy/LB terminates that port and forwards to `PANAUDIA_PORT`.
- `PANAUDIA_UNTICKETED` — `1` accepts connections without a JWT (default `1`)
- `PANAUDIA_SPACE_ORDER` — ambisonic order `2..5` (default `3`; must be `2` or `3` when Link out is on)
- `PANAUDIA_SPACE_MAX_SOURCES` — cap on simultaneous sources/outputs, default `128`. Sizes the binaural decoder pool (one decoder per possible output) and the MOQ client cap, so raising it raises startup cost and memory.
- `PANAUDIA_ENABLE_LINK_IN` / `PANAUDIA_ENABLE_LINK_OUT` — opt-in Panaudia Link (ROC) signalling, default `0` (off)
- `PANAUDIA_LINK_PORT` — dedicated plain-HTTP (`ws://`) port for Panaudia Link (ROC) signalling, default `80` (binding 80 needs root / `CAP_NET_BIND_SERVICE`). Separate from `PANAUDIA_PORT`; ROC media still flows on its own negotiated RTP/UDP ports.




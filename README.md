# mixer
Spatial audio mixer

## Dependencies / module

```
go mod edit -go=1.23.4
go get -u
go mod tidy
```

## Docker build

```
docker buildx build --platform linux/amd64 --no-cache --provenance=false \
  --ssh github2=../keys/id_ed25519_mixer_deploy --ssh github1=../keys/id_ed25519_utils_deploy \
  -t paulharter/panaudia-mixer:latest .
```

## Running the server

There is a single entry point at the repo root (`main.go`): a unified server
that serves **MOQ** (UDP) and **WebRTC** (TCP) over one port, plus **Panaudia
Link (ROC)** when enabled. Build and run from source:

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

- `PANAUDIA_PORT` — single port for MOQ (UDP) + HTTPS/WebRTC (TCP), default `4443`
- `PANAUDIA_ICE_HOST` — public/NAT IP to advertise in WebRTC ICE host candidates (`SetNAT1To1IPs`), default empty (advertise local interface IPs). Set when the server is behind NAT.
- `PANAUDIA_ICE_PORT` — port to advertise in WebRTC ICE host candidates, default `0` (no rewrite — use the real listening port). Set to the public port (e.g. `443`) only when a proxy/LB terminates that port and forwards to `PANAUDIA_PORT`.
- `PANAUDIA_UNTICKETED` — `1` accepts connections without a JWT (default `1`)
- `PANAUDIA_SPACE_ORDER` — ambisonic order `2..5` (default `3`; must be `2` or `3` when Link out is on)
- `PANAUDIA_SPACE_MAX_SOURCES` — cap on simultaneous sources/outputs, default `256`. Sizes the binaural decoder pool (one decoder per possible output) and the MOQ client cap, so raising it raises startup cost and memory.
- `PANAUDIA_ENABLE_LINK_IN` / `PANAUDIA_ENABLE_LINK_OUT` — opt-in Panaudia Link (ROC) signalling, default `0` (off)
- `PANAUDIA_LINK_PORT` — dedicated plain-HTTP (`ws://`) port for Panaudia Link (ROC) signalling, default `80` (binding 80 needs root / `CAP_NET_BIND_SERVICE`). Separate from `PANAUDIA_PORT`; ROC media still flows on its own negotiated RTP/UDP ports.

The test page (`../panaudia-client/examples/moq-test-page/`) defaults to
`https://localhost:4443`, matching `PANAUDIA_PORT`.

## Verification

### Go MOQ tests

```
cd /Users/paul/Dropbox/glowinthedark/panaudia/code/panaudia/panaudia
go test -v -run TestE2E ./core/moq/ -timeout 60s   # just the e2e tests
go test -v ./core/moq/ -timeout 60s                # all moq tests
```

### TypeScript client + test page

```
# Build moq-client
cd spatial-mixer/moq-client && npm run build

# Install and run test page (opens https://localhost:5173)
cd spatial-mixer/moq-test-page && npm install && npm run dev

# Build for production
npm run build
```

Test manually:
1. Open in Chrome 97+ (WebTransport required)
2. Paste JWT token, enter server URL, click Connect
3. Verify connection status shows "Authenticated"
4. Enable microphone — input meter should animate
5. Enable speaker — frames received counter should increment
6. Move position sliders — verify server receives updates (check server logs)

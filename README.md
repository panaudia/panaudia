# Panaudia
### A network spatial audio engine

[panaudia.com](https://panaudia.com/)

Panaudia is a network spatial audio engine: a server for connecting people across the network in shared, virtual audio spaces.

It's a live audio streaming media server combined with an ambisonic mixer and a binaural rendering engine. 
We also have client software for connecting to the server.

This is mostly a Go project but some parts are in C.

### Streaming protocols
- **WebRTC** provided using go [pion project](https://github.com/pion).
- **MoQ** using the [eyevinn fork of moqtransport](https://github.com/eyevinn/moqtransport) which is at draft-16.
- **RTP + RTCP + FEC** — using [Roc Toolkit](https://roc-streaming.org).

### Codecs
For compressed audio we use Opus at 48kbps per channel which seems to be a high enough bitrate to preserve spatial imaging.

We also stream uncompressed 32-bit pcm over RTP using Panaudia Link. 
You need decent bandwidth to do this but it gives broadcast quality i/o.

### Ambisonic Mixing

Panaudia is designed to support large numbers of sources and sinks in real-time, each source providing audio and each sink receiving back binaural mix of all the sources. 
Often all users will be both sources and sinks so the mixer is performing n x n spatialised mixes. 
We have tried to take a balanced approach to mixing that gives high enough quality spatialised rendering 
that can still support large numbers of simultaneous mixes in real-time.  Our cloud version can support ~500 users with 2nd order 
ambisonics, the stand alone one can support ~200 or fewer if you turn it up to 5th order.

| Ambisonics             |              |
|------------------------|--------------|
| Bit depth              | 32           |
| Sampling rate          | 48kHz        |
| Frame size             | 5ms          |
| Format                 | ACN N3D/SN3D |
| Order                  | 2nd - 5th    |

We recalculate ambisonic weights for every source/sink pair every frame and then smoothly blend 
between previous and current frame weights. We also use a simple parametric room reverb and do a wet/dry mix based on distance.

The underlying intensive maths operations for ambisonic mixing are many small matrix multiplications on vectors with new data every time. 
We have played with optimising this quite a bit, our current favourite for servers is OpenBLAS on AMD Milan chipsets, 
which is slightly faster than IPP on Intel,
but Apple's shared memory architecture and their Accelerate intrinsics are faster than either! 
Using NVIDIA's cuBLAS on GPUs is slower than both due to the price paid for moving memory around, 
but we expect this to improve with their better use of shared memory.

### Binaural Rendering

We use the excellent binaural renderer from the [Spatial Audio Framework](https://github.com/leomccormack/Spatial_Audio_Framework). 
We have found its Magnitude Least Squares decoder gives the great perceptual results. 
At the moment we are just using the frameworks included default HRTF which is taken from a KEMAR test head, 
we will be adding the ability to use your own custom SOFA files.

## Pre-built binaries

We provide prebuilt binaries in the form of a Mac silicon app and Docker images for Linux on arm or amd. 

They are built using the scripts in this repo and you can find them on the website's [software page](https://panaudia.com/software)

## Install and build from source

There are detailed instructions on how to [install and build on macOS](docs/install-macos.md) as this is how I work.
If you want to build on Linux have a look at the mac instructions and in the Dockerfiles for reference.

## Running the server

There is a single entry point at the repo root (`main.go`): a unified server
that serves **MOQ** (UDP) and **WebRTC** (TCP) over one port, plus **Panaudia
Link (ROC)** when enabled.


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




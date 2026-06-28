FROM panaudia/panaudia-base:latest AS builder

WORKDIR /opt/panaudia
RUN git clone --depth 1 https://github.com/panaudia/panaudia.git

## Build Panaudia
WORKDIR /opt/panaudia/panaudia
RUN cd spacer && swig -go -cgo -intgosize 64 spacer.i
RUN go mod download

RUN GOOS=linux go build -tags 'openblas lapacke' -o ./main ./main.go


FROM debian:trixie-slim

RUN apt-get update && apt-get install -y ca-certificates libopus0 libopusfile0 libuv1 libunwind8 libspeexdsp1 libzmq5 \
    libgfortran5 libcap2-bin

RUN apt-get -y upgrade perl

# Reentrant OpenBLAS (USE_LOCKING=1) built from source in the builder stage,
# replacing Debian's libopenblas0-serial. It carries its own bundled
# LAPACK+LAPACKE, so liblapack3/liblapacke are no longer installed; libgfortran5
# (above) satisfies the Fortran LAPACK runtime dependency. The binary's only BLAS
# NEEDED entry is libopenblas.so.0 (both -lopenblas and -llapacke resolve here).
COPY --from=builder /opt/openblas/lib/libopenblas.so.0 /usr/local/lib/libopenblas.so.0

COPY --from=builder /usr/lib/x86_64-linux-gnu/libroc.so /usr/lib/libroc.so
COPY --from=builder /usr/lib/x86_64-linux-gnu/libroc.so.0 /usr/lib/libroc.so.0
COPY --from=builder /usr/lib/x86_64-linux-gnu/libroc.so.0.4 /usr/lib/libroc.so.0.4

COPY --from=builder /usr/local/lib/libopenfec.so /usr/local/lib/libopenfec.so
COPY --from=builder /usr/local/lib/libopenfec.so.1 /usr/local/lib/libopenfec.so.1
COPY --from=builder /usr/local/lib/libopenfec.so.1.4.2 /usr/local/lib/libopenfec.so.1.4.2

# debian:trixie-slim ships useradd (in /usr/sbin) but not the higher-level adduser.
RUN useradd \
  --uid 65532 \
  --no-create-home \
  --home-dir "/nonexistent" \
  --shell "/sbin/nologin" \
  --comment "" \
  small-user

RUN mkdir -p /opt/panaudia/bin

COPY --from=builder /opt/panaudia/panaudia/main /opt/panaudia/bin/panaudia

# Third-party licence notices + the project licence travel with the binary —
# required when distributing the built image, which links MIT/BSD/ISC/Apache
# and MPL-2.0 (roc-toolkit/roc-vad) dependencies.
COPY --from=builder /opt/panaudia/panaudia/third-party-licences.md /licenses/THIRD-PARTY-LICENCES.md
COPY --from=builder /opt/panaudia/panaudia/LICENSE /licenses/LICENSE

ENV LD_LIBRARY_PATH=/usr/local/lib;/usr/lib
RUN setcap CAP_NET_BIND_SERVICE=+eip /opt/panaudia/bin/panaudia

RUN touch /etc/ld.so.conf.d/panaudia.conf
RUN echo "/usr/local/lib/ /usr/lib/" > /etc/ld.so.conf.d/panaudia.conf
RUN ldconfig

USER small-user:small-user

WORKDIR /opt/panaudia/bin
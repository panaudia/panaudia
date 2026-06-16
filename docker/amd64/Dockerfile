FROM golang:1.26.4-trixie AS builder

RUN apt-get update && apt-get install -y build-essential git wget gpg cmake scons swig\
    liblapack3 liblapack-dev libopenblas-serial-dev libopenblas0 libopenblas-dev \
    liblapacke-dev pkg-config libopus-dev libopusfile-dev \
    ragel gengetopt libuv1-dev libunwind-dev libspeexdsp-dev libssl-dev

RUN mkdir -m 700 /root/.ssh; \
  touch -m 600 /root/.ssh/known_hosts; \
  ssh-keyscan github.com > /root/.ssh/known_hosts

WORKDIR /opt/panaudia

RUN git clone --branch panaudia --depth 1 https://github.com/panaudia/Spatial_Audio_Framework.git
RUN git clone --depth 1 https://github.com/paulharter/openfec.git
RUN git clone --branch without-staircase --depth 1 https://github.com/paulharter/roc-toolkit.git
RUN git clone --depth 1 https://github.com/panaudia/panaudia-utils.git
RUN git clone --depth 1 https://github.com/panaudia/panaudia.git

WORKDIR /opt/panaudia/openfec

RUN mkdir build
WORKDIR /opt/panaudia/openfec/build
RUN cmake .. -DDEBUG:STRING=OFF -DOF_USE_LDPC_STAIRCASE_CODEC=OFF
RUN make
RUN make install

WORKDIR /opt/panaudia/roc-toolkit

RUN scons -Q --with-openfec-includes=../../openfec/src \
        --with-includes=/opt/panaudia/openfec/src/lib_common \
        --with-libraries=/opt/panaudia/openfec/bin/Release \
        --disable-sox \
        --disable-pulseaudio \
        --disable-sndfile \
        PKG_CONFIG=pkg-config

RUN scons -Q --with-openfec-includes=../../openfec/src \
        --with-includes=/opt/panaudia/openfec/src/lib_common \
        --with-libraries=/opt/panaudia/openfec/bin/Release \
        --disable-sox \
        --disable-pulseaudio \
        --disable-sndfile \
        PKG_CONFIG=pkg-config install

## Build Spatial Audio Framework
WORKDIR /opt/panaudia/Spatial_Audio_Framework
ARG TARGETPLATFORM
ENV TARGETPLATFORM=${TARGETPLATFORM}
RUN echo $TARGETPLATFORM

RUN if [ "$TARGETPLATFORM" = "linux/amd64" ] ; \
  then cmake -S . -B build -DSAF_PERFORMANCE_LIB=SAF_USE_OPEN_BLAS_AND_LAPACKE -DSAF_ENABLE_SIMD=1 -DSAF_BUILD_TESTS=0 -DSAF_BUILD_EXTRAS=0 ; \
  else cmake -S . -B build -DSAF_PERFORMANCE_LIB=SAF_USE_OPEN_BLAS_AND_LAPACKE -DSAF_ENABLE_SIMD=0 -DSAF_BUILD_TESTS=0 -DSAF_BUILD_EXTRAS=0 ; fi

RUN cd build && make
RUN ln -s /opt/panaudia/Spatial_Audio_Framework/build/examples/libsaf_example_ambi_bin.a /usr/local/lib/libsaf_example_ambi_bin.a
RUN ln -s /opt/panaudia/Spatial_Audio_Framework/build/framework/libsaf.a /usr/local/lib/libsaf.a

## Build Panaudia C Utils
WORKDIR /opt/panaudia/panaudia-utils

RUN cmake -S . -B build -DSAF_PERFORMANCE_LIB=SAF_USE_OPEN_BLAS_AND_LAPACKE -DSAF_ENABLE_SIMD=0 -DSAF_BUILD_TESTS=0 -DSAF_BUILD_EXTRAS=0  -DSAF_BUILD_EXAMPLES=0

RUN if [ "$TARGETPLATFORM" = "linux/amd64" ] ; \
  then cmake -S . -B build -DSAF_PERFORMANCE_LIB=SAF_USE_OPEN_BLAS_AND_LAPACKE -DSAF_ENABLE_SIMD=1 -DSAF_BUILD_TESTS=0 -DSAF_BUILD_EXTRAS=0  -DSAF_BUILD_EXAMPLES=0 ; \
  else cmake -S . -B build -DSAF_PERFORMANCE_LIB=SAF_USE_OPEN_BLAS_AND_LAPACKE -DSAF_ENABLE_SIMD=0 -DSAF_BUILD_TESTS=0 -DSAF_BUILD_EXTRAS=0  -DSAF_BUILD_EXAMPLES=0 ; fi

RUN cd build && make
RUN ln -s /opt/panaudia/panaudia-utils/build/libpanaudia_utils.a /usr/local/lib/libpanaudia_utils.a

## Build Panaudia
WORKDIR /opt/panaudia/panaudia
RUN cd spacer && swig -go -cgo -intgosize 64 spacer.i
RUN go mod download

RUN GOOS=linux go build -tags 'openblas lapacke' -o ./main ./main.go


FROM debian:trixie-slim

RUN apt-get update && apt-get install -y ca-certificates libopus0 libopusfile0 libuv1 libunwind8 libspeexdsp1 libzmq5 \
    liblapack3 libopenblas0-serial libopenblas0 liblapacke libcap2-bin

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
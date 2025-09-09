# Use the devel image which contains the full CUDA SDK and compilers.
ARG CUDA_VERSION="12.8.0"
ARG UBUNTU_VERSION="22.04"
FROM nvidia/cuda:${CUDA_VERSION}-devel-ubuntu${UBUNTU_VERSION}

# --- Labels ---
LABEL maintainer="Rich Insley <richinsley@gmail.com>"
LABEL description="Builds a static toolchain for FFmpeg (nvenc, x264, x265)."

# --- Environment Variables for the Build ---
ENV DEBIAN_FRONTEND=noninteractive
ENV PREFIX="/dist"
ENV PKG_CONFIG_PATH="${PREFIX}/lib/pkgconfig"

# --- Build Arguments ---
ARG FFMPEG_ARCANA_VERSION="7.1"
ARG NVENC_HEADERS_VERSION="sdk/12.2"
ARG BUILD_TYPE="general"

# Pass these to the build scripts via ENV so they are accessible
ENV FFMPEG_ARCANA_VERSION=${FFMPEG_ARCANA_VERSION}
ENV NVENC_HEADERS_VERSION=${NVENC_HEADERS_VERSION}
ENV BUILD_TYPE=${BUILD_TYPE}

# --- Install Build Tools ---
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    cmake \
    curl \
    git \
    nasm \
    yasm \
    pkg-config \
    zlib1g-dev \
    libasound2-dev \
    && rm -rf /var/lib/apt/lists/*

# --- Copy Build Scripts ---
COPY scripts/ /usr/local/bin/
RUN chmod +x /usr/local/bin/build-all.sh
RUN chmod +x /usr/local/bin/build-deps.sh
RUN chmod +x /usr/local/bin/build-ffmpeg-nvidia.sh
RUN chmod +x /usr/local/bin/build-ffmpeg-general.sh

# --- Entrypoint ---
ENTRYPOINT ["/usr/local/bin/build-all.sh"]
CMD ["general"]
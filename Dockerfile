# --- Base Image ---
# Choose a base image with NVIDIA CUDA drivers and toolkit.
# Example: nvidia/cuda:12.8.0-devel-ubuntu22.04
# Adjust CUDA_VERSION and UBUNTU_VERSION as needed for your requirements and ffmpeg-arcana compatibility.
ARG CUDA_VERSION="12.8.0"
ARG UBUNTU_VERSION="22.04"
FROM nvidia/cuda:${CUDA_VERSION}-devel-ubuntu${UBUNTU_VERSION}

# --- Labels ---
LABEL maintainer="Rich Insley <richinsley@gmail.com>"
LABEL description="Custom ffmpeg-arcana build with NVIDIA support and the goshadertoy Go application"

# --- Environment Variables ---
ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility,video
ENV DEBIAN_FRONTEND=noninteractive
ENV PREFIX="/usr/local"
ENV LD_LIBRARY_PATH="${PREFIX}/lib:/usr/local/cuda/lib64:${LD_LIBRARY_PATH}"
ENV ARCANA_CONF="/usr/local/bin/loader.toml"

# Add Go paths
ENV GOROOT="/usr/local/go"
ENV GOPATH="/go"
ENV PATH="${GOPATH}/bin:${GOROOT}/bin:${PREFIX}/bin:/usr/local/cuda/bin:${PATH}"

# Ensure PKG_CONFIG_PATH is initialized to prevent build warnings.
# This uses the :- operator: if PKG_CONFIG_PATH is unset or null, it expands to an empty string.
ENV PKG_CONFIG_PATH=${PKG_CONFIG_PATH:-}
# prepend the new path.
ENV PKG_CONFIG_PATH="${PREFIX}/lib/pkgconfig${PKG_CONFIG_PATH:+:${PKG_CONFIG_PATH}}"


# --- Build Arguments ---
ARG FFMPEG_ARCANA_VERSION="7.1" # Default version, can be overridden at build time
ARG NVENC_HEADERS_VERSION="sdk/12.8" # Version for nv-codec-headers
ARG GO_VERSION="1.24.3" # Specify Go version, update as needed

# --- System Dependencies ---
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    cmake \
    curl \
    git \
    nasm \
    yasm \
    libtool \
    ca-certificates \
    # common FFmpeg dependencies
    libass-dev \
    libfreetype6-dev \
    libgnutls28-dev \
    libmp3lame-dev \
    libopencore-amrnb-dev \
    libopencore-amrwb-dev \
    libopus-dev \
    librtmp-dev \
    libsdl2-dev \
    libsnappy-dev \
    libsoxr-dev \
    libspeex-dev \
    libtheora-dev \
    libva-dev \
    libvdpau-dev \
    libvidstab-dev \
    libvorbis-dev \
    libvpx-dev \
    libwebp-dev \
    libx264-dev \
    libx265-dev \
    libxvidcore-dev \
    libxml2-dev \
    portaudio19-dev \
    ocl-icd-opencl-dev \
    opencl-headers \
    pkg-config \
    texinfo \
    zlib1g-dev \
    mesa-utils \
    libegl1-mesa \
    xvfb \
    wget \
    unzip \
    gdb \
    gdbserver \
    python3-dev \
    python3-pip \
    && rm -rf /var/lib/apt/lists/*

# --- Install Go ---
RUN curl -fsSL "https://golang.org/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o go.tar.gz && \
    tar -C /usr/local -xzf go.tar.gz && \
    rm go.tar.gz && \
    # Create GOPATH directories
    mkdir -p "$GOPATH/src" "$GOPATH/pkg" "$GOPATH/bin" && \
    chmod -R 777 "$GOPATH"

# --- Install Delve (Go Debugger) ---
RUN go install github.com/go-delve/delve/cmd/dlv@latest

# --- Copy Project and Build Dependencies ---

# Copy the entire git repository into the /opt/goshadertoy directory in the container
COPY . /opt/goshadertoy

# Set the working directory to the project root
WORKDIR /opt/goshadertoy

# --- Run Build Scripts ---
# The scripts are now located relative to the WORKDIR

# ffmpeg arcana
RUN chmod +x scripts/build_ffmpeg_arcana.sh && \
    # Pass build arguments to the script
    FFMPEG_ARCANA_VERSION=${FFMPEG_ARCANA_VERSION} \
    NVENC_HEADERS_VERSION=${NVENC_HEADERS_VERSION} \
    scripts/build_ffmpeg_arcana.sh && \
    # Update library cache
    echo "${PREFIX}/lib" > /etc/ld.so.conf.d/ffmpeg_arcana.conf && \
    ldconfig

# loader.toml
RUN cp scripts/loader.toml /usr/local/bin/loader.toml

# shmframe
RUN chmod +x scripts/build_shmframe.sh && \
    scripts/build_shmframe.sh

# --- Build the Go Application ---
RUN go build -o /usr/local/bin/goshadertoy cmd/main.go

# --- Final Cleanup ---
# Remove the build directory to keep the final image smaller
RUN rm -rf /opt/goshadertoy

# --- Runtime Settings ---
WORKDIR /

# Set the entrypoint to goshadertoy
ENTRYPOINT ["/usr/local/bin/goshadertoy"]

# pass default arguments to goshadertoy if needed
# CMD ["--help"]

#!/bin/bash
set -e

export MAKEFLAGS="-j$(nproc)"

# --- Install nv-codec-headers ---
echo "--- Installing nv-codec-headers ---"
git clone -b "${NVENC_HEADERS_VERSION}" https://github.com/FFmpeg/nv-codec-headers.git
cd nv-codec-headers
make install PREFIX="${PREFIX}"
cd ..
rm -rf nv-codec-headers

# --- Build Static FFmpeg-Arcana ---
echo "--- Cloning and configuring FFmpeg-Arcana ---"
git clone https://github.com/richinsley/ffmpeg-arcana.git
cd ffmpeg-arcana
mkdir -p build && cd build

# Configure ffmpeg-arcana to build STATICALLY and find our other static libs
cmake -DCMAKE_BUILD_TYPE=Release \
      -DCMAKE_INSTALL_PREFIX="${PREFIX}" \
      -DBUILD_SHARED_LIBS=OFF \
      -DARCANA_PATCH_VERSION="${FFMPEG_ARCANA_VERSION}" \
      -DFFMPEG_VERSION="${FFMPEG_ARCANA_VERSION}" \
      -DFFOPT_pkg-config-flags="--static" \
      -DFFOPT_extra-cflags="-I${PREFIX}/include -I/usr/local/cuda/include" \
      -DFFOPT_extra-ldflags="-L${PREFIX}/lib -L/usr/local/cuda/lib64" \
      -DFFOPT_enable-static=true \
      -DFFOPT_disable-shared=true \
      -DFFOPT_enable-gpl=true \
      -DFFOPT_enable-nonfree=true \
      -DFFOPT_enable-libx264=true \
      -DFFOPT_enable-libx265=true \
      -DFFOPT_enable-nvenc=true \
      -DFFOPT_enable-cuvid=true \
      -DFFOPT_enable-cuda-nvcc=true \
      -DFFOPT_enable-libnpp=true \
      ..

echo "--- Compiling and installing static FFmpeg-Arcana ---"
make ${MAKEFLAGS}
make install
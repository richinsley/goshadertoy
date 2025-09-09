#!/bin/bash
set -e

export MAKEFLAGS="-j$(nproc)"

# --- Build Static FFmpeg-Arcana (GENERAL/PORTABLE) ---
echo "--- Cloning and configuring FFmpeg-Arcana (General Build) ---"
git clone https://github.com/richinsley/ffmpeg-arcana.git
cd ffmpeg-arcana
mkdir -p build && cd build

# Configure ffmpeg-arcana WITHOUT NVIDIA support for portability
cmake -DCMAKE_BUILD_TYPE=Release \
      -DCMAKE_INSTALL_PREFIX="${PREFIX}" \
      -DBUILD_SHARED_LIBS=OFF \
      -DARCANA_PATCH_VERSION="${FFMPEG_ARCANA_VERSION}" \
      -DFFMPEG_VERSION="${FFMPEG_ARCANA_VERSION}" \
      -DFFOPT_pkg-config-flags="--static" \
      -DFFOPT_extra-cflags="-I${PREFIX}/include" \
      -DFFOPT_extra-ldflags="-L${PREFIX}/lib" \
      -DFFOPT_enable-static=true \
      -DFFOPT_disable-shared=true \
      -DFFOPT_enable-gpl=true \
      -DFFOPT_enable-nonfree=true \
      -DFFOPT_enable-libx264=true \
      -DFFOPT_enable-libx265=true \
      -DFFOPT_disable-nvenc=true \
      -DFFOPT_disable-cuvid=true \
      -DFFOPT_disable-cuda-nvcc=true \
      -DFFOPT_disable-libnpp=true \
      ..

echo "--- Compiling and installing static FFmpeg-Arcana (General) ---"
make ${MAKEFLAGS}
make install
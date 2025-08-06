#!/usr/bin/env bash
set -e # Exit immediately if a command exits with a non-zero status.

# =================================================================
#            Goshadertoy - Static FFmpeg Builder for macOS
# =================================================================
#
# This script builds static versions of x264, x265, and a custom
# ffmpeg-arcana with VideoToolbox hardware acceleration enabled.
#
# Prerequisites:
# 1. Homebrew (or another way to install build tools)
# 2. Xcode Command Line Tools (`xcode-select --install`)
# 3. Build dependencies: `brew install cmake nasm yasm`
#
# =================================================================

echo "--- Setting up build environment for macOS ---"

# Define the installation directory for our static libraries.
export PREFIX="$(pwd)/release"

# Use all available CPU cores for building.
export MAKEFLAGS="-j$(sysctl -n hw.ncpu)"

# Point pkg-config to our custom directory to ensure it finds our static libs.
export PKG_CONFIG_PATH="${PREFIX}/lib/pkgconfig"

# --- Create directories ---
mkdir -p "${PREFIX}"
mkdir -p build_temp
cd build_temp

# =================================================================
#                        Build Dependencies
# =================================================================

# --- Build Static x264 ---
echo "--- Building static libx264 ---"
git clone --branch stable --depth 1 https://code.videolan.org/videolan/x264.git
cd x264
# For macOS, we compile with PIC enabled for static libraries.
# No host needed, ./configure will detect the architecture (x86_64 or arm64).
./configure \
    --prefix="${PREFIX}" \
    --enable-static \
    --disable-shared \
    --enable-pic \
    --disable-cli
make ${MAKEFLAGS}
make install
cd ..
rm -rf x264
echo "--- libx264 build complete. ---"


# --- Build Static x265 ---
echo "--- Building static libx265 ---"
git clone --branch stable --depth 1 https://bitbucket.org/multicoreware/x265_git.git
cd x265_git/source
cmake -G "Unix Makefiles" . \
    -DCMAKE_INSTALL_PREFIX="${PREFIX}" \
    -DENABLE_SHARED=OFF \
    -DENABLE_PIC=ON \
    -DENABLE_CLI=OFF
make ${MAKEFLAGS}
make install
cd ../..
rm -rf x265_git
echo "--- libx265 build complete. ---"


# =================================================================
#                  Build FFmpeg-Arcana for macOS
# =================================================================

# NOTE: We DO NOT build nv-codec-headers on macOS.

echo "--- Building static ffmpeg-arcana with VideoToolbox ---"
git clone https://github.com/richinsley/ffmpeg-arcana.git
cd ffmpeg-arcana
mkdir -p build && cd build

# Define FFmpeg version for arcana patch
FFMPEG_ARCANA_VERSION="7.1"

# Configure ffmpeg-arcana for a STATIC build on macOS.
# We disable NVIDIA features and enable VideoToolbox.
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
      -DFFOPT_enable-videotoolbox=true \
      -DFFOPT_enable-nvenc=false \
      -DFFOPT_enable-cuvid=false \
      -DFFOPT_enable-cuda-nvcc=false \
      -DFFOPT_enable-libnpp=false \
      -DFFOPT_disable-sdl2=true \
      -DFFOPT_disable-xlib=true \
      -DFFOPT_disable-indev="xcbgrab" \
      ..

echo "--- Compiling and installing static ffmpeg-arcana ---"
make ${MAKEFLAGS}
make install
cd ../..
echo "--- ffmpeg-arcana build complete. ---"


# =================================================================
#                            Cleanup
# =================================================================
echo "--- Cleaning up temporary build files ---"
cd ../..
rm -rf build_temp

echo ""
echo "================================================="
echo "  macOS static toolchain build complete!"
echo "  Artifacts are in the '$(pwd)/release' directory."
echo "================================================="
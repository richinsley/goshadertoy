#!/bin/bash
set -e

export MAKEFLAGS="-j$(nproc)"

# --- Build Static x264 ---
echo "--- Building libx264 ---"
git clone --branch stable --depth 1 https://code.videolan.org/videolan/x264.git
cd x264
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

# --- Build Static x265 ---
echo "--- Building libx265 ---"
git clone --branch stable --depth 1 https://bitbucket.org/multicoreware/x265_git.git
cd x265_git/build/linux
# NOTE: We must enable PIC for static libs that will be linked into a shared object or executable
cmake -G "Unix Makefiles" ../../source \
    -DCMAKE_INSTALL_PREFIX="${PREFIX}" \
    -DENABLE_SHARED=OFF \
    -DENABLE_PIC=ON \
    -DENABLE_CLI=OFF
make ${MAKEFLAGS}
make install
cd ../../..
rm -rf x265_git
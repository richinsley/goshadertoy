#!/bin/bash
set -e

echo "================================================="
echo "  STARTING STATIC TOOLCHAIN BUILD"
echo "  Installation Prefix: ${PREFIX}"
echo "================================================="

# Create a temporary directory for downloading and compiling source code
mkdir -p /opt/build
cd /opt/build

echo "\n>>> Building Dependencies (x264, x265)..."
/usr/local/bin/build-deps.sh

echo "\n>>> Building FFmpeg-Arcana..."
/usr/local/bin/build-ffmpeg.sh

echo "\n================================================="
echo "  BUILD COMPLETE"
echo "  Artifacts are in ${PREFIX}"
echo "  You can now find the toolchain in the host directory you mounted to ${PREFIX}."
echo "================================================="
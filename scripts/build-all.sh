#!/bin/bash
set -e

# Default to general build if no argument provided
BUILD_TYPE="${1:-general}"

echo "=== Building FFmpeg Static Libraries (${BUILD_TYPE}) ==="

# Build dependencies (common to both builds)
/usr/local/bin/build-deps.sh

# Build FFmpeg based on the specified type
case "${BUILD_TYPE}" in
    "nvidia")
        echo "=== Building with NVIDIA support ==="
        /usr/local/bin/build-ffmpeg-nvidia.sh
        ;;
    "general"|"portable")
        echo "=== Building portable version (no NVIDIA) ==="
        /usr/local/bin/build-ffmpeg-general.sh
        ;;
    *)
        echo "Error: Unknown build type '${BUILD_TYPE}'"
        echo "Usage: $0 [nvidia|general|portable]"
        exit 1
        ;;
esac

echo "=== Build complete! ==="

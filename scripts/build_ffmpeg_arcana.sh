#!/usr/bin/env bash
set -e # Exit immediately if a command exits with a non-zero status.


# --- Environment Variables ---
export FFMPEGVERSION="${FFMPEG_ARCANA_VERSION:-7.1}"
export PREFIX="${PREFIX:-/usr/local}" # Use PREFIX from Dockerfile ENV or default
export MAKEFLAGS="-j$(nproc)"

echo "### Install prefix: ${PREFIX} ###"
echo "### FFmpeg Arcana Version: ${FFMPEGVERSION} ###"

## this needs to match the requirements for the ffmpeg-arcana build
export NVENCVERSION="sdk/12.2"

# --- Install nv-codec-headers ---
echo "### Installing nv-codec-headers... ###"
git clone -b $NVENCVERSION https://github.com/FFmpeg/nv-codec-headers.git /opt/nv-codec-headers
cd /opt/nv-codec-headers
# Install to the same PREFIX so pkg-config can find it
make install PREFIX="${PREFIX}"
cd / # Return to a neutral directory

# Update PKG_CONFIG_PATH to ensure the system finds the .pc file for ffnvcodec
export PKG_CONFIG_PATH="${PREFIX}/lib/pkgconfig${PKG_CONFIG_PATH:+:${PKG_CONFIG_PATH}}"
echo "### PKG_CONFIG_PATH: ${PKG_CONFIG_PATH} ###"

# Verify ffnvcodec.pc is found (optional debug)
echo "### Checking for ffnvcodec.pc via pkg-config: ###"
if pkg-config --exists --print-errors ffnvcodec; then
    echo "ffnvcodec.pc found by pkg-config."
    pkg-config --modversion ffnvcodec
else
    echo "ffnvcodec.pc NOT found by pkg-config. This is likely the cause of the configure error."
fi
echo "### End of pkg-config check ###"


# --- Clone ffmpeg-arcana ---
echo "### Cloning ffmpeg-arcana repository... ###"
git clone https://github.com/richinsley/ffmpeg-arcana.git /opt/ffmpeg-arcana
cd /opt/ffmpeg-arcana

# --- Build ffmpeg-arcana with CMake ---
echo "### Configuring ffmpeg-arcana... ###"
mkdir -p build && cd build

# Define a function to print logs and exit if FFmpeg configure (during make) fails
handle_ffmpeg_configure_failure() {
    local exit_code=$1
    echo "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
    echo "!!! FFmpeg configure step (triggered by make) failed with exit code $exit_code. !!!"
    echo "!!! Dumping FFmpeg configure logs:                                     !!!"
    echo "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
    
    LOG_DIR="./ffmpeg_arcana/ffmpeg_pref/src/ffmpeg_target-stamp/"
    LOG_FILES_PATTERN="${LOG_DIR}ffmpeg_target-configure-*.log"

    echo "### Attempting to dump all logs matching: ${LOG_FILES_PATTERN} ###"
    
    # Check if the directory exists
    if [ ! -d "$LOG_DIR" ]; then
        echo "!!! Log directory not found: $LOG_DIR !!!"
        echo "!!! Listing contents of current directory ($(pwd)): "
        ls -la
        exit $exit_code
    fi

    # Use a loop to cat all matching files, printing their names first
    local found_logs=0
    for log_file in $(ls -t $LOG_FILES_PATTERN 2>/dev/null); do
        if [ -f "$log_file" ]; then
            echo "--- Contents of $log_file ---"
            cat "$log_file"
            echo "--- End of $log_file ---"
            found_logs=$((found_logs + 1))
        fi
    done

    if [ "$found_logs" -eq 0 ]; then
        echo "!!! No FFmpeg configure log files found matching pattern: $LOG_FILES_PATTERN !!!"
        echo "!!! Listing contents of ${LOG_DIR} directory:"
        ls -la "${LOG_DIR}" || echo "Could not list stamp directory."
        echo "!!! Listing contents of ./ffmpeg_arcana/ffmpeg_pref/src/ directory:"
        ls -la ./ffmpeg_arcana/ffmpeg_pref/src/ || echo "Could not list src directory."
    fi
    exit $exit_code
}

# Configure ffmpeg-arcana (generates Makefiles)
cmake -DCMAKE_BUILD_TYPE=Release \
      -DCMAKE_INSTALL_PREFIX="${PREFIX}" \
      -DARCANA_PATCH_VERSION="${FFMPEGVERSION}" \
      -DFFMPEG_VERSION="${FFMPEGVERSION}" \
      -DFFOPT_extra-cflags="-I/usr/local/cuda/include" \
      -DFFOPT_extra-ldflags="-L/usr/local/cuda/lib64" \
      -DFFOPT_enable-gpl=true \
      -DFFOPT_enable-nonfree=true \
      -DFFOPT_enable-nvenc=true \
      -DFFOPT_enable-cuvid=true \
      -DFFOPT_enable-cuda-nvcc=true \
      -DFFOPT_enable-libnpp=true \
      ..

echo "### CMake configuration successful. Compiling ffmpeg-arcana (this will trigger FFmpeg configure)... ###"
# The 'make' command will trigger the actual FFmpeg configure step which is failing.
# We will check its exit code explicitly.
make ${MAKEFLAGS} || handle_ffmpeg_configure_failure $?

# If make was successful, the script continues here.
echo "### ffmpeg-arcana compilation successful. Installing... ###"
make install

# create a symlink to the ffmpeg_arcana binary for simplicity
ln -sf "/usr/local/bin/ffmpeg_arcana" /usr/bin/ffmpeg

# --- Cleanup ---
echo "### Cleaning up build files... ###"
cd /
rm -rf /opt/ffmpeg-arcana /opt/nv-codec-headers

echo "### ffmpeg-arcana build and installation complete. ###"

#!/usr/bin/env bash
set -e # Exit immediately if a command exits with a non-zero status.

export ARCANA_ROOT=/usr/local

# --- Build shmframe with CMake ---
# Change directory to the shmframe folder relative to the WORKDIR
cd shmframe 
echo "### Configuring shmframe... ###"
mkdir -p build && cd build
cmake -DCMAKE_BUILD_TYPE=Release -DARCANA_ROOT=$ARCANA_ROOT ..
make
make install
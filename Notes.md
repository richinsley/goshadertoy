# Build ffmpeg arcana with nvenc
export HERE=$PWD
export ARCANA_ROOT=/home/rich/projects/goshadertoy/release
export NVENCVERSION="sdk/12.2"
export FFMPEGVERSION=7.1

## install nvenc headers
git clone -b $NVENCVERSION https://github.com/FFmpeg/nv-codec-headers.git nv-codec-headers
cd nv-codec-headers
make install PREFIX="${ARCANA_ROOT}"

## clone arcana
cd $HERE
git clone https://github.com/richinsley/ffmpeg-arcana.git
cd ffmpeg-arcana
mkdir -p build
cd build

## build and point the pkg config path in release:
export PKG_CONFIG_PATH=$ARCANA_ROOT/lib/pkgconfig:$PKG_CONFIG_PATH
cmake -DCMAKE_BUILD_TYPE=Release \
      -DCMAKE_INSTALL_PREFIX="${ARCANA_ROOT}" \
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
make && make install

## build 
PKG_CONFIG_PATH=$(pwd)/release/lib/pkgconfig CGO_ENABLED=1 go build -ldflags "-w -s" -o goshadertoy ./cmd/main.go
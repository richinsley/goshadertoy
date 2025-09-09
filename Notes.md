# Build ffmpeg dependencies

## Linux
Linux requires Docker with the Nvidia Docker Toolkit
```bash
# Build portable version (default)
docker build -t ffmpeg-static-builder .
docker run --rm -v "$(pwd)/release:/dist" ffmpeg-static-builder

# Or explicitly specify general
docker run --rm -v "$(pwd)/release:/dist" ffmpeg-static-builder general

# Build NVIDIA version
docker run --rm -v "$(pwd)/release:/dist" ffmpeg-static-builder nvidia

# Or use build args
docker build --build-arg BUILD_TYPE=nvidia -t ffmpeg-static-builder .
docker run --rm -v "$(pwd)/release:/dist" ffmpeg-static-builder
```

## MacOS
Install dependencies
```bash
# Install Xcode Command Line Tools if you haven't already
xcode-select --install

# Install build dependencies with Homebrew
brew install cmake nasm yasm
```

Run build script for ffmpeg
```bash
chmod +x ./scripts/build-macos.sh
./scripts/build-macos.sh
```

## build static
PKG_CONFIG_PATH=$(pwd)/release/lib/pkgconfig CGO_ENABLED=1 go build -ldflags "-w -s" -o goshadertoy ./cmd/main.go
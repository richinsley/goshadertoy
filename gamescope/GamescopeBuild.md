
# Setup gamescope
Tested on a clean Ubuntu 25.04 desktop installation with an nVidia RTX 3090

## Core build dependencies
sudo apt install -y \
  build-essential \
  meson \
  ninja-build \
  pkg-config \
  cmake

## Graphics and Vulkan dependencies
sudo apt install -y \
  libvulkan-dev \
  vulkan-tools \
  libegl1-mesa-dev \
  libegl-dev \
  libgbm-dev \
  libgles2-mesa-dev \
  mesa-common-dev

## Wayland dependencies
sudo apt install -y \
  libwayland-dev \
  wayland-protocols \
  libwayland-client0 \
  libwayland-server0

## X11 dependencies
sudo apt install -y \
  libx11-dev \
  libx11-xcb-dev \
  libxdamage-dev \
  libxcomposite-dev \
  libxres-dev \
  libxmu-dev \
  libxi-dev \
  libxcursor-dev \
  libxss-dev \
  libxxf86vm-dev \
  libxrandr-dev \
  libxinerama-dev \
  libxtst-dev \
  libxcb-composite0-dev \
  libxcb-ewmh-dev \
  libxcb-icccm4-dev \
  libxcb-res0-dev

## Audio and media dependencies
sudo apt install -y \
  libpipewire-0.3-dev

## Hardware data and system libraries
sudo apt install -y \
  hwdata \
  libpixman-1-dev \
  liblcms2-dev \
  libudev-dev \
  libseat-dev \
  libinput-dev \
  libsystemd-dev \
  libcap-dev \
  libdrm-dev \
  libdisplay-info-dev \
  libdecor-0-dev \
  libavif-dev \
  libsdl2-dev \
  libluajit-5.1-dev

## OpenVR (optional, for VR support)
sudo apt install -y \
  libopenvr-dev

## GLSL compiler
sudo apt install -y \
  glslang-tools \
  spirv-tools

## Runtime dependencies for testing
sudo apt install -y \
  mesa-utils \
  wayland-utils \
  xwayland

## tools we need
sudo apt install -y \
  drm-info

## Add your user to required groups
sudo usermod -a -G video,input,render,tty $USER

## clone and build gamescope
git clone https://github.com/ValveSoftware/gamescope.git
cd gamescope
git submodule update --init

meson setup build/ \
  -Ddrm_backend=enabled \
  -Dwlroots:backends=drm,libinput \
  -Dwlroots:renderers=gles2,vulkan \
  -Dwlroots:allocators=gbm

ninja -C build/
sudo ninja -C build/ install
sudo ldconfig

## gamescope help:
```
rich@rich-NUC12DCMi7:~/projects/gamescope/gamescope$ gamescope --help
[gamescope] [Info]  console: gamescope version 3.16.15 (gcc 14.2.0)
usage: gamescope [options...] -- [command...]

Options:
  --help                         show help message
  -W, --output-width             output width
  -H, --output-height            output height
  -w, --nested-width             game width
  -h, --nested-height            game height
  -r, --nested-refresh           game refresh rate (frames per second)
  -m, --max-scale                maximum scale factor
  -S, --scaler                   upscaler type (auto, integer, fit, fill, stretch)
  -F, --filter                   upscaler filter (linear, nearest, fsr, nis, pixel)
                                     fsr => AMD FidelityFX™ Super Resolution 1.0
                                     nis => NVIDIA Image Scaling v1.0.3
  --sharpness, --fsr-sharpness   upscaler sharpness from 0 (max) to 20 (min)
  --expose-wayland               support wayland clients using xdg-shell
  -s, --mouse-sensitivity        multiply mouse movement by given decimal number
  --backend                      select rendering backend
                                     auto => autodetect (default)
                                     drm => use DRM backend (standalone display session)
                                     sdl => use SDL backend
                                     openvr => use OpenVR backend (outputs as a VR overlay)
                                     headless => use headless backend (no window, no DRM output)
                                     wayland => use Wayland backend
  --cursor                       path to default cursor image
  -R, --ready-fd                 notify FD when ready
  --rt                           Use realtime scheduling
  -T, --stats-path               write statistics to path
  -C, --hide-cursor-delay        hide cursor image after delay
  -e, --steam                    enable Steam integration
  --xwayland-count               create N xwayland servers
  --prefer-vk-device             prefer Vulkan device for compositing (ex: 1002:7300)
  --force-orientation            rotate the internal display (left, right, normal, upsidedown)
  --force-windows-fullscreen     force windows inside of gamescope to be the size of the nested display (fullscreen)
  --cursor-scale-height          if specified, sets a base output height to linearly scale the cursor against.
  --virtual-connector-strategy   Specifies how we should make virtual connectors.
  --hdr-enabled                  enable HDR output (needs Gamescope WSI layer enabled for support from clients)
                                 If this is not set, and there is a HDR client, it will be tonemapped SDR.
  --sdr-gamut-wideness           Set the 'wideness' of the gamut for SDR comment. 0 - 1.
  --hdr-sdr-content-nits         set the luminance of SDR content in nits. Default: 400 nits.
  --hdr-itm-enabled              enable SDR->HDR inverse tone mapping. only works for SDR input.
  --hdr-itm-sdr-nits             set the luminance of SDR content in nits used as the input for the inverse tone mapping process.
                                 Default: 100 nits, Max: 1000 nits
  --hdr-itm-target-nits          set the target luminace of the inverse tone mapping process.
                                 Default: 1000 nits, Max: 10000 nits
  --framerate-limit              Set a simple framerate limit. Used as a divisor of the refresh rate, rounds down eg 60 / 59 -> 60fps, 60 / 25 -> 30fps. Default: 0, disabled.
  --mangoapp                     Launch with the mangoapp (mangohud) performance overlay enabled. You should use this instead of using mangohud on the game or gamescope.
  --adaptive-sync                Enable adaptive sync if available (variable rate refresh)

Nested mode options:
  -o, --nested-unfocused-refresh game refresh rate when unfocused
  -b, --borderless               make the window borderless
  -f, --fullscreen               make the window fullscreen
  -g, --grab                     grab the keyboard
  --force-grab-cursor            always use relative mouse mode instead of flipping dependent on cursor visibility.
  --display-index                forces gamescope to use a specific display in nested mode.
Embedded mode options:
  -O, --prefer-output            list of connectors in order of preference (ex: DP-1,DP-2,DP-3,HDMI-A-1)
  --default-touch-mode           0: hover, 1: left, 2: right, 3: middle, 4: passthrough
  --generate-drm-mode            DRM mode generation algorithm (cvt, fixed)
  --immediate-flips              Enable immediate flips, may result in tearing

VR mode options:
  --vr-overlay-key                         Sets the SteamVR overlay key to this string
  --vr-app-overlay-key                                          Sets the SteamVR overlay key to use for child apps
  --vr-overlay-explicit-name               Force the SteamVR overlay name to always be this string
  --vr-overlay-default-name                Sets the fallback SteamVR overlay name when there is no window title
  --vr-overlay-icon                        Sets the SteamVR overlay icon to this file
  --vr-overlay-show-immediately            Makes our VR overlay take focus immediately
  --vr-overlay-enable-control-bar          Enables the SteamVR control bar
  --vr-overlay-enable-control-bar-keyboard Enables the SteamVR keyboard button on the control bar
  --vr-overlay-enable-control-bar-close    Enables the SteamVR close button on the control bar
  --vr-overlay-enable-click-stabilization  Enables the SteamVR click stabilization
  --vr-overlay-modal                       Makes our VR overlay appear as a modal
  --vr-overlay-physical-width              Sets the physical width of our VR overlay in metres
  --vr-overlay-physical-curvature          Sets the curvature of our VR overlay
  --vr-overlay-physical-pre-curve-pitch    Sets the pre-curve pitch of our VR overlay
  --vr-scrolls-speed                       Mouse scrolling speed of trackpad scroll in VR. Default: 8.0

Debug options:
  --disable-layers               disable libliftoff (hardware planes)
  --debug-layers                 debug libliftoff
  --debug-focus                  debug XWM focus
  --synchronous-x11              force X11 connection synchronization
  --debug-hud                    paint HUD with debug info
  --debug-events                 debug X11 events
  --force-composition            disable direct scan-out
  --composite-debug              draw frame markers on alternating corners of the screen when compositing
  --disable-color-management     disable color management
  --disable-xres                 disable XRes for PID lookup
  --hdr-debug-force-support      forces support for HDR, etc even if the display doesn't support it. HDR clients will be outputted as SDR still in that case.
  --hdr-debug-force-output       forces support and output to HDR10 PQ even if the output does not support it (will look very wrong if it doesn't)
  --hdr-debug-heatmap            displays a heatmap-style debug view of HDR luminence across the scene in nits.
Reshade shader options:
  --reshade-effect               sets the name of a reshade shader to use in either /usr/share/gamescope/reshade/Shaders or ~/.local/share/gamescope/reshade/Shaders
  --reshade-technique-idx        sets technique idx to use from the reshade effect

Steam Deck options:
  --mura-map                     Set the mura compensation map to use for the display. Takes in a path to the mura map.

Keyboard shortcuts:
  Super + F                      toggle fullscreen
  Super + N                      toggle nearest neighbour filtering
  Super + U                      toggle FSR upscaling
  Super + Y                      toggle NIS upscaling
  Super + I                      increase FSR sharpness by 1
  Super + O                      decrease FSR sharpness by 1
  Super + S                      take a screenshot
  Super + G                      toggle keyboard grab
```
## build goshadertoy
Integrating goshadertoy with gamescope requires glfw to be built with the wayland build tag:
```bash
PKG_CONFIG_PATH=$(pwd)/release/lib/pkgconfig CGO_ENABLED=1 go build -tags x11,wayland -ldflags "-w -s" -o goshadertoy ./cmd/main.go
```

## Launch gamescope
Switch to TTY2 Ctrl + Alt + F2
```bash
# create XDG_RUNTIME_DIR
export XDG_RUNTIME_DIR=/tmp/gamescope-runtime-fixed
mkdir -p $XDG_RUNTIME_DIR
chmod 700 $XDG_RUNTIME_DIR
export SHADERTOY_KEY=rt8lR1
# start gamescope
gamescope --backend drm --rt  -W 2560 -H 1440 --hdr-enabled --hdr-sdr-content-nits 400 -f
```

## Launch goshadertoy in another shell:
```bash
export XDG_RUNTIME_DIR=/tmp/gamescope-runtime-fixed
export WAYLAND_DISPLAY=gamescope-0
export SHADERTOY_KEY=blabla
./goshadertoy -shader llj3Rz -audio-output-device hw:1,3
```

# gamescope-manager

## build and launch gamescope-manager (on physical tty)
export XDG_RUNTIME_DIR=/tmp/gamescope-runtime-fixed
mkdir -p $XDG_RUNTIME_DIR
go build -o gamescope-manager gamescope/main.go
./gamescope-manager

## debugging gamescope-manager
# run tty-runner.sh on tty2

# send gamescope-manager launch dlv to tty-runner:
./gamescope/debug-on-tty2.sh $PWD/gamescope-manager 2345 --your-manager-args

## launch config to attach to gamescope-manager
```json
{
    "name": "Connect to tty2 debugger",
    "type": "go",
    "request": "attach",
    "mode": "remote",
    // This is the crucial fix. It tells delve where the source code
    // root is on the server, which is your VS Code workspace folder.
    "remotePath": "${workspaceFolder}",
    "host": "localhost",
    "port": 2345,
    "cwd": "${workspaceFolder}"
}
```

## launch config for goshadertoy
```json
{
    "name": "Launch Render Sound Shader (gamescope)",
    "type": "go",
    "request": "launch",
    "mode": "exec", // Tells the debugger to EXECUTE a file, not build one.
    "program": "${workspaceFolder}/goshadertoy/goshadertoy-debug", // Point to the binary from the task.
    "preLaunchTask": "build-goshadertoy-debug", // Run our new debug build task.
    "cwd": "${workspaceFolder}/goshadertoy", // Set working directory so it can find shaders, etc.
    "args": [
        "-shader", "3XyXzD",
        "-audio-output-device", "hw:1,3",
        "-gamescope-socket", "/tmp/gamescope.sock",
    ],
    "env": {
        "SHADERTOY_KEY": "rt8lR1",
    }
}
```
## build ffmpeg_arcana
<!-- # decklink requires:
# CONFIG += --enable-decklink \
#           --extra-cflags="-I/Users/richardinsley/Projects/arcana/blackmagic_sdk/Mac/include" \
#           --enable-nonfree -->

```bash
building decklink for macos provides several problems.  Currently, on decklink sdm 14.2 is supported.
ALSO! ffmpeg has a file "VERSION" which causes a conflict with the sdk using "#include <version>" so we
need to delete that file: /Users/richardinsley/Projects/pyshadertranslator/ffmpeg-arcana/build/ffmpeg_arcana/ffmpeg_pref/src/ffmpeg_target/VERSION:
rm /Users/richardinsley/Projects/pyshadertranslator/ffmpeg-arcana/build/ffmpeg_arcana/ffmpeg_pref/src/ffmpeg_target/VERSION

export ARCANA_ROOT=$HOME/Projects/pyshadertranslator/release
mkdir build
cd build
cmake -DCMAKE_BUILD_TYPE=Debug \
  -DCMAKE_INSTALL_PREFIX=$ARCANA_ROOT \
  -DARCANA_PATCH_VERSION=7.1 \
  -DFFMPEG_VERSION=7.1 \
  -DFFOPT_enable-gpl=true \
  -DFFOPT_enable-nonfree=true \
  -DFFOPT_enable-decklink=true \
  -DFFOPT_extra-cflags="-I/Users/richardinsley/Projects/pyshadertranslator/blackmagic_sdk/Mac/include" \
  -DFFOPT_extra-cxxflags="-std=c++11 -stdlib=libc++ -Wno-deprecated-declarations -fno-aligned-allocation" \
  -DFFOPT_extra-ldflags="-stdlib=libc++" \
  -DFFOPT_disable-stripping=true \
  ..
make && make install
```

## build shmframe
cmake -DCMAKE_BUILD_TYPE=Debug -DARCANA_FFMPEG_PATH=$ARCANA_ROOT ..
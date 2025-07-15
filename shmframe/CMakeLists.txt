cmake_minimum_required(VERSION 3.14)

project(test_arcana_plugs LANGUAGES CXX C)

# Ensure ARCANA_ROOT is defined
if(NOT DEFINED ARCANA_ROOT)
    message(FATAL_ERROR "ARCANA_ROOT must be defined")
endif()

set(CMAKE_INCLUDE_CURRENT_DIR ON)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

# we need this when mixing C++ with arcana ffmpeg libs
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -D__STDC_CONSTANT_MACROS")

set(CMAKE_POSITION_INDEPENDENT_CODE ON)

# Find pkg-config
find_package(PkgConfig REQUIRED)

# Set PKG_CONFIG_PATH to include ARCANA_ROOT
set(ENV{PKG_CONFIG_PATH} "${ARCANA_ROOT}/lib/pkgconfig:$ENV{PKG_CONFIG_PATH}")

# Find all the required libraries using pkg-config
# Use IMPORTED_TARGET to create proper targets
pkg_check_modules(ARCANA IMPORTED_TARGET
    libavfilter_arcana
    libswscale_arcana
    libpostproc_arcana
    libavformat_arcana
    libavcodec_arcana
    libswresample_arcana
    libavutil_arcana
)

include_directories(
    ${ARCANA_ROOT}/include/arcana
    ${ARCANA_ROOT}/include/arcana/libavprivate
)

link_directories(
    ${ARCANA_ROOT}/lib
)

add_library(test_arcana_plugs SHARED
    shmframe.c 
    shm_muxer.c
    arcana_loader.c
)

target_link_libraries(test_arcana_plugs
    PkgConfig::ARCANA
)

install(TARGETS test_arcana_plugs
        LIBRARY DESTINATION ${ARCANA_ROOT}/lib
)

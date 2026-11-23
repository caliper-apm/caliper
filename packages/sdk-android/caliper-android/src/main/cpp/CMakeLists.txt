cmake_minimum_required(VERSION 3.22.1)
project(caliper_native LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

add_library(caliper_native SHARED
    caliper_ndk_crash.cpp
)

# Emit full DWARF debug info so unstripped libcaliper_native.so can be uploaded via
# `caliper-symbols upload --kind native_elf` for server-side ELF DWARF symbolication.
# Compile without C++ exceptions/RTTI and without linking libc++_shared.so so any
# pure-Java/Kotlin host APK can load libcaliper_native.so without shipping libc++_shared.so.
target_compile_options(caliper_native PRIVATE
    -O2
    -g
    -fno-exceptions
    -fno-rtti
    -Wall
    -Wextra
    -fvisibility=hidden
)

target_link_options(caliper_native PRIVATE
    -nostdlib++
    -Wl,--build-id=sha1
)

target_link_libraries(caliper_native
    log
)

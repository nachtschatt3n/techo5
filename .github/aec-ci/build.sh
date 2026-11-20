#!/bin/sh
# Builds tools/aec/techo5-aec.cpp of a checkout against the installed webrtc-audio-processing-1,
# the way tools/linux/build-aec.sh does: with AEC3's own header from the library's source release
# when the source uses it (for --linear), and without it when that is not possible.
#   build.sh <checkout> <output>
set -eu
src=$1
out=$2
cpp=$src/tools/aec/techo5-aec.cpp
WAP_VERSION=1.3
WAP_TAR=webrtc-audio-processing-$WAP_VERSION.tar.xz
WAP_SHA256=2365e93e778d7b61b5d6e02d21c47d97222e9c7deff9e1d0838ad6ec2e86f1b9
WAP_URL=https://freedesktop.org/software/pulseaudio/webrtc-audio-processing/$WAP_TAR
work=$(mktemp -d)
have=$(pkg-config --modversion webrtc-audio-processing-1)
echo "== webrtc-audio-processing-1 $have, $(uname -m)"
extra=""
if grep -q TECHO5_AEC_LINEAR "$cpp" && [ "$have" = "$WAP_VERSION" ]; then
	curl -fsSL -o "$work/$WAP_TAR" "$WAP_URL"
	echo "$WAP_SHA256  $work/$WAP_TAR" | sha256sum -c
	mkdir -p "$work/wap"
	tar -xJf "$work/$WAP_TAR" -C "$work/wap" --strip-components=1 "webrtc-audio-processing-$WAP_VERSION/webrtc"
	extra="-I$work/wap/webrtc -DWEBRTC_APM_DEBUG_DUMP=0"
fi
compile() {
	g++ -O2 -std=c++17 -o "$out" "$cpp" $(pkg-config --cflags webrtc-audio-processing-1) $1 $(pkg-config --libs webrtc-audio-processing-1)
}
mkdir -p "$(dirname "$out")"
if [ -n "$extra" ] && compile "$extra"; then
	echo "built with --linear"
else
	[ -n "$extra" ] && echo "warning: AEC3's header did not compile; building without --linear"
	compile ""
fi
strip "$out" 2>/dev/null || true
ls -la "$out"

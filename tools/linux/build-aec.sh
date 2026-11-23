#!/bin/bash
# build-aec.sh — compile tools/aec/techo5-aec.cpp for armv7 inside an Alpine root under
# QEMU user emulation, in WSL. The first run makes the root (~/alpine-armv7-sdk) from the
# minirootfs and installs the compiler and webrtc-audio-processing; later runs reuse it.
#
#   tools/linux/build-aec.sh [-o bin/techo5-aec-arm]
#
# Needs what wsl-build.sh needs (qemu-user-static, binfmt-support, uidmap, ~/apk/apk.static)
# and the Alpine minirootfs from TECHO5_INPUTS (default: inputs/ in this repository). For the
# helper's --linear it also fetches the library's source release once (curl, xz), for a header.
set -euo pipefail
# The repo: from this script's place, or TECHO5_ROOT when the script was copied elsewhere (deploy
# copies it to a Linux path so WSL's bash does not trip over CRLF).
ROOT=${TECHO5_ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}
export TECHO5_ROOT=$ROOT
INPUTS=${TECHO5_INPUTS:-$ROOT/inputs}
SDK=$HOME/alpine-armv7-sdk
APK=$HOME/apk/apk.static
OUT=$ROOT/bin/techo5-aec-arm
while [ $# -gt 0 ]; do
	case "$1" in
	-o) OUT=$2; shift 2;;
	*) echo "unknown argument: $1" >&2; exit 1;;
	esac
done

if [ -z "${TECHO5_IN_NS:-}" ]; then
	exec unshare -Ur --map-auto env TECHO5_IN_NS=1 bash "$0" -o "$OUT"
fi

if [ ! -x "$SDK/usr/bin/g++" ]; then
	echo "== making the armv7 SDK root at $SDK"
	rm -rf "$SDK"; mkdir -p "$SDK"
	tar -xzf "$(ls "$INPUTS"/alpine-minirootfs-*-armv7.tar.gz | head -1)" -C "$SDK"
	cp /etc/resolv.conf "$SDK/etc/resolv.conf"
	"$APK" --root "$SDK" --arch armv7 --no-cache add build-base webrtc-audio-processing-1-dev pkgconf
fi

mkdir -p "$SDK/build"
cp "$ROOT/tools/aec/techo5-aec.cpp" "$SDK/build/"

# AEC3's own header, for the helper's --linear (techo5-aec.cpp says why it needs it). The -dev package
# leaves it out, so it comes from the library's source release of the same version: only headers are
# taken from it, and the code that runs is still the installed library's. The header has to describe
# the very library it is linked against, so it is used only when the installed version is this one;
# with any other, or when it will not compile, the helper is built without it and refuses --linear,
# which the daemon takes in its stride.
WAP_VERSION=1.3
WAP_TAR=webrtc-audio-processing-$WAP_VERSION.tar.xz
WAP_SHA256=2365e93e778d7b61b5d6e02d21c47d97222e9c7deff9e1d0838ad6ec2e86f1b9
WAP_URL=https://freedesktop.org/software/pulseaudio/webrtc-audio-processing/$WAP_TAR
LINEAR=""
have=$(chroot "$SDK" pkg-config --modversion webrtc-audio-processing-1 || true)
if [ "$have" = "$WAP_VERSION" ]; then
	mkdir -p "$SDK/src"
	if [ ! -f "$SDK/src/$WAP_TAR" ]; then
		echo "== fetching $WAP_TAR for AEC3's header"
		curl -fsSL -o "$SDK/src/$WAP_TAR.part" "$WAP_URL" && mv "$SDK/src/$WAP_TAR.part" "$SDK/src/$WAP_TAR" || true
	fi
	if [ -f "$SDK/src/$WAP_TAR" ] && echo "$WAP_SHA256  $SDK/src/$WAP_TAR" | sha256sum -c --quiet -; then
		rm -rf "$SDK/build/wap"; mkdir -p "$SDK/build/wap"
		tar -xJf "$SDK/src/$WAP_TAR" -C "$SDK/build/wap" --strip-components=1 "webrtc-audio-processing-$WAP_VERSION/webrtc"
		LINEAR="-I/build/wap/webrtc -DWEBRTC_APM_DEBUG_DUMP=0"
	else
		echo "warning: no checked $WAP_TAR; building the helper without --linear" >&2
	fi
else
	echo "warning: webrtc-audio-processing-1 is ${have:-missing}, not $WAP_VERSION; building the helper without --linear" >&2
fi

echo "== compiling under QEMU"
# Installed headers first, so the source release only fills in what the package leaves out.
compile='cd /build && g++ -O2 -std=c++17 -o techo5-aec techo5-aec.cpp $(pkg-config --cflags webrtc-audio-processing-1) '
link=' $(pkg-config --libs webrtc-audio-processing-1) && strip techo5-aec && ls -la techo5-aec'
if [ -n "$LINEAR" ] && chroot "$SDK" /bin/sh -c "$compile $LINEAR $link"; then
	echo "built with --linear"
else
	[ -n "$LINEAR" ] && echo "warning: AEC3's header did not compile here; building the helper without --linear" >&2
	chroot "$SDK" /bin/sh -c "$compile $link"
fi
mkdir -p "$(dirname "$OUT")"
cp "$SDK/build/techo5-aec" "$OUT"
echo "built: $OUT"

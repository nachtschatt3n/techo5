#!/usr/bin/env python3
"""Smoke test for the echo canceller helper: pipes synthetic microphone and reference blocks through
it and asserts it writes as many bytes as the protocol says.

    smoke.py <helper> --cases "<flags>;<flags>;..."    (an empty case is no flags at all)

One block in is a 320 sample microphone block then a 320 sample reference block (int16 LE, 16 kHz).
One block out is the 320 processed samples; with --linear the helper writes the four bytes T5LN
first and then two blocks (processed, linear) for every block in.
"""
import argparse
import math
import struct
import subprocess
import sys

BLOCK = 320
FRAMES = 100


def synth(n, f, amp):
    return [int(amp * math.sin(2 * math.pi * f * i / 16000)) for i in range(n)]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('helper')
    ap.add_argument('--cases', default='')
    a = ap.parse_args()
    ref = synth(FRAMES * BLOCK, 300, 8000)
    # The microphone: speech-like tone plus the reference, attenuated and a few samples late.
    mic = [s + (ref[i - 40] // 3 if i >= 40 else 0) for i, s in enumerate(synth(FRAMES * BLOCK, 440, 3000))]
    stdin = b''.join(struct.pack('<%dh' % BLOCK, *mic[i:i + BLOCK]) + struct.pack('<%dh' % BLOCK, *ref[i:i + BLOCK])
                     for i in range(0, FRAMES * BLOCK, BLOCK))
    failed = 0
    for case in a.cases.split(';'):
        flags = case.split()
        name = 'smoke[%s]' % (' '.join(flags) or 'no flags')
        linear = '--linear' in flags
        want = FRAMES * BLOCK * 2 * (2 if linear else 1) + (4 if linear else 0)
        try:
            p = subprocess.run([a.helper] + flags, input=stdin, capture_output=True, timeout=120)
            err = p.stderr.decode(errors='replace').strip()
            problems = []
            if p.returncode != 0:
                problems.append('exit code %d: %s' % (p.returncode, err))
            if len(p.stdout) != want:
                problems.append('wrote %d bytes, expected %d' % (len(p.stdout), want))
            if linear and p.stdout[:4] != b'T5LN':
                problems.append('no T5LN hello, got %r' % p.stdout[:4])
            if 'ready' not in err:
                problems.append('no ready line on stderr: %s' % err)
        except subprocess.TimeoutExpired:
            problems = ['timed out']
        if problems:
            failed += 1
            print('FAIL %s: %s' % (name, '; '.join(problems)))
        else:
            print('ok   %s: %d bytes out for %d blocks in' % (name, len(p.stdout), FRAMES))
    sys.exit(1 if failed else 0)


main()

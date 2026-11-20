#!/usr/bin/env python3
"""Offline tests for the echo canceller helper, tools/aec/techo5-aec.cpp: its arguments, the
microphone delay (--delay-ms) and its timing, one block out for every block in. No device, no WebRTC.

    python3 tools/test_aec_helper.py

The helper is built against tools/aec/stub, a stand-in for WebRTC's header whose canceller subtracts
the reference it was given from the microphone, sample for sample, so what comes out says which
microphone sample met which reference sample. What the real canceller does to the sound is the
library's and is not tested here.

Without a C++ compiler (c++, g++ or clang++) the test is skipped and says so; in CI (CI set) it fails
instead.
"""
import os
import shutil
import struct
import subprocess
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SOURCE = os.path.join(HERE, 'aec', 'techo5-aec.cpp')
STUB = os.path.join(HERE, 'aec', 'stub')

RATE = 16000
BLOCK = 320  # 20 ms, the daemon's frame
HELPER = None


def setUpModule():
    global HELPER
    cxx = os.environ.get('CXX') or next((c for c in ('c++', 'g++', 'clang++') if shutil.which(c)), None)
    if not cxx:
        if os.environ.get('CI'):
            raise RuntimeError('no C++ compiler to build the helper with')
        raise unittest.SkipTest('no C++ compiler: SKIPPED')
    out = tempfile.mkdtemp(prefix='techo5-aec-test-')
    HELPER = os.path.join(out, 'techo5-aec')
    subprocess.run([cxx, '-std=c++17', '-O1', '-Wall', '-I', STUB, '-o', HELPER, SOURCE], check=True)


def tearDownModule():
    if HELPER:
        shutil.rmtree(os.path.dirname(HELPER), ignore_errors=True)


def run(args, blocks):
    """Feeds blocks (pairs of microphone and reference, BLOCK samples each) and returns the samples
    that came out, the exit code and what went to stderr."""
    stdin = b''.join(struct.pack('<%dh' % BLOCK, *mic) + struct.pack('<%dh' % BLOCK, *ref)
                     for mic, ref in blocks)
    p = subprocess.run([HELPER] + args, input=stdin, capture_output=True, timeout=30)
    n = len(p.stdout) // 2
    return list(struct.unpack('<%dh' % n, p.stdout[:2 * n])), p.returncode, p.stderr.decode()


def ramp(blocks):
    """A microphone that counts up and a reference that counts down from 0 (and over a smaller span),
    so every output sample, microphone less reference, says which of each met in it."""
    out = []
    for b in range(blocks):
        n = [b * BLOCK + i for i in range(BLOCK)]
        out.append(([v % 20000 + 1 for v in n], [-(v % 5000) for v in n]))
    return out


def met(mic, ref, d):
    """What the stand-in gives back when the microphone is held back by d samples and the reference is
    not: the microphone from d samples earlier (silence while the line fills) less the reference now."""
    held = ([0] * d + mic)[:len(mic)]
    return [m - r for m, r in zip(held, ref)]


class Delay(unittest.TestCase):
    def test_no_delay_is_how_the_helper_always_ran(self):
        blocks = ramp(5)
        mic = [v for m, _ in blocks for v in m]
        ref = [v for _, r in blocks for v in r]
        for args in ([], ['--delay-ms', '0']):
            out, code, err = run(args, blocks)
            self.assertEqual(code, 0, err)
            self.assertEqual(out, met(mic, ref, 0))

    def test_delay_holds_the_microphone_back_and_keeps_one_block_per_block(self):
        blocks = ramp(7)
        mic = [v for m, _ in blocks for v in m]
        ref = [v for _, r in blocks for v in r]
        for ms in (1, 2, 5, 10, 20, 25, 100):
            out, code, err = run(['--delay-ms', str(ms)], blocks)
            self.assertEqual(code, 0, err)
            # Exactly as many samples out as microphone samples in: nothing dropped, nothing held over.
            self.assertEqual(len(out), len(mic), ms)
            # The microphone from ms earlier against the reference as it came, sample for sample,
            # across block edges whether or not the delay is a whole number of blocks: the reference
            # leads, and is never the one held back.
            self.assertEqual(out, met(mic, ref, ms * RATE // 1000), ms)
            self.assertIn('delay=%d ms' % ms, err)

    def test_a_partial_block_at_the_end_brings_nothing_out(self):
        blocks = ramp(3)
        stdin = b''.join(struct.pack('<%dh' % BLOCK, *m) + struct.pack('<%dh' % BLOCK, *r) for m, r in blocks)
        p = subprocess.run([HELPER, '--delay-ms', '5'], input=stdin[:-2], capture_output=True, timeout=30)
        self.assertEqual(p.returncode, 0)
        self.assertEqual(len(p.stdout), 2 * 2 * BLOCK)

    def test_a_gap_between_runs_starts_the_next_one_from_silence(self):
        # The daemon feeds the helper only while something plays. After a pause the line is emptied,
        # so what the next run starts with is silence, not the end of the run before.
        ms = 10
        d = ms * RATE // 1000
        blocks = ramp(4)
        p = subprocess.Popen([HELPER, '--delay-ms', str(ms)], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE)
        try:
            def feed(pairs):
                for mic, ref in pairs:
                    p.stdin.write(struct.pack('<%dh' % BLOCK, *mic) + struct.pack('<%dh' % BLOCK, *ref))
                p.stdin.flush()
                raw = p.stdout.read(2 * BLOCK * len(pairs))
                return list(struct.unpack('<%dh' % (len(raw) // 2), raw))

            first = feed(blocks[:2])
            time.sleep(0.6)
            second = feed(blocks[2:])
        finally:
            p.stdin.close()
            p.wait(timeout=30)
            p.stdout.close()
            p.stderr.close()
        self.assertEqual(p.returncode, 0)
        mic = [v for m, _ in blocks for v in m]
        ref = [v for _, r in blocks for v in r]
        self.assertEqual(first, met(mic[:2 * BLOCK], ref[:2 * BLOCK], d))
        # Without the gap the second run would start with the first run's last d samples.
        self.assertEqual(second, met(mic[2 * BLOCK:], ref[2 * BLOCK:], d))


class Arguments(unittest.TestCase):
    def test_bad_delays_are_refused(self):
        for v in ('-1', '101', 'five', '5ms', '', '2.5'):
            _, code, err = run(['--delay-ms', v], ramp(1))
            self.assertEqual(code, 2, v)
            self.assertIn('--delay-ms takes 0 to 100', err, v)

    def test_unknown_argument_is_refused(self):
        _, code, err = run(['--delay', '5'], ramp(1))
        self.assertEqual(code, 2)
        self.assertIn('unknown argument', err)

    def test_the_daemons_arguments_are_taken(self):
        # What hardware/mic/cancel.go's helperArgs passes.
        out, code, err = run(['--ns', 'low', '--delay-ms', '8'], ramp(2))
        self.assertEqual(code, 0, err)
        self.assertEqual(len(out), 2 * BLOCK)


if __name__ == '__main__':
    unittest.main()

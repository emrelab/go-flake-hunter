#!/usr/bin/env python3
"""Exercise the real TUI through a Unix PTY, including terminal restoration.

The default check uses only Python's standard library. --preview-dir also
renders captured terminal cells as PNGs using Pillow (as make demo does).
"""
import argparse
import copy
import json
import os
from pathlib import Path
import re
import select
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time

if os.name == "nt":
    sys.exit("tui-smoke needs a Unix PTY; run the console checks natively on Windows")
import fcntl
import pty
import termios

ANSI = re.compile(r"\x1b\[([0-9;?]*)([ -/]*)([@-~])")


class Terminal:
    def __init__(self, binary, cwd, args, no_color=False):
        self.master, self.slave = pty.openpty()
        self.resize(110, 34)
        self.original = termios.tcgetattr(self.slave)
        env = dict(os.environ, TERM="xterm-256color", CI="")
        if no_color:
            env["NO_COLOR"] = "1"
        else:
            env.pop("NO_COLOR", None)
        self.process = subprocess.Popen([str(binary), *args], cwd=cwd, env=env,
                                        stdin=self.slave, stdout=self.slave,
                                        stderr=self.slave, start_new_session=True)
        self.capture = bytearray()

    def resize(self, width, height):
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ,
                    struct.pack("HHHH", height, width, 0, 0))

    def send(self, keys):
        os.write(self.master, keys)

    def expect(self, text, timeout=180):
        start = len(self.capture)
        deadline = time.monotonic() + timeout
        while text.encode() not in self.capture[start:]:
            if self.process.poll() is not None:
                raise AssertionError(f"TUI exited {self.process.returncode}: {self.capture[-3000:]!r}")
            if time.monotonic() > deadline:
                raise AssertionError(f"waiting for {text!r}: {self.capture[-3000:]!r}")
            if select.select([self.master], [], [], .1)[0]:
                self.capture.extend(os.read(self.master, 65536))
        # Read any remaining bytes in this frame before saving a preview.
        while select.select([self.master], [], [], .05)[0]:
            self.capture.extend(os.read(self.master, 65536))

    def snapshot(self, path):
        frame = bytes(self.capture).split(b"\x1b[H")[-1].decode("utf-8")
        path.with_suffix(".ansi").write_text(frame)
        render(frame, path)

    def finish(self):
        self.process.wait(timeout=15)
        assert self.process.returncode == 0, self.process.returncode
        restored = termios.tcgetattr(self.slave)
        expected = copy.deepcopy(self.original)
        # XNU adds transient PENDIN when ICANON is restored without flushing
        # typeahead. Compare configuration, not that kernel queue-state bit:
        # github.com/apple/darwin-xnu/blob/main/bsd/kern/tty.c (ttioctl_locked).
        if sys.platform == "darwin":
            restored[3] &= ~termios.PENDIN
            expected[3] &= ~termios.PENDIN
        assert restored == expected, (expected, restored)
        while select.select([self.master], [], [], .05)[0]:
            self.capture.extend(os.read(self.master, 65536))
        assert b"\x1b[?1049h" in self.capture
        assert b"\x1b[?1049l" in self.capture

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait()
        os.close(self.master)
        os.close(self.slave)


def render(frame, path):
    from PIL import Image, ImageDraw, ImageFont
    font_path = next((p for p in ("/System/Library/Fonts/Menlo.ttc",
                                 "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf")
                      if Path(p).exists()), None)
    if not font_path:
        raise RuntimeError("no monospace font for --preview-dir")
    font = ImageFont.truetype(font_path, 16)
    cell, line = font.getlength("M"), 24
    image = Image.new("RGB", (round(110*cell+32), 34*line+46), (15, 23, 42))
    draw = ImageDraw.Draw(image)
    draw.text((16, 8), "go-flake-hunter  ·  live terminal capture", font=font, fill=(148,163,184))
    x = y = 0
    fg, bg = (226,232,240), (15,23,42)
    i = 0
    while i < len(frame):
        match = ANSI.match(frame, i)
        if match:
            args, _, cmd = match.groups()
            nums = [int(v) for v in args.split(";") if v.isdigit()]
            if cmd == "m":
                if args == "0":
                    fg, bg = (226,232,240), (15,23,42)
                else:
                    j = 0
                    while j < len(nums):
                        if nums[j] in (38,48) and j+4 < len(nums) and nums[j+1] == 2:
                            if nums[j] == 38: fg = tuple(nums[j+2:j+5])
                            else: bg = tuple(nums[j+2:j+5])
                            j += 5
                        else: j += 1
            elif cmd == "G": x = (nums[0] if nums else 1)-1
            elif cmd == "K":
                draw.rectangle((16, 38+y*line, image.width-16, 38+(y+1)*line), fill=bg)
            i = match.end()
            continue
        char = frame[i]
        if char == "\r": x = 0
        elif char == "\n": y += 1
        elif y < 34:
            xx, yy = 16+x*cell, 38+y*line
            draw.rectangle((xx, yy, xx+cell, yy+line), fill=bg)
            draw.text((xx, yy), char, font=font, fill=fg)
            x += 1
        i += 1
    image.save(path)


def run(binary, previews):
    root = Path(__file__).resolve().parent.parent
    with tempfile.TemporaryDirectory(prefix="gfh-tui-") as scratch:
        project = Path(scratch)/"checkout-service"
        shutil.copytree(root/"testdata/fixtures/order-basic", project,
                        ignore=shutil.ignore_patterns(".go-flake-hunter", "*goflakehunter_test.go", "zz_goflakehunter_*"))
        terminal = Terminal(binary, project, ["tui", "--short", "--budget", "100"])
        try:
            terminal.expect("Workspace loaded")
            if previews: terminal.snapshot(previews/"tui-overview.png")
            assert not (project/".go-flake-hunter").exists()
            terminal.resize(44, 16)
            terminal.expect("Resize to at least")
            terminal.resize(110, 34)
            terminal.expect("GO FLAKE HUNTER")
            terminal.send(b"\x1b[200~n\x1b[201~")
            terminal.expect("Clipboard text never runs commands")
            assert not (project/".go-flake-hunter").exists()
            terminal.send(b"n")
            terminal.expect("Scanning packages")
            terminal.send(b"q")
            terminal.expect("Stop the running task and exit?")
            terminal.send(b"n")
            terminal.expect("Scan: 1 confirmed")
            terminal.send(b"f")
            terminal.expect("Stored findings")
            terminal.send(b"/Victim\r")
            terminal.expect("filter: Victim")
            if previews: terminal.snapshot(previews/"tui-findings.png")
            terminal.send(b"r")
            terminal.expect("Witness reproduced in all three")
            terminal.send(b"fg")
            terminal.expect("Regression source preview")
            assert not list(project.glob("*_goflakehunter_test.go"))
            if previews: terminal.snapshot(previews/"tui-preview.png")
            terminal.send(b"w")
            terminal.expect("Write the previewed regression test?")
            terminal.send(b"\x1b[200~y\x1b[201~")
            terminal.expect("Clipboard text never runs commands")
            assert not list(project.glob("*_goflakehunter_test.go"))
            originals = {p: p.read_text() for p in project.glob("*.go")}
            for p, source in originals.items():
                p.write_text(source.replace("package orderbasic", "package orderbasic_test"))
            terminal.send(b"y")
            terminal.expect("regression source changed since preview")
            assert not list(project.glob("*_goflakehunter_test.go"))
            for p, source in originals.items():
                p.write_text(source)
            terminal.send(b"fg")
            terminal.expect("Regression source preview")
            terminal.send(b"w")
            terminal.expect("Write the previewed regression test?")
            terminal.send(b"y")
            terminal.expect("regression test written:")
            files = list(project.glob("*_goflakehunter_test.go"))
            assert len(files) == 1 and "//go:build goflakehunter" in files[0].read_text()
            terminal.send(b"q")
            terminal.finish()
        finally:
            terminal.close()

        clean = Path(scratch)/"clean-service"
        shutil.copytree(root/"testdata/fixtures/clean", clean)
        terminal = Terminal(binary, clean, ["tui", "--budget", "20000"])
        try:
            terminal.expect("Workspace loaded")
            terminal.send(b"n")
            terminal.expect("order /")
            terminal.send(b"\x03")
            terminal.expect("Stopped. Any partial")
            capsules = [json.loads(p.read_text()) for p in (clean/".go-flake-hunter").glob("gfh_*.json")]
            assert any(c["status"] == "partial" for c in capsules)
            assert not list(clean.glob("zz_goflakehunter_witness_*"))
            terminal.send(b"q")
            terminal.finish()
        finally:
            terminal.close()

        terminal = Terminal(binary, clean, [], no_color=True)
        try:
            terminal.expect("Workspace loaded")
            assert not re.search(rb"\x1b\[[0-9;]*m", terminal.capture)
            terminal.send(b"q")
            terminal.finish()
        finally:
            terminal.close()
    print("TUI smoke: startup, resize, safe paste, scan, replay, preview/write, cancellation, NO_COLOR and terminal restoration passed")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="./go-flake-hunter", type=Path)
    parser.add_argument("--preview-dir", type=Path)
    args = parser.parse_args()
    if args.preview_dir:
        args.preview_dir.mkdir(parents=True, exist_ok=True)
    run(args.binary.resolve(), args.preview_dir)

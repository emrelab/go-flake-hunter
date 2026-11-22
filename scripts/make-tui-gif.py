#!/usr/bin/env python3
"""Render docs/tui-demo.gif from a real TUI session over a PTY.

Same claim as make-demo-gif.py, one layer up: every glyph in the GIF is a cell
the binary drew. The session is driven with key bytes; the only synthetic part
is that the captured screens are replayed as an animation.

    python3 scripts/make-tui-gif.py --binary ./go-flake-hunter

Reuses the terminal harness and the ANSI renderer from tui-smoke.py instead of
duplicating them.
"""
import argparse
import importlib.util
import os
import re
import select
import shutil
import sys
import tempfile
import time
from pathlib import Path

from PIL import Image

ROOT = Path(__file__).resolve().parent.parent


def load_harness():
    sys.dont_write_bytecode = True  # importing the hyphenated script must not litter scripts/__pycache__
    spec = importlib.util.spec_from_file_location("tui_smoke", ROOT/"scripts/tui-smoke.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class Recorder:
    def __init__(self, harness, scratch):
        self.harness, self.scratch = harness, scratch
        self.frames, self.holds = [], []

    def pump(self, terminal, seconds):
        """Drain output until the TUI goes quiet, then keep the last screen."""
        hard_stop = time.monotonic() + 2
        end = time.monotonic() + seconds
        while time.monotonic() < min(end, hard_stop):
            if select.select([terminal.master], [], [], .05)[0]:
                terminal.capture.extend(os.read(terminal.master, 65536))
                end = time.monotonic() + .15   # a redraw is one burst; wait for silence
        self.add(terminal)

    def wait(self, terminal, text, timeout=180):
        """Expect against everything captured so far: pump() already drained it."""
        deadline = time.monotonic() + timeout
        while text.encode() not in terminal.capture:
            if terminal.process.poll() is not None or time.monotonic() > deadline:
                raise AssertionError(f"waiting for {text!r}")
            if select.select([terminal.master], [], [], .05)[0]:
                terminal.capture.extend(os.read(terminal.master, 65536))

    def add(self, terminal, hold=1):
        frame = bytes(terminal.capture).split(b"\x1b[H")[-1].decode("utf-8", "replace")
        # Never render half an escape sequence as literal text.
        frame = re.sub(r"\x1b(\[[0-9;?]*[ -/]*)?$", "", frame)
        path = Path(self.scratch)/f"frame{len(self.frames):03d}.png"
        self.harness.render(frame, path)
        with Image.open(path) as img:
            self.frames.append(img.convert("RGB").copy())
        self.holds.append(hold)


def run(binary, out):
    harness = load_harness()
    with tempfile.TemporaryDirectory(prefix="gfh-tui-gif-") as scratch:
        project = Path(scratch)/"checkout-service"
        shutil.copytree(ROOT/"testdata/fixtures/order-basic", project,
                        ignore=shutil.ignore_patterns(".go-flake-hunter", "*goflakehunter_test.go", "zz_goflakehunter_*"))
        terminal = harness.Terminal(binary, project, ["tui", "--short", "--budget", "100"])
        rec = Recorder(harness, scratch)
        try:
            terminal.expect("Workspace loaded")
            rec.add(terminal, hold=3)          # overview
            terminal.send(b"n")
            terminal.expect("Scanning packages")
            for _ in range(6):                 # scan in flight
                rec.pump(terminal, .35)
            rec.wait(terminal, "Scan: 1 confirmed")
            rec.add(terminal, hold=2)          # scan finished
            terminal.send(b"f")
            terminal.expect("Stored findings")
            rec.add(terminal, hold=2)          # findings
            for key in b"/Victim":
                terminal.send(bytes([key]))
                rec.pump(terminal, .12)
            terminal.send(b"\r")
            terminal.expect("filter: Victim")
            rec.add(terminal, hold=2)          # filtered
            terminal.send(b"r")
            terminal.expect("Witness reproduced in all three")
            rec.add(terminal, hold=3)          # replay result
            terminal.send(b"fg")
            terminal.expect("Regression source preview")
            rec.add(terminal, hold=4)          # preview is the payoff frame
            terminal.send(b"q")
            terminal.finish()
        finally:
            terminal.close()

    frames = [f.quantize(colors=24, method=Image.Quantize.MEDIANCUT) for f in rec.frames]
    frames[0].save(out, save_all=True, append_images=frames[1:],
                   duration=[h*250 for h in rec.holds], loop=0, optimize=True, disposal=1)
    print(f"make-tui-gif: {out} ({len(frames)} frames, {Path(out).stat().st_size} bytes)")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--binary", default="./go-flake-hunter", type=Path)
    ap.add_argument("--out", default=str(ROOT/"docs/tui-demo.gif"), type=Path)
    args = ap.parse_args()
    if not args.binary.exists():
        sys.exit(f"make-tui-gif: {args.binary} missing — run `make build` first")
    run(args.binary.resolve(), args.out)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

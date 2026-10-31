#!/usr/bin/env python3
"""Regression tests for the key-ring archive check in deploy/ops/common.sh: only the three regular files the backup
writes may reach tar's extraction as root. Run with `python3 -m unittest discover -s scripts`."""

import io
import os
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
COMMON = ROOT / "deploy" / "ops" / "common.sh"


def archive(path: Path, members: list[tuple[str, bytes, bytes]]) -> None:
    with tarfile.open(path, "w") as tar:
        for name, kind, payload in members:
            info = tarfile.TarInfo(name)
            if kind == b"file":
                info.size = len(payload)
                info.mode = 0o400
                tar.addfile(info, io.BytesIO(payload))
            else:
                info.type = tarfile.SYMTYPE if kind == b"symlink" else tarfile.LNKTYPE
                info.linkname = payload.decode()
                tar.addfile(info)


class KeysArchiveTest(unittest.TestCase):
    def check(self, path: Path) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["bash", "-c", f'. "{COMMON}"; validate_keys_archive "$1"', "_", str(path)],
            env={**os.environ, "PROGRAM": "test"},
            text=True,
            capture_output=True,
        )

    def test_the_backups_three_regular_files_pass(self) -> None:
        with tempfile.TemporaryDirectory() as work:
            path = Path(work) / "keys.tar"
            archive(path, [("master", b"file", b"m"), ("hash", b"file", b"h"), ("identity", b"file", b"i")])
            got = self.check(path)
            self.assertEqual(got.returncode, 0, got.stderr)

    def test_a_symlink_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as work:
            path = Path(work) / "keys.tar"
            archive(
                path,
                [
                    ("master", b"symlink", b"/etc/passwd"),
                    ("hash", b"file", b"h"),
                    ("identity", b"file", b"i"),
                ],
            )
            got = self.check(path)
            self.assertNotEqual(got.returncode, 0)
            self.assertIn("not a regular file", got.stderr)

    def test_busybox_style_hard_link_is_rejected(self) -> None:
        # Alpine's BusyBox tar lists a hard link as a regular mode followed by " -> target"; a type-character check
        # alone would let it through. The fake tar proves the validator rejects that listing without needing Docker.
        with tempfile.TemporaryDirectory() as work:
            bindir = Path(work) / "bin"
            bindir.mkdir()
            fake = bindir / "tar"
            fake.write_text(
                '#!/bin/sh\n'
                'printf "%s\\n" "-rw-rw-r-- ai/ai 0 2026-10-30 11:39:04 hash -> master"\n'
                'printf "%s\\n" "-rw-rw-r-- ai/ai 1 2026-10-30 11:39:04 master"\n'
                'printf "%s\\n" "-rw-rw-r-- ai/ai 1 2026-10-30 11:39:04 identity"\n'
            )
            fake.chmod(0o755)
            path = Path(work) / "keys.tar"
            path.write_bytes(b"")
            got = subprocess.run(
                ["bash", "-c", f'. "{COMMON}"; validate_keys_archive "$1"', "_", str(path)],
                env={**os.environ, "PROGRAM": "test", "PATH": f"{bindir}:{os.environ['PATH']}"},
                text=True,
                capture_output=True,
            )
            self.assertNotEqual(got.returncode, 0)
            self.assertIn("link", got.stderr)

    def test_a_hard_link_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as work:
            path = Path(work) / "keys.tar"
            archive(
                path,
                [
                    ("master", b"file", b"m"),
                    ("hash", b"hardlink", b"master"),
                    ("identity", b"file", b"i"),
                ],
            )
            got = self.check(path)
            self.assertNotEqual(got.returncode, 0)
            self.assertIn("not a regular file", got.stderr)

    def test_a_member_outside_the_three_names_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as work:
            path = Path(work) / "keys.tar"
            archive(
                path,
                [
                    ("master", b"file", b"m"),
                    ("hash", b"file", b"h"),
                    ("identity", b"file", b"i"),
                    ("../passwd", b"file", b"x"),
                ],
            )
            got = self.check(path)
            self.assertNotEqual(got.returncode, 0)
            self.assertIn("not just the key ring", got.stderr)

    def test_a_duplicate_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as work:
            path = Path(work) / "keys.tar"
            archive(
                path,
                [
                    ("master", b"file", b"m"),
                    ("master", b"file", b"m"),
                    ("hash", b"file", b"h"),
                    ("identity", b"file", b"i"),
                ],
            )
            got = self.check(path)
            self.assertNotEqual(got.returncode, 0)
            self.assertIn("twice", got.stderr)


if __name__ == "__main__":
    unittest.main()

import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from test_runner_host import BASH

SCRIPT = Path(__file__).parent / "ci" / "bench-compare.sh"


@unittest.skipUnless(BASH, "Bash is unavailable")
class BaseBenchmarkGate(unittest.TestCase):
    def test_train_inherits_base_fix_and_checks_gate_changes_with_both_versions(self):
        for change in ("unrelated", "gate", "module"):
            gate_changed = change != "unrelated"
            for base_status, head_status in ((0, 0), (13, 0), (0, 17)):
                with self.subTest(change=change, base_status=base_status, head_status=head_status), \
                        tempfile.TemporaryDirectory() as tmp:
                    root = Path(tmp)
                    def git(*args):
                        env = {**os.environ, "GIT_AUTHOR_NAME": "Fixture", "GIT_COMMITTER_NAME": "Fixture",
                               "GIT_AUTHOR_EMAIL": "fixture@example.invalid", "GIT_COMMITTER_EMAIL": "fixture@example.invalid"}
                        return subprocess.check_output(["git", "-C", str(root), *args], env=env, text=True).strip()
                    git("init", "-q")
                    (root / "tools" / "benchgate").mkdir(parents=True)
                    gate = root / "tools" / "benchgate" / "main.go"
                    gate.write_text("old gate")
                    (root / "tools" / "go.mod").write_text("base module")
                    git("add", "tools")
                    git("commit", "-qm", "old base")
                    old = git("rev-parse", "HEAD")
                    gate.write_text("fixed base gate")
                    git("commit", "-qam", "gate fix")
                    base = git("rev-parse", "HEAD")
                    git("checkout", "-q", "--detach", old)
                    if change == "gate":
                        (gate.parent / "proposed.go").write_text("proposed gate")
                    if change == "module":
                        (root / "tools" / "go.mod").write_text("head module")
                    (root / "change.go").write_text("head change")
                    git("add", "tools", "change.go")
                    git("commit", "-qm", "PR change without base fix")
                    pr = git("rev-parse", "HEAD")
                    git("checkout", "-q", "--detach", base)
                    git("merge", "--no-ff", "-qm", "assemble train", pr)
                    git("merge-base", "--is-ancestor", base, "HEAD")
                    self.assertEqual(gate.read_text(), "fixed base gate")
                    bin_dir = root / "bin"
                    bin_dir.mkdir()
                    fake_go = bin_dir / "go"
                    fake_go.write_text('''#!/usr/bin/env bash
set -eu
[ "$1" = run ]
[ "$2" = -C ]
[ "$4" = ./benchgate ]
[ "$5" = "$EXPECTED_ROOT" ]
[ "$6" = "$EXPECTED_BASE" ]
if [ -f "$3/benchgate/proposed.go" ]; then
  cat "$3/benchgate/proposed.go" >> "$LOG"
else
  cat "$3/benchgate/main.go" >> "$LOG"
fi
printf '\\n' >> "$LOG"
if [ "$3" = "$EXPECTED_ROOT/tools" ]; then exit "$HEAD_STATUS"; fi
[ "$(cat "$3/go.mod")" = 'base module' ]
exit "$BASE_STATUS"
''', newline="\n")
                    fake_go.chmod(0o755)
                    log = root / "calls"
                    env = {**os.environ, "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"],
                           "EXPECTED_ROOT": root.as_posix(), "EXPECTED_BASE": base, "LOG": log.as_posix(),
                           "BASE_STATUS": str(base_status), "HEAD_STATUS": str(head_status)}
                    result = subprocess.run([BASH, SCRIPT.resolve().as_posix(), base], cwd=root, env=env,
                                            capture_output=True, text=True)
                    self.assertEqual(result.returncode, (head_status or base_status) if gate_changed else base_status,
                                     result.stderr)
                    self.assertEqual(log.read_text().splitlines(),
                                     ["fixed base gate"] + (["proposed gate" if change == "gate" else "fixed base gate"] if gate_changed else []))

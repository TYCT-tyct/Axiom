import os
import re
import subprocess
import unittest
from pathlib import Path


BASH = r"D:\Git\bin\bash.exe" if os.name == "nt" else "bash"
SCRIPT = (Path(__file__).parent / "ci" / "runner-host.sh").read_text(encoding="utf-8")


class RunnerLabels(unittest.TestCase):
    def test_train_membership_is_decided_only_by_count(self):
        # Execute only the pure label selector, never the host installer or its system/API commands.
        selector = re.search(r"^runner_labels\(\).*?^}", SCRIPT, re.M | re.S).group()
        default = re.search(r"^train_runners=.*$", SCRIPT, re.M).group()
        for count, labels, want in (
            (None, "axiom,axiom-temp", [True] * 4 + [False] * 4),
            ("2", "axiom,axiom-temp", [True] * 2 + [False] * 6),
            ("0", "axiom,axiom-train,axiom-temp", [False] * 8),
            ("8", "axiom,axiom-train,axiom-temp", [True] * 8),
        ):
            with self.subTest(count=count, labels=labels):
                env = {**os.environ, "LABELS": labels}
                env.pop("TRAIN_RUNNERS", None)
                if count is not None:
                    env["TRAIN_RUNNERS"] = count
                result = subprocess.run(
                    [BASH, "-eu", "-c", default + '\nlabels=$LABELS\n' + selector +
                     '\nfor i in {1..8}; do runner_labels "$i"; done'],
                    env=env, text=True, capture_output=True, check=True,
                )
                rows = [line.split(",") for line in result.stdout.splitlines()]
                self.assertEqual(len(rows), 8)
                for row, train in zip(rows, want):
                    self.assertEqual(row, ["axiom", "axiom-temp"] + (["axiom-train"] if train else []))

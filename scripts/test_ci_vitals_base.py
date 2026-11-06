"""Run the vitals' base server script with every tool it calls replaced by a recorder."""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).parent
SCRIPT = SCRIPTS / "ci/vitals-base.sh"
SHIM = '''#!{python}
import json, os, pathlib, sys
command = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
if command == "python3":
    # What the base's web_cache would put into its build key from this environment.
    sys.path.insert(0, {scripts!r})
    import web_cache
    pathlib.Path(os.environ["KEY_INPUTS"]).write_text(json.dumps(list(web_cache.env_inputs())))
elif command == "just":
    print("axiom v0.0.0-test")
elif command == "go":
    server = pathlib.Path(args[args.index("-o") + 1])
    server.write_text("#!/bin/sh\\nexit 0\\n")
    server.chmod(0o755)
'''


@unittest.skipUnless(sys.platform == "linux", "the CI scripts run on Linux")
class VitalsBase(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for command in ("git", "pnpm", "just", "python3", "go"):
            shim = self.root / command
            shim.write_text(SHIM.format(python=sys.executable, scripts=str(SCRIPTS)), encoding="utf-8")
            shim.chmod(0o755)

    def key_inputs(self, port: str):
        out = self.root / f"inputs-{port}.json"
        env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ["PATH"], KEY_INPUTS=str(out),
                   AXIOM_VITALS_BASE_PORT=port, TMPDIR=str(self.root))
        result = subprocess.run(["bash", str(SCRIPT), "abc123", "localhost:1", "localhost:2"], env=env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(out.read_text())

    # The measuring run's ports must not reach the base's build key, or a base a runner built before is built again.
    def test_measuring_ports_leave_the_base_build_key_alone(self):
        first = self.key_inputs("25000")
        self.assertEqual(first, self.key_inputs("26000"))
        self.assertNotIn("AXIOM_VITALS_BASE_PORT", [name for name, _ in first])


if __name__ == "__main__":
    unittest.main()

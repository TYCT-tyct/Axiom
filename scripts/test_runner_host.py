import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


def bash_executable():
    if sys.platform != "win32":
        return shutil.which("bash")
    git = shutil.which("git")
    if git:
        directory = Path(git).resolve().parent
        for install in (directory.parent, directory.parent.parent):
            candidate = install / "bin" / "bash.exe"
            if candidate.is_file():
                return str(candidate)
    return None


BASH = bash_executable()
SCRIPT = (Path(__file__).parent / "ci" / "runner-host.sh").read_text(encoding="utf-8")
TRAIN_SETTINGS = re.search(r"^train_runners=.*?(?=^cpu_weight=)", SCRIPT, re.M | re.S).group()


def shell_function(name):
    # Copy only a function declaration, never source the installer's host-changing entry point.
    return re.search(rf"^{name}\(\).*?^}}", SCRIPT, re.M | re.S).group()


class BashDiscovery(unittest.TestCase):
    def test_windows_uses_git_installation_instead_of_wsl(self):
        with tempfile.TemporaryDirectory() as tmp:
            install = Path(tmp) / "Git For Windows"
            bash = install / "bin" / "bash.exe"
            bash.parent.mkdir(parents=True)
            bash.write_text("")
            for directory in ("cmd", "bin", "mingw64/bin"):
                git = install / directory / "git.exe"
                git.parent.mkdir(parents=True, exist_ok=True)
                git.write_text("")
                with self.subTest(directory=directory), mock.patch("sys.platform", "win32"), \
                        mock.patch("shutil.which", return_value=str(git)) as which:
                    self.assertEqual(bash_executable(), str(bash))
                    which.assert_called_once_with("git")

    def test_windows_without_git_or_its_bash_is_skippable(self):
        with tempfile.TemporaryDirectory() as tmp:
            git = Path(tmp) / "Git" / "cmd" / "git.exe"
            git.parent.mkdir(parents=True)
            git.write_text("")
            for executable in (None, str(git)):
                with self.subTest(git=executable), mock.patch("sys.platform", "win32"), \
                        mock.patch("shutil.which", return_value=executable) as which:
                    self.assertIsNone(bash_executable())
                    which.assert_called_once_with("git")

    def test_other_platforms_use_path(self):
        with mock.patch("sys.platform", "linux"), mock.patch("shutil.which", return_value="/bin/bash") as which:
            self.assertEqual(bash_executable(), "/bin/bash")
            which.assert_called_once_with("bash")


@unittest.skipUnless(BASH, "Bash from Git for Windows or PATH is unavailable")
class RunnerLabels(unittest.TestCase):
    def test_pool_membership_is_decided_only_by_count(self):
        # Execute only the pure label selector, never the host installer or its system/API commands.
        selector = shell_function("runner_labels")
        for train, only, labels in (
            (None, None, "axiom,axiom-temp"),                     # 4 train runners, the first 2 the train's alone
            ("2", "2", "axiom,axiom-temp"),                       # no runner shared
            ("0", "0", "axiom,axiom-train,axiom-temp"),           # no train pool: every runner serves PRs
            ("8", "8", "axiom,axiom-train,axiom-pr,axiom-temp"),  # every runner the train's alone
            ("4", "0", "axiom,axiom-pr,axiom-temp"),              # train runners all shared
        ):
            with self.subTest(train=train, only=only, labels=labels):
                env = {**os.environ, "LABELS": labels}
                for name, value in (("TRAIN_RUNNERS", train), ("TRAIN_ONLY", only)):
                    env.pop(name, None)
                    if value is not None:
                        env[name] = value
                result = subprocess.run(
                    [BASH, "-eu", "-c", TRAIN_SETTINGS + '\nlabels=$LABELS\n' + selector +
                     '\nfor i in {1..8}; do runner_labels "$i"; done'],
                    env=env, text=True, capture_output=True, check=True,
                )
                rows = [line.split(",") for line in result.stdout.splitlines()]
                self.assertEqual(len(rows), 8)
                t, o = int(train or 4), int(only or 2)
                for i, row in enumerate(rows, 1):
                    want = ["axiom", "axiom-temp"] + (["axiom-train"] if i <= t else []) + (["axiom-pr"] if i > o else [])
                    self.assertEqual(row, want)

    def test_train_only_must_fit_the_train_pool(self):
        for train, only in (("4", "5"), ("4", "-1"), ("4", "two")):
            with self.subTest(train=train, only=only):
                env = {**os.environ, "TRAIN_RUNNERS": train, "TRAIN_ONLY": only}
                result = subprocess.run([BASH, "-eu", "-c", TRAIN_SETTINGS], env=env, text=True, capture_output=True)
                self.assertEqual(result.returncode, 2)
                self.assertIn("TRAIN_ONLY", result.stderr)

    def test_invalid_count_stops_before_host_changes(self):
        for count in ("-1", "four", "4.5", "99999999999999999999999999999"):
            with self.subTest(count=count):
                result = subprocess.run(
                    [BASH, "-eu", "-c", TRAIN_SETTINGS], env={**os.environ, "TRAIN_RUNNERS": count},
                    text=True, capture_output=True,
                )
                self.assertEqual(result.returncode, 2)
                self.assertIn("TRAIN_RUNNERS must be a nonnegative integer", result.stderr)


CURL_STUB = r'''
curl() {
  printf '%s\0' "$@" > "$CURL_ARGV"
  : > "$CURL_HEADER"
  local argument line
  for argument in "$@"; do
    if [[ "$argument" == @* ]]; then
      while IFS= read -r line; do printf '%s\n' "$line"; done < "${argument#@}" > "$CURL_HEADER"
    fi
  done
  return "${CURL_STATUS:-0}"
}
'''


@unittest.skipUnless(BASH, "Bash from Git for Windows or PATH is unavailable")
class RunnerCredentials(unittest.TestCase):
    def run_api(self, command, status=0):
        with tempfile.TemporaryDirectory() as tmp:
            argv, header = Path(tmp) / "argv", Path(tmp) / "header"
            env = {**os.environ, "GH_TOKEN": "test-admin-argument-marker",
                   "RUNNER_TOKEN": "test-registration-argument-marker",
                   "CURL_ARGV": argv.as_posix(), "CURL_HEADER": header.as_posix(), "CURL_STATUS": str(status)}
            result = subprocess.run([BASH, "-eu", "-c", CURL_STUB + shell_function("runner_api") + "\n" + command],
                                    env=env, text=True, capture_output=True)
            return result, argv.read_bytes().split(b"\0")[:-1], header.read_text(encoding="utf-8")

    def test_api_token_is_only_in_the_header_pipe(self):
        for method, path, extra in (("GET", "", ""), ("GET", "/123/labels", ""),
                                    ("PUT", "/123/labels", " --data '{}'")):
            with self.subTest(method=method, path=path):
                result, argv, header = self.run_api(f'runner_api {method} "{path}"{extra}')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertFalse(any(b"test-admin-argument-marker" in value for value in argv))
                self.assertEqual(header, "Authorization: Bearer test-admin-argument-marker\n")
                self.assertIn(method.encode(), argv)
                self.assertIn(f"https://api.github.com/repos/TYCT-tyct/Axiom/actions/runners{path}".encode(), argv)

    def test_denied_preflight_stops_before_host_setup(self):
        preflight = re.search(r"^install\(\) \{\n(.*?)(?=^  command -v docker)", SCRIPT, re.M | re.S).group(1)
        for status in (0, 22):
            with self.subTest(status=status):
                result, argv, header = self.run_api(preflight + "\nprintf 'host setup would start'", status)
                self.assertEqual(result.returncode, status, result.stderr)
                self.assertEqual(bool(result.stdout), status == 0)
                self.assertIn(b"https://api.github.com/repos/TYCT-tyct/Axiom/actions/runners", argv)
                self.assertEqual(header, "Authorization: Bearer test-admin-argument-marker\n")

    def test_registration_token_is_in_environment_not_sudo_argv(self):
        registration = re.search(r'^    \(cd "\$dir" &&.*?\)\n', SCRIPT, re.M | re.S).group()
        sudo = r'''
sudo() {
  "$TEST_PYTHON" -c 'import json, os, sys; print(json.dumps({"argv": sys.argv[1:], "token": os.getenv("ACTIONS_RUNNER_INPUT_TOKEN")}))' "$@"
}
'''
        with tempfile.TemporaryDirectory() as tmp:
            env = {**os.environ, "RUNNER_TOKEN": "test-registration-argument-marker", "TEST_PYTHON": sys.executable,
                   "dir": Path(tmp).as_posix(), "user": "test-user", "node_bin": "/node", "toolcache": "/tools",
                   "go_version": "test", "repo_url": "https://example.invalid/repo", "name": "test", "desired": "test"}
            result = subprocess.run([BASH, "-eu", "-c", sudo + shell_function("as_user") + "\n" + registration],
                                    env=env, text=True, capture_output=True, check=True)
            record = json.loads(result.stdout)
            self.assertEqual(record["token"], env["RUNNER_TOKEN"])
            self.assertNotIn(env["RUNNER_TOKEN"], " ".join(record["argv"]))
            self.assertNotIn("--token", record["argv"])
            self.assertIn("--preserve-env=ACTIONS_RUNNER_INPUT_TOKEN", record["argv"])

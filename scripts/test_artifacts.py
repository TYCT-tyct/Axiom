import unittest

import check_artifacts
import justrepo

LIMIT = check_artifacts.LIMIT


def artifacts(main_files, branch_files, origin_main=True, uncommitted=None, stage=False):
    """Run the real `just artifacts`: main_files committed as origin/main, then branch_files as this branch.

    Without origin_main the repository has no origin/main, as a partial clone would not, so every file counts.
    uncommitted files are written after the last commit, and staged when stage is true.
    """
    with justrepo.repository() as repo:
        justrepo.commit(repo, main_files)
        if origin_main:
            justrepo.git(repo, "update-ref", "refs/remotes/origin/main", "HEAD")
        if branch_files:
            justrepo.commit(repo, branch_files)
        if uncommitted:
            justrepo.write(repo, uncommitted)
            if stage:
                justrepo.git(repo, "add", "--", *uncommitted)
        return justrepo.just(repo, "artifacts")


class Artifacts(unittest.TestCase):
    def assert_refused(self, result, refused, why):
        self.assertEqual(result.returncode != 0, refused is not None, result.stdout)
        if refused:
            found = next((line for line in result.stdout.splitlines() if line.startswith(f"{refused}: ")), "")
            self.assertIn(why, found)

    def test_cases(self):
        cases = [
            # name, files on origin/main, files this branch adds or changes, origin/main exists, refused, why
            ("a build output added on the branch", {"note.txt": "kept\n"}, {"axiom.exe": "MZ"}, True, "axiom.exe",
             "a build output, archive or binary asset"),
            ("a suffix in another case", {"note.txt": "kept\n"}, {"AXIOM.EXE": "MZ"}, True, "AXIOM.EXE",
             "a build output, archive or binary asset"),
            ("a binary asset by its suffix, holding text", {"note.txt": "kept\n"}, {"logo.png": "not a png"}, True,
             "logo.png", "a build output, archive or binary asset"),
            ("a file over 1 MB", {"note.txt": "kept\n"}, {"blob": "a" * (LIMIT + 1)}, True, "blob",
             "bytes is over the 1 MB limit"),
            ("a file of exactly 1 MB", {"note.txt": "kept\n"}, {"blob": "a" * LIMIT}, True, None, ""),
            ("binary content a suffix does not announce", {"note.txt": "kept\n"}, {"payload.json": '{"a": \0}'}, True,
             "payload.json", "binary content"),
            # what the branch does not change is not the branch's problem
            ("a large file already on main", {"old.bin": "a" * (LIMIT + 1), "note.txt": "kept\n"},
             {"note.txt": "changed\n"}, True, None, ""),
            ("a large file the branch deletes", {"old.bin": "a" * (LIMIT + 1)}, {"old.bin": None}, True, None, ""),
            # main, and a clone without origin/main, check every tracked file
            ("main", {"old.bin": "a" * (LIMIT + 1)}, {}, True, "old.bin", "bytes is over the 1 MB limit"),
            ("without origin/main", {"old.bin": "a" * (LIMIT + 1)}, {}, False, "old.bin", "bytes is over the 1 MB limit"),
            # the allowlist, and a file with nothing wrong with it
            ("a recorded sample", {"note.txt": "kept\n"},
             {"internal/protocol/testdata/plant/upstream.sse": "a" * (LIMIT + 1) + "\0"}, True, None, ""),
            ("a contract's generated server", {"note.txt": "kept\n"},
             {"internal/platform/adminapi/adminapi.gen.go": "a" * (LIMIT + 1)}, True, None, ""),
            ("a hand-written file beside it", {"note.txt": "kept\n"},
             {"internal/platform/adminapi/big.go": "a" * (LIMIT + 1)}, True, "internal/platform/adminapi/big.go",
             "bytes is over the 1 MB limit"),
            ("a source file", {"note.txt": "kept\n"}, {"internal/plant/plant.go": "package plant\n"}, True, None, ""),
        ]
        for name, main, branch, origin_main, refused, why in cases:
            with self.subTest(name):
                self.assert_refused(artifacts(main, branch, origin_main), refused, why)

    def test_what_a_commit_would_take(self):
        # the branch has a commit, and the worktree holds what the next one would take: staged, unstaged or untracked
        cases = [
            # name, files on origin/main, files this branch commits, files left in the worktree, staged, refused, why
            ("a staged 2 MB file", {"note.txt": "kept\n"}, {"branch.txt": "committed\n"},
             {"blob": "a" * (2 * LIMIT)}, True, "blob", "2097152 bytes is over the 1 MB limit"),
            ("an unstaged change to a file untouched by the branch", {"note.txt": "kept\n"},
             {"branch.txt": "committed\n"},
             {"note.txt": "a" * (LIMIT + 1)}, False, "note.txt", "bytes is over the 1 MB limit"),
            ("an untracked file over 1 MB", {"note.txt": "kept\n"}, {"note.txt": "committed\n"},
             {"blob": "a" * (LIMIT + 1)}, False, "blob", "bytes is over the 1 MB limit"),
            # .gitignore is what keeps a local build out of the check: an ignored file is never committed
            ("an ignored build output", {".gitignore": "*.exe\n", "note.txt": "kept\n"}, {"note.txt": "committed\n"},
             {"axiom.exe": "MZ"}, False, None, ""),
        ]
        for name, main, branch, uncommitted, stage, refused, why in cases:
            with self.subTest(name):
                self.assert_refused(artifacts(main, branch, uncommitted=uncommitted, stage=stage), refused, why)

    def test_a_git_failure_fails(self):
        self.assertNotEqual(justrepo.just_with_broken_git("artifacts").returncode, 0)


if __name__ == "__main__":
    unittest.main()

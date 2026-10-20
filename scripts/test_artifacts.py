import unittest

import check_artifacts
import justrepo

LIMIT = check_artifacts.LIMIT


def artifacts(main_files, branch_files, origin_main=True):
    """Run the real `just artifacts`: main_files committed as origin/main, then branch_files as this branch.

    Without origin_main the repository has no origin/main, as a partial clone would not, so every tracked file counts.
    """
    with justrepo.repository() as repo:
        justrepo.commit(repo, main_files)
        if origin_main:
            justrepo.git(repo, "update-ref", "refs/remotes/origin/main", "HEAD")
        if branch_files:
            justrepo.commit(repo, branch_files)
        return justrepo.just(repo, "artifacts")


class Artifacts(unittest.TestCase):
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
            ("a source file", {"note.txt": "kept\n"}, {"internal/plant/plant.go": "package plant\n"}, True, None, ""),
        ]
        for name, main, branch, origin_main, refused, why in cases:
            with self.subTest(name):
                result = artifacts(main, branch, origin_main)
                self.assertEqual(result.returncode != 0, refused is not None, result.stdout)
                if refused:
                    found = next((line for line in result.stdout.splitlines() if line.startswith(f"{refused}: ")), "")
                    self.assertIn(why, found)

    def test_a_git_failure_fails(self):
        self.assertNotEqual(justrepo.just_with_broken_git("artifacts").returncode, 0)


if __name__ == "__main__":
    unittest.main()

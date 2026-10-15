import unittest

import justrepo


def scope(files, commit):
    """Run the real `just scope` in a fresh repository holding files ({path: content}), committed or left untracked."""
    with justrepo.repository() as repo:
        if commit:
            justrepo.commit(repo, files)
        else:
            justrepo.write(repo, files)
        return justrepo.just(repo, "scope")


class Scope(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("escaped quotes around the dot in Go", "internal/plant/plant.go",
             'package plant\n\nconst q = "SET LOCAL \\"axiom\\" . \\"scope_platform\\" = \'on\'"\n', False, True),
            ("a line break inside the name in SQL, in another case", "internal/plant/plant.sql",
             "SET LOCAL axiom\n. SCOPE_Sites = '1';\n", False, True),
            ("set_config in any case", "cmd/plant/main.go", 'package main\n\nconst q = "SELECT Set_Config($1, $2, true)"\n',
             False, True),
            ("a committed file", "internal/plant/plant.sql", "SET LOCAL axiom.scope_accounts = '1';\n", True, True),
            ("a role switch in a test", "internal/plant/plant_test.go",
             'package plant\n\nconst q = "set role axiom_owner"\n', False, True),
            ("a quoted local role in a migration", "internal/plant/migrations/00001_plant.sql",
             "SET  LOCAL \"role\" = 'axiom_owner';\n", False, True),
            ("a role quoted with Go escapes", "internal/plant/plant.go",
             'package plant\n\nconst q = "SET \\"role\\" = \'axiom_owner\'"\n', False, True),
            ("a role reset quoted with Go escapes", "internal/plant/plant.go",
             'package plant\n\nconst q = "RESET \\"role\\""\n', False, True),
            ("a quoted role in a raw Go string", "internal/plant/plant.go",
             'package plant\n\nconst q = `SET "role" TO axiom_owner`\n', False, True),
            ("a session role", "internal/plant/plant.sql", "SET SESSION ROLE axiom_owner;\n", False, True),
            ("a role reset", "cmd/plant/main.go", 'package main\n\nconst q = "RESET ROLE"\n', False, True),
            ("names that only contain set and role", "internal/plant/plant.go",
             'package plant\n\n// SetRole resets roles.\nconst q = "SELECT 1 OFFSET role"\n', False, False),
            ("the pg package may", "internal/platform/pg/scope.sql",
             "SELECT set_config('axiom.scope_accounts', '1', true);\n", False, False),
            ("other file types are not code", "docs/plant.md", "axiom.scope_platform\n", False, False),
        ]
        for name, path, content, commit, fails in cases:
            with self.subTest(name):
                result = scope({path: content}, commit)
                self.assertEqual(result.returncode != 0, fails, result.stdout)
                if fails:
                    self.assertIn(path, result.stdout)


if __name__ == "__main__":
    unittest.main()

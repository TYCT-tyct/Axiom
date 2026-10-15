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
            ("a line break between set and role", "internal/plant/plant.sql", "SET\nROLE axiom_owner;\n", False, True),
            ("a line break after local", "internal/plant/plant.sql", "SET LOCAL\n  ROLE axiom_owner;\n", False, True),
            ("a line break inside a role reset", "internal/plant/plant.sql", "RESET\n\tROLE;\n", False, True),
            ("a block comment between set and role", "internal/plant/plant.sql", "SET/**/ROLE axiom_owner;\n", False, True),
            ("a line comment between set and role", "internal/plant/plant.sql", "SET -- as owner\nROLE axiom_owner;\n", False, True),
            ("a nested block comment", "internal/plant/plant.sql", "SET /* a /* b */ c */ ROLE axiom_owner;\n", False, True),
            ("comment markers inside quotes do not hide the statement", "internal/plant/plant.sql",
             "SELECT '/*'; SET /* c */ ROLE axiom_owner; SELECT '*/';\n", False, True),
            ("a comment opener inside a quoted identifier", "internal/plant/plant.sql",
             'SELECT "--"; SET\nROLE axiom_owner;\n', False, True),
            ("a Go string with escaped line breaks", "internal/plant/plant.go",
             'package plant\n\nconst q = "SET\\nLOCAL\\tROLE axiom_owner"\n', False, True),
            ("a Go raw string with a line break", "internal/plant/plant.go",
             'package plant\n\nconst q = `SET\n\tROLE axiom_owner`\n', False, True),
            ("a Go string with a comment between set and role", "internal/plant/plant.go",
             'package plant\n\nconst q = "SET /* c */ ROLE axiom_owner"\n', False, True),
            ("a Go string with a line comment, after a rune literal holding a quote", "internal/plant/plant.go",
             'package plant\n\nvar r, q = \'"\', "SET -- c\\nROLE axiom_owner"\n', False, True),
            ("a quote inside a Go block comment", "internal/plant/plant.go",
             'package plant\n\n/* " */ var q = "SET\\nROLE axiom_owner"\n', False, True),
            ("a backtick inside a Go line comment", "internal/plant/plant.go",
             'package plant\n\n// a ` in a comment\nconst q = `SET\nROLE axiom_owner`\n', False, True),
            ("a role reset split by a Go escape", "internal/plant/plant.go",
             'package plant\n\nconst q = "RESET\\nROLE"\n', False, True),
            ("names that only contain set and role", "internal/plant/plant.go",
             'package plant\n\n// SetRole resets roles.\nconst q = "SELECT 1 OFFSET role"\n', False, False),
            ("set and role in separate statements", "internal/plant/plant.sql",
             "SET search_path = a;\nALTER ROLE b SET statement_timeout = 1;\nSELECT 1 OFFSET\nROLE;\n", False, False),
            ("set and role in separate comments", "internal/plant/plant.sql", "-- SET\n-- ROLE x\n/* RESET */ /* ROLE */\n", False, False),
            ("set and role in separate Go comments", "internal/plant/plant.go",
             'package plant\n\n// Never SET\n// ROLE here; /* RESET ROLE */ is not either.\nconst q = "SELECT 1"\n', False, False),
            ("a Go string that ends before role", "internal/plant/plant.go",
             'package plant\n\nconst q, r = "SET", "ROLE"\n', False, False),
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

    def test_a_git_failure_fails(self):
        self.assertNotEqual(justrepo.just_with_broken_git("scope").returncode, 0)


if __name__ == "__main__":
    unittest.main()

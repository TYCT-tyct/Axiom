import unittest

from migration_safety import problems

UP = "-- +goose Up\n"
NO_TX = "-- +goose NO TRANSACTION\n" + UP


class MigrationSafety(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("index on a table the file creates", UP + "CREATE TABLE a.t (k int);\nCREATE INDEX t_k ON a.t (k);", 0),
            ("index on an existing table", UP + "CREATE UNIQUE INDEX IF NOT EXISTS t_k ON a.t (k);", 1),
            ("concurrent index outside a transaction", NO_TX + "CREATE INDEX CONCURRENTLY IF NOT EXISTS t_k ON a.t (k);", 0),
            ("concurrent index in a transaction", UP + "CREATE INDEX CONCURRENTLY t_k ON a.t (k);", 1),
            ("parent index of a partitioned table", UP + "CREATE INDEX p_k ON ONLY a.p (k);", 0),
            ("constraint without NOT VALID", UP + "ALTER TABLE a.t ADD CONSTRAINT k_positive CHECK (k > 0);", 1),
            ("foreign key without NOT VALID among other actions",
             UP + "ALTER TABLE a.t ADD COLUMN u int, ADD CONSTRAINT t_u FOREIGN KEY (u) REFERENCES a.u (id);", 1),
            ("foreign key NOT VALID", UP + "ALTER TABLE a.t ADD CONSTRAINT t_u FOREIGN KEY (u) REFERENCES a.u (id) NOT VALID;", 0),
            ("validated in the transaction that added it",
             UP + "ALTER TABLE a.t ADD CONSTRAINT c CHECK (k > 0) NOT VALID;\nALTER TABLE a.t VALIDATE CONSTRAINT c;", 1),
            ("validated in a later migration", UP + "ALTER TABLE a.t VALIDATE CONSTRAINT c;", 0),
            ("backfill of an existing table", UP + "UPDATE a.t SET k = 0 WHERE k IS NULL;", 1),
            ("copy into an existing table", UP + "INSERT INTO a.t (k) SELECT k FROM a.u;", 1),
            ("rows written as values", UP + "INSERT INTO a.t (k) VALUES (1) ON CONFLICT DO NOTHING;", 0),
            ("backfill of a table the file creates", UP + "CREATE TABLE a.n (k int);\nINSERT INTO a.n SELECT k FROM a.t;", 0),
            ("backfill outside a transaction", NO_TX + "UPDATE a.t SET k = 0;", 0),
            ("waived with a reason", UP + "-- migration-safety: one row per site\nUPDATE a.sites SET k = 0;", 0),
            ("a waiver without a reason", UP + "-- migration-safety:\nUPDATE a.sites SET k = 0;", 1),
            ("statements in comments, strings and function bodies",
             UP + "-- UPDATE a.t SET k = 0;\nCOMMENT ON TABLE a.t IS 'see; UPDATE a.t SET k = 0';\n"
                  "CREATE FUNCTION a.f() RETURNS void LANGUAGE sql AS $$ UPDATE a.t SET k = 0 $$;", 0),
            ("the Down section", UP + "SELECT 1;\n-- +goose Down\nCREATE INDEX t_k ON a.t (k);", 0),
        ]
        for name, text, want in cases:
            with self.subTest(name):
                self.assertEqual(len(problems(text)), want, problems(text))


if __name__ == "__main__":
    unittest.main()

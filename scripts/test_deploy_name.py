import hashlib
import unittest

import deploy_name


def digest(workspace):
    return hashlib.sha256(workspace.lower().encode()).hexdigest()[: deploy_name.DIGEST]


class Name(unittest.TestCase):
    def test_sanitises_the_runner_and_hashes_the_workspace(self):
        workspace = "/home/runner/_work/Axiom/Axiom"
        self.assertEqual(deploy_name.name("Axiom 1", workspace), "axiom-1-" + digest(workspace))

    def test_workspace_case_does_not_change_the_name(self):
        self.assertEqual(deploy_name.name("axiom-1", "/A/B"), deploy_name.name("axiom-1", "/a/b"))

    def test_a_runner_name_that_sanitises_to_nothing_falls_back(self):
        self.assertTrue(deploy_name.name("///", "/w").startswith("local-"))

    def test_same_runner_name_in_another_workspace_gets_another_name(self):
        # Two repositories' runners can share a name; their workspaces differ, so their projects must too.
        self.assertNotEqual(deploy_name.name("axiom-1", "/home/runner/axiom-1/work"),
                            deploy_name.name("axiom-1", "/home/runner/other/work"))

    def test_names_that_sanitise_alike_stay_apart(self):
        self.assertNotEqual(deploy_name.name("a.b", "/home/runner/a.b/work"),
                            deploy_name.name("a-b", "/home/runner/a-b/work"))

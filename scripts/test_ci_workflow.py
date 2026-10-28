import re
import unittest
from pathlib import Path


WORKFLOW = (Path(__file__).parents[1] / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")


def job(name):
    return re.search(rf"^  {name}:\n(.*?)(?=^  [\w-]+:|\Z)", WORKFLOW, re.M | re.S).group(1)


def enabled(name, event, stress=False, draft=False, docs=False):
    condition = re.search(r"^    if: (.+)$", job(name), re.M).group(1)
    values = {
        "always()": True,
        "github.event_name": event,
        "github.event.pull_request.draft": draft,
        "inputs.gate_stress": stress,
        "needs.changes.result": "success",
        "needs.changes.outputs.docs_only": "true" if docs else "false",
    }
    for key, value in values.items():
        condition = condition.replace(key, repr(value))
    condition = condition.replace("&&", " and ").replace("||", " or ")
    condition = re.sub(r"!(?!=)", " not ", condition)
    return eval(condition, {"__builtins__": {}}, {})


class BuildChecks(unittest.TestCase):
    def test_normal_events_publish_the_literal_build_check(self):
        self.assertRegex(job("build"), r"(?m)^    name: build$")
        for event in ("pull_request", "push", "workflow_dispatch"):
            for docs in (False, True):
                with self.subTest(event=event, docs=docs):
                    self.assertTrue(enabled("build", event, docs=docs))
                    self.assertFalse(enabled("gate-stress", event, docs=docs))
        self.assertFalse(enabled("build", "pull_request", draft=True))

    def test_diagnostic_cannot_pass_or_skip_the_required_build(self):
        self.assertTrue(enabled("gate-stress", "workflow_dispatch", stress=True))
        self.assertTrue(enabled("web", "workflow_dispatch", stress=True, docs=True))
        self.assertTrue(enabled("build", "workflow_dispatch", stress=True))
        # A skipped required check is accepted by GitHub; the diagnostic must explicitly fail it instead.
        self.assertRegex(job("build"), r"(?s)- if: inputs.gate_stress\n        run: \|\n.*?\n          exit 1")
        self.assertFalse(enabled("gate-stress", "pull_request", stress=True))

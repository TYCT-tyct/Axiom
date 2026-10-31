import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).parents[1]


@unittest.skipUnless(shutil.which("docker"), "Docker Compose is unavailable")
class DeploymentEnvironment(unittest.TestCase):
    def test_exchange_source_from_deployment_env_file_reaches_the_node(self):
        for source in (None, "", "ecb"):
            with self.subTest(source=source), tempfile.TemporaryDirectory() as tmp:
                env_file = Path(tmp) / "deployment.env"
                settings = ["AXIOM_POSTGRES_PASSWORD=fixture", "AXIOM_APP_PASSWORD=fixture",
                            "AXIOM_MIGRATOR_PASSWORD=fixture"]
                if source is not None:
                    settings.append(f"AXIOM_EXCHANGE_SOURCE={source}")
                env_file.write_text("\n".join(settings) + "\n")
                env = {key: value for key, value in os.environ.items()
                       if key != "AXIOM_EXCHANGE_SOURCE"}
                result = subprocess.run(["docker", "compose", "-f", str(ROOT / "deploy" / "compose.yaml"),
                                         "--env-file", str(env_file), "config", "--format", "json"],
                                        env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                node_env = json.loads(result.stdout)["services"]["axiom"]["environment"]
                self.assertEqual(node_env.get("AXIOM_EXCHANGE_SOURCE"), source or "")

    def test_edge_admin_sources_reach_the_node_and_default_to_loopback(self):
        required = ["AXIOM_POSTGRES_PASSWORD=fixture", "AXIOM_APP_PASSWORD=fixture",
                    "AXIOM_MIGRATOR_PASSWORD=fixture"]
        with tempfile.TemporaryDirectory() as tmp:
            env_file = Path(tmp) / "deployment.env"
            for value in (None, "203.0.113.0/24"):
                with self.subTest(value=value):
                    settings = list(required)
                    if value is not None:
                        settings.append(f"AXIOM_ADMIN_SOURCES={value}")
                    env_file.write_text("\n".join(settings) + "\n")
                    env = {key: value for key, value in os.environ.items()
                           if key != "AXIOM_ADMIN_SOURCES"}
                    env.update({
                        "AXIOM_EDGE_CONSOLE_HOST": "console.example",
                        "AXIOM_EDGE_API_HOST": "api.example",
                        "AXIOM_EDGE_ADMIN_HOST": "admin.example",
                    })
                    result = subprocess.run(
                        ["docker", "compose", "-f", str(ROOT / "deploy" / "compose.yaml"),
                         "-f", str(ROOT / "deploy" / "compose.edge.yaml"),
                         "--env-file", str(env_file), "config", "--format", "json"],
                        env=env, capture_output=True, text=True)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    services = json.loads(result.stdout)["services"]
                    self.assertEqual(services["axiom"]["environment"].get("AXIOM_ADMIN_SOURCES"),
                                     value or "127.0.0.1/32,::1/128")
                    self.assertNotIn("AXIOM_ADMIN_SOURCES", services["caddy"].get("environment", {}))

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

    def test_supported_settings_reach_the_node_and_the_operations_listener_stays_off(self):
        settings = {"AXIOM_MODEL_VERIFICATION": "off", "AXIOM_DIRECT_FORMATS": "on", "AXIOM_BYOK": "on",
                    "AXIOM_TRUSTED_PROXIES": "10.0.0.1", "AXIOM_STARTUP_TIMEOUT": "2m",
                    "AXIOM_DB_COMPLETION_RESERVE": "8", "AXIOM_HASH_CONCURRENCY": "2", "AXIOM_HASH_WAIT": "1s",
                    "AXIOM_CORE_NODES": "2", "AXIOM_NODE_ID": "core-a", "AXIOM_OPERATIONS_ADDR": ":8084",
                    "AXIOM_OPERATIONS_SOURCES": "10.0.0.0/8", "AXIOM_SMTP_HOST": "smtp.example",
                    "AXIOM_SMTP_PORT": "465", "AXIOM_SMTP_USERNAME": "mailer", "AXIOM_SMTP_PASSWORD": "fixture",
                    "AXIOM_SMTP_FROM": "no-reply@example.com", "AXIOM_SEARCH_URL": "https://search.example",
                    "AXIOM_SEARCH_KEY": "fixture", "AXIOM_FILE_PARSER_URL": "https://parse.example",
                    "AXIOM_FILE_PARSER_KEY": "fixture"}
        required = ["AXIOM_POSTGRES_PASSWORD=fixture", "AXIOM_APP_PASSWORD=fixture", "AXIOM_MIGRATOR_PASSWORD=fixture"]
        env = {key: value for key, value in os.environ.items() if key not in settings}
        with tempfile.TemporaryDirectory() as tmp:
            env_file = Path(tmp) / "deployment.env"
            for given in (settings, {}):
                with self.subTest(set=bool(given)):
                    env_file.write_text("\n".join(required + [f"{k}={v}" for k, v in given.items()]) + "\n")
                    result = subprocess.run(["docker", "compose", "-f", str(ROOT / "deploy" / "compose.yaml"),
                                             "--env-file", str(env_file), "config", "--format", "json"],
                                            env=env, capture_output=True, text=True)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    node_env = json.loads(result.stdout)["services"]["axiom"]["environment"]
                    # Unset reaches the node as empty, which the program reads as its default: the listener off.
                    self.assertEqual({name: node_env.get(name) for name in settings},
                                     given or dict.fromkeys(settings, ""))

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

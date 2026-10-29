import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("registry", Path(__file__).resolve().parents[1] / "docker-internal-registry.py")
registry = importlib.util.module_from_spec(spec)
spec.loader.exec_module(registry)


class RegistryTests(unittest.TestCase):
    def test_requires_explicit_http_denial(self):
        for message in ["StatusCode: 403 Forbidden", "invalid status code from registry 401 (Unauthorized)",
                        "unexpected status: 403", "HTTP/1.1 401 Unauthorized", "status code: 401"]:
            with self.subTest(message=message):
                registry.require_denied(subprocess.CompletedProcess([], 1, "", message))

    def test_network_and_other_failures_are_not_privacy_evidence(self):
        for message in ["connection refused", "TLS handshake timeout", "no such host", "unauthorized",
                        "denied", "404 Not Found", "500 Internal Server Error", "timeout after 401ms", ""]:
            with self.subTest(message=message):
                with self.assertRaises(RuntimeError):
                    registry.require_denied(subprocess.CompletedProcess([], 1, "", message))
        with self.assertRaises(RuntimeError):
            registry.require_denied(subprocess.CompletedProcess([], 0, "{}", "401 Unauthorized"))

    def test_exact_multiarch_index(self):
        raw = json.dumps({"manifests": [{"platform": {"os": "linux", "architecture": arch}}
                                        for arch in ["amd64", "arm64"]]}).encode()
        self.assertRegex(registry.check_manifest(raw, raw), r"^sha256:[a-f0-9]{64}$")
        with self.assertRaises(RuntimeError):
            registry.check_manifest(raw, raw + b"\n")
        single = json.dumps({"manifests": [{"platform": {"os": "linux", "architecture": "amd64"}}]}).encode()
        with self.assertRaises(RuntimeError):
            registry.check_manifest(single, single)

    def test_roundtrip_and_fail_closed_execution(self):
        raw = json.dumps({"manifests": [{"platform": {"os": "linux", "architecture": arch}}
                                        for arch in ["amd64", "arm64"]]}).encode()
        for denial in ["403 Forbidden", "TLS handshake timeout", ""]:
            calls = []

            def output(args, **kwargs):
                if args[0] == "skopeo":
                    return raw
                return kwargs["env"]["DOCKER_DEFAULT_PLATFORM"].split("/")[1] + "\n"

            def run(args, **kwargs):
                calls.append((args, kwargs))
                if args[0] == "skopeo":
                    auth = Path(args[args.index("--authfile") + 1])
                    self.assertEqual(json.loads(auth.read_text()), {"auths": {}})
                    self.assertEqual(kwargs["env"]["DOCKER_CONFIG"], str(auth.parent))
                    return subprocess.CompletedProcess(args, 1 if denial else 0, "", denial)
                return subprocess.CompletedProcess(args, 0)

            with self.subTest(denial=denial), tempfile.TemporaryDirectory() as temp:
                authfile = Path(temp) / "auth.json"
                authfile.write_text('{"auths":{"ghcr.io":{}}}')
                with patch.object(registry.subprocess, "check_output", side_effect=output), patch.object(registry.subprocess, "run", side_effect=run):
                    if denial == "403 Forbidden":
                        registry.main("ghcr.io/xmancodeing/xmall-internal-private", "internal-test", str(authfile), "test.tar")
                        pulls = [args for args, _ in calls if args[:2] == ["docker", "pull"]]
                        self.assertEqual([args[3] for args in pulls], ["linux/amd64", "linux/arm64"])
                        self.assertTrue(all("@sha256:" in args[4] for args in pulls))
                        self.assertEqual(len([args for args, _ in calls if args[0] == registry.sys.executable]), 2)
                    else:
                        with self.assertRaises(RuntimeError):
                            registry.main("ghcr.io/xmancodeing/xmall-internal-private", "internal-test", str(authfile), "test.tar")
                        self.assertEqual(len(calls), 1)

    def test_registry_workflow_uses_isolated_auth_digest_and_both_runtime_arches(self):
        source = Path(registry.__file__).read_text()
        self.assertIn('"REGISTRY_AUTH_FILE": str(empty_auth)', source)
        self.assertIn('"--authfile", str(empty_auth)', source)
        self.assertIn('reference = f"{image}@{digest}"', source)
        self.assertIn('for arch in ["amd64", "arm64"]', source)
        self.assertIn('"DOCKER_DEFAULT_PLATFORM": f"linux/{arch}"', source)
        self.assertIn('["docker", "pull", "--platform", f"linux/{arch}", reference]', source)
        self.assertIn('with_name("docker-smoke.py")', source)


if __name__ == "__main__":
    unittest.main()

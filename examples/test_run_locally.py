"""Regression tests for local-runner configuration path resolution."""

import importlib.util
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "cassandra_local_runner", Path(__file__).with_name("run_locally.py")
)
assert SPEC is not None and SPEC.loader is not None
RUNNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER)


class ConfigPathTests(unittest.TestCase):
    def test_home_paths_expand_in_scalar_and_list_settings(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home = root / "home"
            target = root / "target"
            checkout = root / "checkout"
            original = {
                "provider-options-file": "~/provider.json",
                "review-pipeline-file": "~/pipeline.toml",
                "supplemental-guidelines": ["~/guidelines.md", "general"],
            }
            with patch.dict(os.environ, {"HOME": str(home)}):
                resolved = RUNNER.absolutize_config_paths(original, target, checkout)
            self.assertEqual(str(home / "provider.json"), resolved["provider-options-file"])
            self.assertEqual(str(home / "pipeline.toml"), resolved["review-pipeline-file"])
            self.assertEqual(
                [str(home / "guidelines.md"), "general"],
                resolved["supplemental-guidelines"],
            )
            self.assertEqual("~/provider.json", original["provider-options-file"])
            self.assertEqual("~/guidelines.md", original["supplemental-guidelines"][0])

    def test_existing_path_resolution_and_urls_are_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "target"
            checkout = root / "checkout"
            target.mkdir()
            checkout.mkdir()
            (target / "target.md").touch()
            (checkout / "checkout.md").touch()
            cases = [
                ("target.md", "target.md"),
                ("checkout.md", str(checkout / "checkout.md")),
                ("general", "general"),
                (str(root / "absolute.md"), str(root / "absolute.md")),
                ("https://example.com/~/guide.md", "https://example.com/~/guide.md"),
            ]
            for value, expected in cases:
                with self.subTest(value=value):
                    self.assertEqual(
                        expected, RUNNER.absolutize_repo_path(value, target, checkout)
                    )


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Cassandra AI-Reviewer local runner script example.

This script runs Cassandra on the current git repository comparing HEAD to the default branch.

Configuration:
    - Automatically loads and merges configuration from `--config` (or `examples/cassandra.toml`)
      and any repository-local `cassandra.toml`.
    - Automatically resolves the required API key based on the `provider` setting in TOML:
        - provider = "google"    -> GEMINI_API_KEY or GOOGLE_API_KEY
        - provider = "anthropic" -> ANTHROPIC_API_KEY
        - provider = "openai"    -> OPENAI_API_KEY
        - custom/fallback        -> LLM_API_KEY

Requirements:
    - Python 3.11+ (uses standard library `tomllib`).
"""

import argparse
import os
import subprocess
import sys
import tempfile
import tomllib
from pathlib import Path

# Map providers to environment variables
PROVIDER_ENV_VARS = {
    "google": ["GEMINI_API_KEY", "GOOGLE_API_KEY"],
    "anthropic": ["ANTHROPIC_API_KEY"],
    "openai": ["OPENAI_API_KEY"],
}


def serialize_flat_toml(data: dict) -> str:
    """Serializes a flat dictionary back into TOML format."""
    lines = []
    for k, v in data.items():
        if isinstance(v, list):
            lines.append(f"{k} = [")
            for item in v:
                lines.append(f'    "{item}",')
            lines.append("]")
        elif isinstance(v, bool):
            lines.append(f"{k} = {'true' if v else 'false'}")
        elif isinstance(v, (int, float)):
            lines.append(f"{k} = {v}")
        else:
            lines.append(f'{k} = "{v}"')
    return "\n".join(lines)


def merge_tomls(base_path: Path, override_path: Path) -> dict:
    """Merges override TOML into base TOML. Lists are joined, single values overridden."""
    base_data = {}
    if base_path.exists():
        with base_path.open("rb") as f:
            base_data = tomllib.load(f)

    override_data = {}
    if override_path.exists():
        with override_path.open("rb") as f:
            override_data = tomllib.load(f)

    merged = dict(base_data)
    for k, v in override_data.items():
        if k in merged:
            if isinstance(merged[k], list) and isinstance(v, list):
                # Join lists and deduplicate preserving order
                merged[k] = list(dict.fromkeys(merged[k] + v))
            else:
                merged[k] = v
        else:
            merged[k] = v
    return merged


def run_git(args, cwd, capture_output=True, check=True):
    """Helper to run a git command and return its stdout cleaned up."""
    cmd = ["git", "-C", str(cwd)] + args
    result = subprocess.run(cmd, capture_output=capture_output, text=True)
    if check and result.returncode != 0:
        err = result.stderr.strip() if capture_output and result.stderr else ""
        raise RuntimeError(
            f"Git command failed (exit code {result.returncode}): {' '.join(cmd)}\n{err}".strip()
        )
    return result.stdout.strip() if capture_output else result.returncode


def git_ref_exists(ref, cwd):
    """Helper to check if a git ref exists."""
    cmd = ["git", "-C", str(cwd), "show-ref", "--verify", "--quiet", ref]
    return subprocess.run(cmd).returncode == 0


def _get_default_base_ref(target_dir):
    """Resolves the default base ref, falling back to standard names if origin/HEAD is unset."""
    ref = run_git(["symbolic-ref", "-q", "refs/remotes/origin/HEAD"], target_dir, check=False)
    if ref:
        return ref

    # Fallback to standard remote/local refs if origin/HEAD is missing
    for fallback in [
        "refs/remotes/origin/main",
        "refs/remotes/origin/master",
        "refs/heads/main",
        "refs/heads/master",
    ]:
        if git_ref_exists(fallback, target_dir):
            return fallback

    print(f"Error: could not determine a default base ref for {target_dir}.", file=sys.stderr)
    sys.exit(1)


def resolve_api_key(provider: str, remaining_args: list[str]) -> str | None:
    """Resolves the API key for the given provider from environment variables."""
    # Check if --provider-api-key was passed directly in remaining args
    if "--provider-api-key" in remaining_args:
        return None

    env_vars = PROVIDER_ENV_VARS.get(provider.lower(), [])
    for var in env_vars:
        val = os.environ.get(var)
        if val:
            return val

    # Fallback env vars
    fallback_val = os.environ.get("LLM_API_KEY") or os.environ.get("PROVIDER_API_KEY")
    if fallback_val:
        return fallback_val

    # Construct error message
    expected_vars = env_vars + ["LLM_API_KEY"]
    print(
        f"Error: API key for provider '{provider}' is missing.\n"
        f"Please set one of the following environment variables: {', '.join(expected_vars)} "
        "or pass --provider-api-key.",
        file=sys.stderr,
    )
    sys.exit(1)


def main():
    # 1. Determine directories
    script_path = Path(__file__).resolve()
    cassandra_root = script_path.parent.parent
    target_dir = Path.cwd().resolve()

    # 2. Verify target is a git repository
    if run_git(["rev-parse", "--is-inside-work-tree"], target_dir, check=False) != "true":
        print(f"Error: {target_dir} is not a git repository.", file=sys.stderr)
        sys.exit(1)

    # 3. Determine the default base commit (SHA) of the target repository
    default_base_ref = _get_default_base_ref(target_dir)

    default_base_sha = run_git(["rev-parse", default_base_ref], target_dir)

    merge_base_sha = run_git(["merge-base", "HEAD", default_base_sha], target_dir, check=False)
    if not merge_base_sha:
        print(
            f"Warning: could not compute merge-base, falling back to tip of {default_base_ref}",
            file=sys.stderr,
        )
        merge_base_sha = default_base_sha

    print(f"Detected base ref:           {default_base_ref}", file=sys.stderr)
    print(f"Default branch tip:          {default_base_sha}", file=sys.stderr)
    print(f"Effective base (merge-base): {merge_base_sha}", file=sys.stderr)

    # 4. Parse command line arguments.
    parser = argparse.ArgumentParser(description="Cassandra AI-Reviewer local runner script example.")
    parser.add_argument(
        "--config",
        type=Path,
        help="Path to base TOML configuration file (defaults to examples/cassandra.toml).",
    )
    args, remaining_args = parser.parse_known_args()

    # Determine base configuration file
    if args.config:
        config_file = args.config.resolve()
    else:
        config_file = script_path.parent / "cassandra.toml"
        if not config_file.exists():
            config_file = cassandra_root / "cassandra.toml"

    if not config_file.exists():
        print(f"Error: Base configuration file '{config_file}' not found.", file=sys.stderr)
        sys.exit(1)

    temp_file_path = None
    try:
        # Merge target directory's local cassandra.toml if it exists
        local_config = target_dir / "cassandra.toml"
        if local_config.exists() and local_config.resolve() != config_file.resolve():
            print(f"Merging {local_config} into base configuration...", file=sys.stderr)
            merged_data = merge_tomls(config_file, local_config)
            with tempfile.NamedTemporaryFile(
                mode="w",
                suffix=".toml",
                prefix=f"cassandra.{target_dir.name}.",
                delete=False,
            ) as f:
                f.write(serialize_flat_toml(merged_data))
                temp_file_path = Path(f.name)
            config_file = temp_file_path
        else:
            with config_file.open("rb") as f:
                merged_data = tomllib.load(f)

        # 5. Resolve provider & API key from merged config
        provider = merged_data.get("provider", "google")
        api_key = resolve_api_key(provider, remaining_args)

        # 6. Build and run Cassandra via Bazel.
        print(f"Invoking Cassandra AI-Reviewer for {target_dir}...", file=sys.stderr)

        run_args = [
            "--render",
            "markdown",
            "--config",
            str(config_file),
            "--cwd",
            str(target_dir),
            "--base",
            merge_base_sha,
            "--head",
            "HEAD",
            "--allow-ask-developer",
            "--interactive-post-review",
        ]

        if api_key:
            run_args.extend(["--provider-api-key", api_key])

        run_args.extend(remaining_args)

        cmd = ["bazelisk", "run", "//cmd/ai_reviewer", "--"] + run_args

        try:
            subprocess.run(cmd, cwd=str(cassandra_root), check=True)
        except subprocess.CalledProcessError as e:
            sys.exit(e.returncode)
    finally:
        if temp_file_path and temp_file_path.exists():
            try:
                temp_file_path.unlink()
            except OSError:
                pass


if __name__ == "__main__":
    main()

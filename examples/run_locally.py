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
import json
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

# Config keys holding a single file path / dir that may be repo-relative.
PATH_KEYS = (
    "provider-options-file",
    "pre-review-prompt",
    "approval-evaluation-prompt-file",
    "mcp-config",
    "wishlist-dir",
)
# Config keys holding a list of guideline paths or named prompts.
PATH_LIST_KEYS = ("supplemental-guidelines",)


def absolutize_repo_path(value: str, target_dir: Path, cassandra_root: Path) -> str:
    """Rewrites a config file path to absolute against cassandra_root if not found in target_dir."""
    if os.path.isabs(value) or "://" in value:
        return value
    if (target_dir / value).exists():
        return value
    repo_candidate = cassandra_root / value
    if repo_candidate.exists():
        return str(repo_candidate)
    return value


def absolutize_config_paths(data: dict, target_dir: Path, cassandra_root: Path) -> dict:
    """Rewrites repo-relative file paths in merged config to absolute paths."""
    data = dict(data)
    for key in PATH_KEYS:
        value = data.get(key)
        if isinstance(value, str) and value:
            data[key] = absolutize_repo_path(value, target_dir, cassandra_root)
    for key in PATH_LIST_KEYS:
        value = data.get(key)
        if isinstance(value, list):
            data[key] = [
                absolutize_repo_path(item, target_dir, cassandra_root)
                if isinstance(item, str)
                else item
                for item in value
            ]
    return data


def serialize_flat_toml(data: dict) -> str:
    """Serializes a flat dictionary back into TOML format."""
    lines = []
    for k, v in data.items():
        if isinstance(v, dict):
            raise ValueError(f"Nested TOML tables are not supported for configuration key '{k}'")
        elif isinstance(v, list):
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



def has_arg(args: list[str], flag: str) -> bool:
    """Checks if a flag (e.g. '--base') is in args as '--base' or '--base='."""
    return any(a == flag or a.startswith(f"{flag}=") for a in args)


def _resolve_branch_ref(branch_name: str, target_dir: Path) -> str:
    """Resolves branch name to a fully-qualified ref if present, otherwise returns branch name."""
    for prefix in ["refs/heads/", "refs/remotes/origin/"]:
        ref = f"{prefix}{branch_name}"
        if git_ref_exists(ref, target_dir):
            return ref
    return branch_name


def _extract_branch_name(val: any) -> str | None:
    """Extracts a branch name from either a string or a dict ({'branch': ...} or {'name': ...})."""
    if isinstance(val, str):
        cleaned = val.strip()
        return cleaned or None
    if isinstance(val, dict):
        res = val.get("branch") or val.get("name") or val.get("ref")
        if isinstance(res, str):
            cleaned = res.strip()
            return cleaned or None
    return None


def _get_gh_stack_base_ref(target_dir: Path, current_branch: str) -> str | None:
    """Resolves the parent branch ref in a gh stack if the current branch is stacked."""
    if not current_branch:
        return None

    # 1. Try reading .git/gh-stack directly (fast, no external CLI dependency)
    git_common_dir = run_git(["rev-parse", "--git-common-dir"], target_dir, check=False)
    if git_common_dir:
        gh_stack_path = Path(git_common_dir)
        if not gh_stack_path.is_absolute():
            gh_stack_path = target_dir / gh_stack_path
        gh_stack_file = gh_stack_path / "gh-stack"
        if gh_stack_file.exists():
            try:
                with gh_stack_file.open("r", encoding="utf-8") as f:
                    data = json.load(f)
                for stack in data.get("stacks", []):
                    branches = stack.get("branches", [])
                    branch_names = [_extract_branch_name(b) for b in branches]
                    if current_branch in branch_names:
                        idx = branch_names.index(current_branch)
                        if idx > 0 and branch_names[idx - 1]:
                            return _resolve_branch_ref(branch_names[idx - 1], target_dir)
                        trunk = _extract_branch_name(stack.get("trunk"))
                        if trunk:
                            return _resolve_branch_ref(trunk, target_dir)
            except Exception:
                pass

    # 2. Fallback to `gh stack view --json` if gh CLI extension is available
    try:
        res = subprocess.run(
            ["gh", "stack", "view", "--json"],
            cwd=str(target_dir),
            capture_output=True,
            text=True,
            check=False,
        )
        if res.returncode == 0 and res.stdout.strip():
            data = json.loads(res.stdout)
            branches = data.get("branches", [])
            branch_names = [_extract_branch_name(b) for b in branches]
            if current_branch in branch_names:
                idx = branch_names.index(current_branch)
                if idx > 0 and branch_names[idx - 1]:
                    return _resolve_branch_ref(branch_names[idx - 1], target_dir)
                trunk = _extract_branch_name(data.get("trunk"))
                if trunk:
                    return _resolve_branch_ref(trunk, target_dir)
    except Exception:
        pass

    return None


def _get_upstream_base_ref(target_dir: Path, current_branch: str) -> str | None:
    """Checks if the current branch has a tracking upstream that isn't origin/<current_branch>."""
    if not current_branch:
        return None
    upstream = run_git(["rev-parse", "--abbrev-ref", "@{upstream}"], target_dir, check=False)
    if not upstream:
        return None
    if (
        upstream == current_branch
        or upstream == f"origin/{current_branch}"
        or upstream.endswith(f"/{current_branch}")
    ):
        return None
    return _resolve_branch_ref(upstream, target_dir)

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

    return None


def resolve_api_key(
    provider: str, remaining_args: list[str], api_key_env: str | None = None
) -> str | None:
    """Resolves the API key for the given provider from environment variables."""
    # Check if --provider-api-key was passed directly in remaining args (e.g. --provider-api-key KEY or --provider-api-key=KEY)
    if any(arg.startswith("--provider-api-key") for arg in remaining_args):
        return None

    if api_key_env:
        val = os.environ.get(api_key_env)
        if val:
            return val
        print(
            f"Error: environment variable '{api_key_env}' specified via --api-key-env is not set or empty.",
            file=sys.stderr,
        )
        sys.exit(1)

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
        "or pass --api-key-env / --provider-api-key.",
        file=sys.stderr,
    )
    sys.exit(1)


def main():
    # 1. Determine directories
    script_path = Path(__file__).resolve()
    cassandra_root = script_path.parent.parent
    # 1. Parse command line arguments.
    parser = argparse.ArgumentParser(description="Cassandra AI-Reviewer local runner script example.")
    parser.add_argument(
        "--cwd",
        type=Path,
        help="Path to target git repository (defaults to current directory).",
    )
    parser.add_argument(
        "--config",
        type=Path,
        help="Path to base TOML configuration file (defaults to examples/cassandra.toml).",
    )
    parser.add_argument(
        "--acp",
        action="store_true",
        help="Enable Agent Client Protocol (ACP) mode over stdio.",
    )
    parser.add_argument(
        "--wait-for-message",
        action="store_true",
        help="Wait for an initial ACP user message before starting the review (requires --acp).",
    )
    parser.add_argument(
        "--api-key-env",
        "--env-key-name",
        dest="api_key_env",
        type=str,
        help="Optional environment variable name to read the provider API key from.",
    )
    args, remaining_args = parser.parse_known_args()

    if args.wait_for_message and not args.acp:
        print("Error: --wait-for-message can only be used with --acp.", file=sys.stderr)
        sys.exit(1)

    target_dir = args.cwd.resolve() if args.cwd else Path.cwd().resolve()

    # 2. Determine default base commit (SHA) of target repository if in a git repo
    is_git = run_git(["rev-parse", "--is-inside-work-tree"], target_dir, check=False) == "true"
    base_ref = None
    base_type = None
    base_sha = None
    merge_base_sha = None

    has_base_arg = has_arg(remaining_args, "--base")
    has_meta_arg = has_arg(remaining_args, "--metadata-json")
    has_diff_arg = has_arg(remaining_args, "--diff-file")

    if is_git and not has_base_arg and not has_meta_arg and not has_diff_arg:
        current_branch = run_git(["branch", "--show-current"], target_dir, check=False)

        # 1. Check stack parent from gh stack
        base_ref = _get_gh_stack_base_ref(target_dir, current_branch)
        if base_ref:
            base_type = "stack parent"
        else:
            # 2. Check git upstream branch tracking
            base_ref = _get_upstream_base_ref(target_dir, current_branch)
            if base_ref:
                base_type = "upstream branch"
            else:
                # 3. Default trunk branch fallback
                base_ref = _get_default_base_ref(target_dir)
                if base_ref:
                    base_type = "default branch"

        if base_ref:
            base_sha = run_git(["rev-parse", base_ref], target_dir, check=False)
            if base_sha:
                merge_base_sha = run_git(["merge-base", "HEAD", base_sha], target_dir, check=False)
                if not merge_base_sha:
                    merge_base_sha = base_sha

    if merge_base_sha:
        if not args.acp:
            label = f"Detected base ref ({base_type}):" if base_type else "Detected base ref:"
            print(f"{label:<32} {base_ref}", file=sys.stderr)
            print(f"Base branch tip:                 {base_sha}", file=sys.stderr)
            print(f"Effective base (merge-base):     {merge_base_sha}", file=sys.stderr)
    elif not has_base_arg and not has_meta_arg and not has_diff_arg:
        if not args.acp:
            print(
                f"No git repository or base branch detected in {target_dir}. Assuming no-diff.",
                file=sys.stderr,
            )

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
    runner_script = None
    try:
        # Merge target directory's local cassandra.toml if it exists
        local_config = target_dir / "cassandra.toml"
        if local_config.exists() and local_config.resolve() != config_file.resolve():
            if not args.acp:
                print(f"Merging {local_config} into base configuration...", file=sys.stderr)
            merged_data = merge_tomls(config_file, local_config)
        else:
            with config_file.open("rb") as f:
                merged_data = tomllib.load(f)

        merged_data = absolutize_config_paths(merged_data, target_dir, cassandra_root)
        if args.acp and merged_data.get("allow-url-fetch"):
            merged_data["allow-url-fetch"] = False

        with tempfile.NamedTemporaryFile(
            mode="w",
            suffix=".toml",
            prefix=f"cassandra.{target_dir.name}.",
            delete=False,
        ) as f:
            temp_file_path = Path(f.name)
            f.write(serialize_flat_toml(merged_data))
        config_file = temp_file_path

        # 5. Resolve provider & API key from merged config or CLI overrides
        provider = merged_data.get("provider", "google")
        for i, arg in enumerate(remaining_args):
            if arg == "--provider" and i + 1 < len(remaining_args):
                provider = remaining_args[i + 1]
                break
            elif arg.startswith("--provider="):
                provider = arg.split("=", 1)[1]
                break
        api_key = resolve_api_key(provider, remaining_args, args.api_key_env)

        # 6. Build and run Cassandra via Bazel (--script_path keeps stdin/stdout clean for ACP).
        if not args.acp:
            print(f"Invoking Cassandra AI-Reviewer for {target_dir}...", file=sys.stderr)

        run_args = [
            "--config",
            str(config_file),
            "--cwd",
            str(target_dir),
        ]
        if merge_base_sha:
            run_args.extend(["--base", merge_base_sha])
            if not has_arg(remaining_args, "--head"):
                run_args.extend(["--head", "HEAD"])
        elif not has_base_arg and not has_meta_arg and not has_diff_arg:
            run_args.extend(
                [
                    "--diff-file",
                    "/dev/null",
                    "--files-list-file",
                    "/dev/null",
                    "--commits-file",
                    "/dev/null",
                ]
            )
        if args.acp:
            run_args.append("--acp")
            if args.wait_for_message:
                run_args.append("--wait-for-message")
        else:
            run_args.extend(
                [
                    "--render",
                    "markdown",
                    "--allow-ask-developer",
                    "--interactive-post-review",
                ]
            )

        if api_key:
            run_args.extend(["--provider-api-key", api_key])

        run_args.extend(remaining_args)

        with tempfile.NamedTemporaryFile(
            prefix="cassandra.runner.", suffix=".sh", delete=False
        ) as sf:
            runner_script = Path(sf.name)

        try:
            subprocess.run(
                [
                    "bazelisk",
                    "run",
                    "--noshow_progress",
                    "--ui_event_filters=-info,-stdout,-stderr",
                    f"--script_path={runner_script}",
                    "//cmd/ai_reviewer",
                ],
                cwd=str(cassandra_root),
                stdin=subprocess.DEVNULL,
                stdout=sys.stderr,
                check=True,
            )
            subprocess.run([str(runner_script)] + run_args, cwd=str(cassandra_root), check=True)
        except subprocess.CalledProcessError as e:
            sys.exit(e.returncode)
    finally:
        for path in (temp_file_path, runner_script):
            if path and path.exists():
                try:
                    path.unlink()
                except OSError:
                    pass


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Pinned Hermes one-shot entrypoint; no model-visible tools or ambient context."""

import os
from pathlib import Path
import sys


def require_empty_tools(agent):
    # Check the assembled schemas, including late-added context-engine tools.
    if getattr(agent, "tools", None) != []:
        raise RuntimeError("perps proposal tool isolation failed")


def install_guard(agent_class):
    original_init = agent_class.__init__
    original_run = agent_class.run_conversation

    def guarded_init(self, *args, **kwargs):
        # Pinned -z does not forward config agent.max_turns to this constructor.
        kwargs.update(skip_context_files=True, skip_memory=True,
                      skip_background_review=True, load_soul_identity=False,
                      max_iterations=1)
        original_init(self, *args, **kwargs)
        require_empty_tools(self)

    def guarded_run(self, *args, **kwargs):
        require_empty_tools(self)
        return original_run(self, *args, **kwargs)

    agent_class.__init__ = guarded_init
    agent_class.run_conversation = guarded_run


def main():
    os.umask(0o077)
    export = bool(sys.argv[1:])
    if export:
        args = sys.argv[1:]
        if args != ["sessions", "export", "--format", "jsonl", "--redact", "--yes",
                    "/opt/research-data/sessions.jsonl"]:
            raise RuntimeError("perps proposal export arguments are invalid")
    home = Path("/opt/research-data")
    if (os.environ.get("HERMES_HOME") != str(home) or
            os.environ.get("HOME") != str(home) or Path.cwd() != home or
            (not export and set(path.name for path in home.iterdir()) != {"auth.json", "config.yaml"})):
        raise RuntimeError("perps proposal home is not fresh")
    import yaml
    config = yaml.safe_load((home / "config.yaml").read_text(encoding="utf-8"))
    if (not isinstance(config, dict) or config.get("mcp_servers") != {} or
            config.get("plugins") != {"enabled": []}):
        raise RuntimeError("perps proposal startup configuration is invalid")
    if export:
        from hermes_cli.main import main as hermes_main
        sys.argv = ["hermes", *args]
        hermes_main()
        return
    with Path("/opt/mithril/prompts/perps-proposal.md").open("rb") as stream:
        prompt = stream.read((1 << 20) + 1)
    if not prompt or len(prompt) > 1 << 20:
        raise RuntimeError("perps proposal prompt size is invalid")
    prompt = prompt.decode("utf-8")
    # The image is pinned; do not rely on the model to honor a no-tools prompt.
    from run_agent import AIAgent
    install_guard(AIAgent)
    # v2026.8.27 resolves unknown names to zero tools but -z rejects them.
    # Register an explicitly empty process-local toolset, not an MCP server.
    from toolsets import create_custom_toolset
    create_custom_toolset("none", "No tools", tools=[])
    from hermes_cli.main import main as hermes_main
    sys.argv = ["hermes", "-z", prompt, "--provider", "openai-codex",
                "--model", "gpt-5.6-terra", "--reasoning", "high", "--toolsets", "none"]
    hermes_main()


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Provider/library errors can contain prompts or credential material.
        print("perps proposal isolated invocation failed", file=sys.stderr)
        raise SystemExit(1)

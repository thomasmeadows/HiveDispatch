"""The task the Go worker sends on stdin (internal/executor/langgraph)."""

import json
from dataclasses import dataclass, field, fields


@dataclass
class ChatModel:
    provider: str = ""
    model: str = ""
    base_url: str = ""
    api_key_env: str = ""


@dataclass
class Task:
    ticket: str = ""
    prompt: str = ""
    workspace: str = "."
    resume_token: str = ""
    step_budget: int = 0
    code_with: str = "claude"
    model: str = ""
    base_ref: str = ""
    git_dir: str = ""
    hivedispatch: str = "hivedispatch"
    worker_config: str = ""
    checks: list[str] = field(default_factory=list)
    allowed_commands: list[str] = field(default_factory=list)
    check_timeout_seconds: int = 600
    max_fix_rounds: int = 3
    max_review_rounds: int = 1
    guidance: str = ""
    path: list[str] = field(default_factory=list)
    chat_model: ChatModel = field(default_factory=ChatModel)

    @classmethod
    def from_json(cls, raw: str) -> "Task":
        data = json.loads(raw)
        known = {f.name for f in fields(cls)}
        kwargs = {k: v for k, v in data.items() if k in known and v is not None}
        cm = kwargs.pop("chat_model", {}) or {}
        cm_known = {f.name for f in fields(ChatModel)}
        t = cls(**kwargs)
        t.chat_model = ChatModel(**{k: v for k, v in cm.items() if k in cm_known})
        return t

"""The chat model for the plan and review nodes: any OpenAI-compatible API."""

import os

from langchain_openai import ChatOpenAI


def chat_model(task):
    cm = task.chat_model
    key = os.environ.get(cm.api_key_env, "") if cm.api_key_env else ""
    return ChatOpenAI(model=cm.model, base_url=cm.base_url or None, api_key=key or "unused", temperature=0)

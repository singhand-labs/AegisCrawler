#!/usr/bin/env bash
set -e
cd "$(dirname "$0")/../server"
LLM_API_KEY=${LLM_API_KEY:-} RUN_LLM_EVAL=1 go test -tags eval ./internal/llm/eval/... -v

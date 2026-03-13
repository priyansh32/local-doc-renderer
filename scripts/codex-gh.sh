#!/usr/bin/env bash

set -euo pipefail

trim_whitespace() {
    local value="$1"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    printf '%s' "$value"
}

get_arthur_token() {
    local env_path="$1"
    local line token

    if [[ ! -f "$env_path" ]]; then
        printf '.env not found at %s\n' "$env_path" >&2
        return 1
    fi

    line="$(grep -m1 -E '^[[:space:]]*ARTHUR_TOKEN[[:space:]]*=' "$env_path" || true)"
    if [[ -z "$line" ]]; then
        printf 'ARTHUR_TOKEN not found in %s\n' "$env_path" >&2
        return 1
    fi

    token="${line#*=}"
    token="$(trim_whitespace "$token")"

    if [[ ${#token} -ge 2 ]]; then
        if [[ "${token:0:1}" == '"' && "${token: -1}" == '"' ]]; then
            token="${token:1:${#token}-2}"
        elif [[ "${token:0:1}" == "'" && "${token: -1}" == "'" ]]; then
            token="${token:1:${#token}-2}"
        fi
    fi

    if [[ -z "${token//[[:space:]]/}" ]]; then
        printf 'ARTHUR_TOKEN is empty in %s\n' "$env_path" >&2
        return 1
    fi

    printf '%s' "$token"
}

restore_env_var() {
    local name="$1"
    local value="$2"
    local was_set="$3"

    if [[ "$was_set" == "1" ]]; then
        export "$name=$value"
    else
        unset "$name"
    fi
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
env_path="$repo_root/.env"
token="$(get_arthur_token "$env_path")"

prev_gh_token="${GH_TOKEN-}"
had_gh_token=0
if [[ ${GH_TOKEN+x} ]]; then
    had_gh_token=1
fi

prev_gh_host="${GH_HOST-}"
had_gh_host=0
if [[ ${GH_HOST+x} ]]; then
    had_gh_host=1
fi

prev_prompt_disabled="${GH_PROMPT_DISABLED-}"
had_prompt_disabled=0
if [[ ${GH_PROMPT_DISABLED+x} ]]; then
    had_prompt_disabled=1
fi

cleanup() {
    restore_env_var "GH_TOKEN" "$prev_gh_token" "$had_gh_token"
    restore_env_var "GH_HOST" "$prev_gh_host" "$had_gh_host"
    restore_env_var "GH_PROMPT_DISABLED" "$prev_prompt_disabled" "$had_prompt_disabled"
}
trap cleanup EXIT

export GH_TOKEN="$token"
export GH_HOST="github.com"
export GH_PROMPT_DISABLED="1"

if ! login="$(gh api user --jq '.login' 2>/dev/null)"; then
    printf 'Failed to validate GitHub auth with ARTHUR_TOKEN.\n' >&2
    exit 1
fi

login="$(trim_whitespace "$login")"
if [[ -z "$login" ]]; then
    printf 'Failed to validate GitHub auth with ARTHUR_TOKEN.\n' >&2
    exit 1
fi

if [[ "$login" != "realArthurMorgan" ]]; then
    printf "ARTHUR_TOKEN resolves to '%s', expected 'realArthurMorgan'.\n" "$login" >&2
    exit 1
fi

if [[ "$#" -eq 0 ]]; then
    gh auth status
else
    gh "$@"
fi

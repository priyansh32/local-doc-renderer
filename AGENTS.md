# AGENTS.md

## Purpose

This repo prefers a low-friction workflow with minimal back-and-forth.

When possible, interpret the user's request by stage and complete that stage fully before stopping.

## Default Stages

Use these defaults unless the user says otherwise:

- `investigate` or `analyze`
  - inspect code and behavior
  - do not edit files
  - do not post to GitHub

- `fix`
  - implement the code change locally
  - verify it
  - do not commit, push, open a PR, or merge unless explicitly requested

- `ship`
  - implement the fix if needed
  - verify it
  - commit the requested files only
  - push the branch
  - open or update the PR

- `merge`
  - merge the PR
  - confirm the linked issue state

## Communication Style

- Keep updates short and direct.
- Prefer plain English over jargon.
- Keep GitHub issue comments and PR text concise unless the user explicitly asks for detail.
- If the user asks for markdown text for GitHub, provide ready-to-paste markdown.
- Avoid multiple wording iterations unless the user asks.

## Git And GitHub Defaults

- GitHub CLI (`gh`) is available in this repo and should be the default tool for GitHub actions.
- Assume `gh` is the normal path for:
  - listing, viewing, creating, and commenting on issues
  - creating, viewing, updating, and merging PRs
  - checking issue/PR state before acting
- If the user says things like `open PR`, `update issue`, `post this`, `merge PR`, or `close issue`, use `gh` directly unless they explicitly ask for draft text only.
- Before creating a new issue or PR, check existing GitHub state with `gh` to avoid duplicates when practical.
- Never commit unrequested files.
- If the user names specific files to commit, commit only those files.
- Leave unrelated local changes alone.
- When creating a PR, keep the summary short and include the requested verification note if one was given.
- When updating issues or PRs, prefer practical root cause and fix plan over long writeups.

## Verification Defaults

- If the user says `verified manually`, do not add or commit tests in that pass.
- If the user says `add tests later`, leave test files uncommitted unless explicitly requested.
- If verification is not specified:
  - run the smallest useful local verification
  - report what was verified and what was not

## Preferred Request Format

For the fastest workflow, the user should ideally provide:

1. Goal
2. Constraints
3. Git/GitHub actions
4. Verification level
5. Stop point

Example:

```text
Fix issue #1.

Constraints:
- keep explanations short
- no tests in this pass
- commit only main.go

Git/GitHub:
- update the issue
- push the branch
- open a PR with "verified manually"
- do not merge yet

Verification:
- manual verification only

Stop after the PR is created.
```

## Useful Short Commands

These phrases should be interpreted narrowly:

- `investigate only`
- `fix locally`
- `fix and ship`
- `commit only <files>`
- `open PR only`
- `update issue`
- `post this to GitHub`
- `merge PR`
- `keep GH text short`
- `no tests in this pass`
- `verified manually`

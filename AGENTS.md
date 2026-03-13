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
  - do not commit, push, open a PR, or merge unless explicitly requested

- `ship`
  - implement the fix if needed
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
- Do not work directly on `main` or `master` when implementing fixes unless the user explicitly says to.
- Create a new branch for implementation work unless the user specifies otherwise.
- Never commit unrequested files.
- If the user names specific files to commit, commit only those files.
- Leave unrelated local changes alone.
- Do not run destructive git commands such as `git reset --hard`, `git clean -fd`, or force push unless explicitly requested.
- When updating issues or PRs, prefer practical root cause and fix plan over long writeups.

## Decision Rules

If the user's request is ambiguous:

1. Prefer investigation before modification.
2. Prefer local fixes before GitHub actions.
3. Prefer small changes over large refactors.
4. Ask for clarification if progress is impossible.

## Diff Discipline

When implementing fixes:

- keep changes minimal
- avoid formatting-only edits
- avoid refactors unless required for the fix
- prefer surgical diffs

## Request Style

The user does not need to follow a template.

- Expect normal human instructions, not structured task specs.
- Infer the goal, constraints, Git/GitHub actions, and stop point from context when possible.
- If the user leaves details out, make the smallest reasonable assumption and keep moving.
- Ask for clarification only when progress would otherwise be risky or blocked.

Structured requests are welcome, but they are optional.

## Verification

If verification is not specified:

- run the smallest useful local check (build, lint, or targeted test)
- avoid long test suites unless needed to confirm the fix
- report what was verified and what was not

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

## Autonomy Boundaries

The agent may:

- investigate code
- implement local fixes
- create branches
- open or update PRs when requested

The agent must NOT:

- merge PRs
- delete branches
- modify repository settings
- change CI configuration

unless the user explicitly asks.
---
name: pubmir-mirror
description: Use when working inside a git repository managed by pubmir as a sanitized mirror (it contains .pubmir.yml with role: mirror). Explains the private/mirror security boundary - preserve <PUBMIR:KEY> tokens as-is, freely use `pubmir status`/`pubmir check`, never run `pubmir sync` automatically, and never try to reach the paired private repository.
---

## Purpose

This skill defines how an AI coding agent should behave when working in a repository managed by `pubmir`.

`pubmir` maintains a security boundary between:

* a **private repository**, which may contain real infrastructure values and sensitive information
* a **mirror repository**, which contains sanitized/tokenized representations intended to be safe for AI-assisted work

The AI should normally operate only inside the mirror repository.

The purpose of this skill is not to automate the security boundary itself. It is to ensure that the agent understands and respects that boundary.

---

## Recognizing a pubmir repository

A repository is managed by pubmir when it contains:

```text
.pubmir.yml
```

Read this file when necessary to determine the repository role.

Typical values:

```yaml
role: mirror
```

or:

```yaml
role: private
```

**If `role: private`, stop.** This skill is written for mirror-side work only. Tell the human that AI work is intended to happen in the paired mirror repository instead, and do not proceed with edits here unless the human explicitly overrides that.

When operating in a mirror repository, treat it as the complete working context available to the AI.

Do not attempt to bypass the mirror by locating the corresponding private repository.

---

## PUBMIR tokens

Sanitized repositories may contain tokens such as:

```text
<PUBMIR:VPS_PUBLIC_IP>
<PUBMIR:VPS_USER>
<PUBMIR:PRIVATE_DOMAIN>
```

These tokens intentionally replace private values.

They are meaningful symbolic identifiers and should normally be preserved exactly.

Do not:

* guess the underlying value
* infer the underlying value and replace the token
* search local files, environment variables, shell history, sibling directories, or other resources for the real value
* rewrite tokens into plausible real-looking values
* remove tokens merely because they look unusual

For example:

```text
ssh <PUBMIR:VPS_USER>@<PUBMIR:VPS_PUBLIC_IP>
```

is valid mirror content.

When editing such content, preserve the tokens unless the task specifically requires changing the symbolic structure itself.

---

## Commands the AI may run

The AI may run read-only pubmir commands when they help understand or validate the repository.

### `pubmir status`

The AI may run:

```bash
pubmir status
```

Use it when synchronization state is relevant to the task.

Examples:

* determining whether the mirror has unsynchronized commits
* checking whether the repository is in a valid pubmir state
* understanding whether human synchronization may be needed after the work is complete

Do not treat `pubmir status` as permission to access the paired private repository directly.

---

### `pubmir check`

The AI may run:

```bash
pubmir check
```

Use it when appropriate, especially after making changes that may affect sanitized content, and before considering work complete.

If `pubmir check` reports a possible secret leak or pubmir safety violation:

1. stop any action that could publish or propagate the affected repository
2. inspect only the mirror-side information necessary to understand the reported problem
3. do not search for the corresponding private value
4. report the problem clearly to the human

---

## Commands the AI must not run automatically

Do not run:

```bash
pubmir sync
```

on your own.

`pubmir sync` crosses the security boundary between the private and mirror repositories.

This operation is intentionally human-controlled.

If synchronization is needed, tell the human that `pubmir sync` should be run from the appropriate repository.

Do not invoke it merely because:

* the mirror is behind the private repository
* the private repository is behind the mirror
* your work is complete
* `pubmir status` reports unsynchronized commits

The human decides when changes cross the pubmir boundary.

---

## Private repository access

When operating in a mirror repository:

**Do not attempt to access the paired private repository.**

This includes avoiding actions such as:

* changing directory into the private repository
* reading its files
* reading its Git objects
* inspecting its commit history directly
* reading `.pubmir.env`
* reading `.pubmir/secrets-history.json`
* inspecting `.pubmir/local.yml` for the purpose of locating private content
* recursively searching parent or sibling directories for matching repositories
* following paths discovered through pubmir metadata to retrieve private values

The existence of a local paired repository does not make it part of the AI's working context.

The mirror exists specifically to prevent this access from being necessary.

---

## `.pubmir/local.yml`

`.pubmir/local.yml` may contain machine-local information such as the path to the paired repository.

Do not use that path to inspect or access the paired repository.

If this file must be read by tooling internally, treat its contents as operational metadata, not as an invitation to follow the referenced path.

Avoid including its local path values in generated documentation, commit messages, issue text, or other output unless explicitly requested by the human.

---

## Git operations

Normal Git operations inside the current mirror repository are allowed.

The AI may, when appropriate:

```bash
git status
git diff
git log
git show
git blame
```

and may edit files normally.

If the user has authorized the agent to make commits as part of the task, commits may also be created in the mirror repository.

The mirror repository's Git history is intentionally useful context for AI work.

Do not inspect Git objects belonging to the private repository.

---

## Completing work

After making changes in a mirror repository:

1. review the changes normally
2. run relevant tests
3. run `pubmir check` when appropriate
4. report the resulting Git state
5. if synchronization is required, tell the human to run `pubmir sync`

Do not perform the synchronization yourself.

A typical completion message may state that:

```text
The mirror changes are ready. `pubmir check` passes.
There are commits waiting to be synchronized to the private repository.
Please run `pubmir sync` when you want to apply them.
```

Do not repeatedly remind the human about synchronization when it is not relevant.

---

## Safety priority

When convenience conflicts with the pubmir security boundary, preserve the boundary.

In particular:

* never expose a private value merely to make a configuration executable
* never bypass a token because the actual value would make testing easier
* never access the private repository to obtain missing context
* never run `pubmir sync` automatically
* never assume that read access to the local machine implies authorization to inspect private pubmir data

If a task cannot be completed using the sanitized mirror alone, explain what information is missing and let the human decide how to proceed.

---

## Design principle

The intended division of responsibility is:

```text
AI:
    understand mirror
    edit mirror
    inspect mirror history
    run tests
    run pubmir status
    run pubmir check

Human:
    manage private values
    decide when changes cross the boundary
    run pubmir sync
```

In short:

> The AI works inside the mirror. The human controls the boundary.

---
name: Bug report
about: Something behaves differently from what the docs or the code say it should
labels: bug
---

## What happened

## What you expected

## How to reproduce

## Environment

- How you run it: Docker image / self-hosted binary / macOS desktop client
- Host OS and version:
- Version (the console shows it; for Docker, the image tag):

## Before filing

`docs/TRAPS.md` lists behaviours that look like bugs and are not. Worth a look first.

Note: kernel-level enforcement holds on **Linux only**. On macOS it degrades to a
Seatbelt sandbox that intercepts writes only, and the self-check reports which layer is
actually in effect — that is documented, not a bug.

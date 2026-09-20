# Contributing to NeoX OS

Thanks for your interest. Bug reports, fixes, scenario cases and docs are all welcome.

## Setup

```bash
cd go && go build ./...                 # the system itself
cd packages/apps/console
npm install && npm run os:pack          # build the frontend, embed it in the Go binary
./electron/bin/neox-console             # prints a URL with a token on first boot
```

Node ≥ 20, Go 1.25.

The mobile client needs its speech models fetched once — they are not kept in the
repository:

```bash
mobile/tool/fetch-models.sh
```

## Versions

`packages/apps/console/package.json` carries the version of the OS itself; the desktop
build takes the DMG filename from it (`NeoX-OS-${version}-arm64.dmg`). The root
`package.json` is a private workspace root and stays at `0.0.0` on purpose.

`mobile/pubspec.yaml` versions the Flutter client separately, because app stores track
it on their own schedule. It is not expected to match.

## Before opening a pull request

```bash
cd go && go build ./... && go vet ./... && go test ./...
cd packages/apps/console && npm run typecheck && npx vitest run
```

Both must be clean.

## What to know before changing the boundary layer

Kernel-level enforcement (`go/confine/`) only holds on Linux. macOS degrades to a
Seatbelt sandbox that intercepts writes only, so a change that "passes on macOS" has
not been tested where it matters. Acceptance for anything under `go/confine/` is done
on a real Linux machine or VM, and `selfcheck.go` must keep reporting the enforcement
level honestly — a layer that isn't actually applied must not be listed as if it were.

`docs/TRAPS.md` is the list of things that have already bitten us. Read it before
assuming a behaviour is a bug.

## Scenario bench

```bash
python3 tools/scenario/run.py list
```

Scenarios run against a live system with real models, and assert invariants rather than
mocking. If your change alters agent behaviour, add or update a scenario instead of only
adding a unit test.

## Commit messages

Say what changed and why it matters to a user of the system. Both English and Chinese are fine.

## How this repository is maintained

This tree is generated from a private source repository that also holds the hosted
product's account, subscription and gateway code. That code is not part of the open
release; behaviour that differs between the two goes through a capability contract
(`packages/apps/console/src/cloud/`, `mobile/lib/cloud/`), and the open build registers
a no-op implementation. Everything else is the same code.

Pull requests are welcome and are merged here in the normal way. Merged changes are
carried back into the private source, so a contribution does not get overwritten by the
next sync.

Push access is limited to maintainers. Fork the repository, open a pull request against
`main`, and expect one review.

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).

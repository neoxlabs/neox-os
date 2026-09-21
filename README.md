<div align="center">

<!-- One colored wordmark, deliberately not a <picture>: prefers-color-scheme follows the
     operating system, while the GitHub theme is an account setting. When the two disagree
     the near-black wordmark lands on a dark page and the word disappears. The monochrome
     wordmark-dark / wordmark-white variants stay for in-app use. -->
<img src="packages/apps/console/src/assets/wordmark-color.png" alt="NeoX OS" height="52">

**A self-hosted operating system for AI agents**

One person running a room full of bots: every bot is a real process, its boundary is
enforced by the kernel, and the work it produces is judged by git.

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](go/)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](packages/apps/console/)
[![Electron](https://img.shields.io/badge/Electron-40-47848F?logo=electron&logoColor=white)](packages/apps/console/electron/)
[![Platform](https://img.shields.io/badge/platform-macOS%20%C2%B7%20Linux-555555)](#quick-start)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

[Website](https://os.neox-dev.com) · [中文文档](README.zh-CN.md)

</div>

---

Not a loop inside a chat window, and not a workflow orchestrator. The OS owns the
machine: it hands out workspaces, translates declared capabilities into kernel rules,
records everything that happens, and holds the line while you are away.

## Features

- **Processes, not sessions** — a bot is a process with a lifecycle. Processes die;
  the conversation doesn't. An append-only event ledger can rebuild the exact state at
  any point in time, so a restarted agent picks up where it stopped.
- **The boundary lives in the kernel** — on Linux, declared capabilities are translated
  into landlock, network namespaces and cgroup2 rules. Child processes inherit them, so
  `nohup` doesn't get you out. On macOS the enforcement degrades to a Seatbelt sandbox
  (writes only) and the self-check reports which layer is actually in effect rather than
  claiming full enforcement.
- **Collaboration at the artifact level** — when several bots work on one project, each
  gets its own git worktree branch. Sync, merge, handoff and rollback are plain git
  operations. Delegation is one level deep: one instruction, and everyone in the room
  acts at most once.
- **Approval belongs to the system** — crossing a boundary asks a human instead of
  crashing. Once approved, the OS relaunches the process with the widened scope and the
  conversation continues without a seam. Credentials never enter a confined process;
  they stay with the host.
- **Cost engineering** — a context page table with O(1) fork, content-addressed
  deduplication, and a measured prefix-cache hit rate of 94–99%, plus a cache health
  signal that does not rely on the hit rate alone.
- **Scenario test bench** — a dozen real situations (several bots contending for one
  file, an interrupt mid-run, a handoff to another bot, burning through a budget cap)
  run against the live system with real models. The assertions are invariants, not mocks.

## Architecture

```
┌─────────────────────────────────────────────────┐
│  Clients                                         │
│  Electron desktop shell / browser (built-in UI)  │
└──────────────┬──────────────────────────────────┘
               │ HTTP + SSE (Bearer token)
┌──────────────┴──────────────────────────────────┐
│  neox-console (Go)                               │
│  ├─ osinit    process table · ledger · decisions │
│  ├─ agent     ReAct loop · tools · teamwork      │
│  ├─ confine   capabilities → kernel rules        │
│  └─ engine    context page table · cache · LLM   │
└─────────────────────────────────────────────────┘
```

| Directory | Contents |
|---|---|
| `go/` | **The system.** Everything on the production path: `osinit` (process table, event ledger, decisions), `agent` (the ReAct loop and tools), `confine` (capabilities → kernel rules), `engine` (context page table, cache), `abi` (wire format), `boot` (PID 1), `cmd/` (binaries) |
| `packages/apps/console/` | The console: a React frontend, embedded into the Go binary, plus the Electron desktop shell |
| `packages/{abi,boot,confine,engine,init}/` | **Not dead code, and not the production path.** These are the original TypeScript implementations, kept deliberately as a differential-test baseline: Go must decode frames TypeScript encoded, byte for byte, and both sides must move when the wire format does. A port that can only check itself proves nothing |
| `mobile/` | Flutter client (Android + iOS): chat, on-device speech recognition, presence |
| `android/` | A separate, much smaller Android app (`com.neox.sense`) that reports phone signals — presence, connectivity — to an OS instance. It is not the mobile client |
| `tools/scenario/` | Scenario bench: real situations run against the live system with real models |
| `probe/` | A deliberately foreign agent process, used to prove confinement applies to things we did not write |
| `docs/` | How it boots, how it layers, how to run it in Docker, how to cut a release, what works today, and the traps already paid for |

### Two Dockerfiles, two jobs

- **`Dockerfile.console`** builds the OS itself — the thing you run. `docker run` it and
  the browser has a full interface on port 7717. This is what the published image is.
- **`Dockerfile`** builds the confinement layer: a minimal image whose PID 1 is
  `neox-init`, which is the environment a *bot* runs inside. You do not run it directly.

## Quick start

Three shapes, one binary.

### Docker (fastest)

```bash
docker pull lmk1010/neox-os:latest

docker run -d --name neox-os --restart unless-stopped \
  -p 7717:7717 \
  -v neox-os-data:/root/.neox-os \
  -e NEOX_OBSERVE_TOKEN=my-secret-key \
  lmk1010/neox-os:latest

# open http://localhost:7717/?token=my-secret-key
```

Pick your own token via `NEOX_OBSERVE_TOKEN` and you won't have to dig the address out
of the logs. Without it a random one is generated on first boot and printed by
`docker logs neox-os`. Opening the bare address also works — you get an onboarding flow
(paste the token, point it at an inference endpoint, pull the model list). API
credentials can be injected the same way:

```bash
  -e NEOX_API_KEY=sk-… -e NEOX_API_BASE=https://api.deepseek.com -e NEOX_MODEL_ID=deepseek-chat
```

If 707MB is too much, `lmk1010/neox-os:slim` is 176MB without node. Volumes,
permissions, remote access and building your own image: [`docs/DOCKER.md`](docs/DOCKER.md).

### Self-hosted (browser)

```bash
cd packages/apps/console
npm install
npm run os:pack        # build the frontend → embed it in the Go binary
./electron/bin/neox-console
# first boot prints: open http://127.0.0.1:7717/?token=…
```

The token persists in `~/.neox-os/token` and survives restarts. For remote access, set
the listen address explicitly — and secure the transport yourself, via a private
network, a tunnel or TLS:

```bash
NEOX_OBSERVE_ADDR=0.0.0.0:7717 ./electron/bin/neox-console
```

### Desktop client (macOS)

[**Download the latest build**](https://os.neox-dev.com) (Apple Silicon, signed and notarized)

Drag it into Applications and double-click. It is signed and notarized by Apple, so you
will not hit the "unidentified developer" wall.

The client **ships a complete OS inside it** — no Docker, no Go, no node required. On
first launch it takes a look at what the machine already has (git, python3, node) and
offers a copy-paste install command for anything missing. You can skip all of it and
still use the product.

To run the work somewhere else — a Docker host on a server, say — switch to remote mode
under Settings → Connection and fill in the address and token. The execution
environment is then the container's, and the client only connects and renders.

Running the client from source:

```bash
cd packages/apps/console
npm install && npm run os:pack   # first time, or after changing code
npm start                        # day to day
npm run dmg                      # build a signed DMG (see docs/RELEASE.md)
```

### Inference

Under Settings → Inference, fill in the endpoint, model and key (OpenAI-compatible
protocol). The model list can be pulled in one click, and connectivity is probed for
real, including an actual check of vision capability.

## Development

```bash
cd go && go build ./... && go vet ./... && go test ./...          # system layer
cd packages/apps/console && npm run typecheck && npx vitest run   # frontend
python3 tools/scenario/run.py list                                # scenario bench
```

Packaging, signing, notarizing and uploading a client release: [`docs/RELEASE.md`](docs/RELEASE.md).

Kernel-level enforcement only holds on Linux, and acceptance is done on real Linux
machines or VMs. macOS is a development environment, where the self-check reports the
enforcement level honestly.

## Status

Single user, single machine, under active development. Known limits and outstanding
debts are recorded in [`docs/STATUS.md`](docs/STATUS.md) and
[`docs/TRAPS.md`](docs/TRAPS.md) — the rule for both documents is that every sentence
written there must point at a command you can still re-run today.

## License

[Apache-2.0](LICENSE)

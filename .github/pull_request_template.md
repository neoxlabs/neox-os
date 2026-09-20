## What this changes

<!-- One or two sentences. What behaviour is different after this merges? -->

## Why

<!-- The reason the change is needed. If it fixes something, describe what went wrong. -->

## How it was verified

<!-- Not "it should work". What did you actually run? -->

```
cd go && go build ./... && go vet ./... && go test ./...
cd packages/apps/console && npm run typecheck && npx vitest run
```

- [ ] Both of the above pass
- [ ] If this touches `go/confine/`, it was verified on a real Linux machine or VM —
      macOS degrades to a write-only Seatbelt sandbox, so passing there proves nothing
      about enforcement
- [ ] If it changes agent behaviour, a scenario in `tools/scenario/` covers it

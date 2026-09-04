# task-1-relay

## Commands

```
go test ./internal/buildapp -count=1 -v   # PASS, see task-1-relay.test.txt
go test ./... -count=1                   # PASS (aistudio/api/buildapp/camoufoxnative)
go vet ./...                             # exit 0, empty output
go build ./...                           # exit 0, empty output
```

## Real Build surface

Unit fixtures only. No live applet/Camoufox session in this todo.
Do not treat this as real Build end-to-end success.

## Bounds

`MaxRelayBodyBytes = 32MiB` decoded body. Stale `Content-Length` is stripped and rebuilt from actual bytes.

## Adversarial

| Class | Result |
| --- | --- |
| malformed input | covered: malformed body_b64, missing MIME, TRACE/CONNECT, `..`, admin path |
| long external command | N/A — no shell/exec in this change |
| flaky/timing-sensitive test | N/A — no sleep; IDs not asserted |
| misleading success output | N/A — tests assert bytes/`errors.Is`, not log text |
| dirty worktree | inspected; commit stages only relay files + this evidence |
| mid-operation interruption | reject happens before `Server.Submit`; pending map stays empty |

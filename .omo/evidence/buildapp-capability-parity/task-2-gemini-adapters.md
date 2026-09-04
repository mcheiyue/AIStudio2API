# task-2-gemini-adapters

## Commands

```
go test ./internal/api ./internal/aistudio -count=1 -v   # PASS, see .test.txt
go test ./... -count=1                                   # PASS
go vet ./...                                             # exit 0
go build ./...                                           # exit 0
```

## Real Build surface

HTTP fixtures with fake Service/ServeBuildApp only. No live applet.
Do not treat as real Build E2E success.

## Adversarial

| Class | Result |
| --- | --- |
| malformed input | empty contents/content/requests and unknown method return 400, worker not started |
| misleading success | assertions check batch path, `models/<model>`, split embedding shape, not log text |
| dirty worktree | commit stages only adapter files + this evidence |
| long external command | N/A — no shell |
| flaky/timing | N/A — no sleep |
| mid-operation interruption | N/A — validation before ServeBuildApp |

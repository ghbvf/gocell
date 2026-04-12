# PR Plan: Runtime Race Closure

| PR | Scope | Tasks | Depends | Verify | Branch |
| --- | --- | --- | --- | --- | --- |
| PR-1 | runtime bootstrap reload gate + eventbus regression closure | T001-T010 | none | `cd src && go test -race ./runtime/eventbus ./runtime/worker ./runtime/bootstrap && go build ./...` | `fix/217-runtime-race` |

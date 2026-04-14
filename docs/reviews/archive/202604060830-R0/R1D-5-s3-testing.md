# R1D-5: adapters/s3 Testing Review

- **Seat**: S3 Test / Regression
- **Date**: 2026-04-06
- **Baseline**: `5096d4f`
- **Scope**: `adapters/s3/` production and test files
- **Method**: isolated rerun from code and direct test execution only; no existing review docs consulted during this rerun

## Verification

Executed in 项目根目录:

```bash
go test ./adapters/s3
go test -cover ./adapters/s3
go test -tags=integration -run TestIntegration -v ./adapters/s3
```

Observed:

- unit tests passed
- coverage reported `85.9%`
- all three integration-tagged tests compiled and then `SKIP`

## Findings

- `P1` integration coverage is effectively zero. All three integration cases are placeholders with `t.Skip(...)`, so no real MinIO/S3 round-trip is verified. Refs: [integration_test.go:11](/Users/shengming/Documents/code/gocell/adapters/s3/integration_test.go#L11), [integration_test.go:17](/Users/shengming/Documents/code/gocell/adapters/s3/integration_test.go#L17), [integration_test.go:23](/Users/shengming/Documents/code/gocell/adapters/s3/integration_test.go#L23)

- `P1` the fake server does not validate SigV4, `Host`, or `x-amz-*` headers, so signing-related tests are shallow and can produce false confidence. Refs: [client_test.go:27](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L27), [client_test.go:35](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L35), [client_test.go:264](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L264), [client_test.go:276](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L276), [client_test.go:308](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L308)

- `P1` `UsePathStyle=false` is completely untested. Every test config sets `UsePathStyle: true`, so the broken default branch is invisible to the suite. Refs: [client.go:145](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L145), [client_test.go:100](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L100), [client_test.go:107](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L107)

- `P1` reserved-character keys are untested. Current cases only cover simple ASCII slash paths, so the canonical URI and URL-construction edge cases have no regression protection. Refs: [client_test.go:219](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L219), [client_test.go:243](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L243), [client_test.go:264](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L264)

- `P2` timeout, cancellation, and transport-failure branches are barely exercised. Tests use `context.Background()` and do not inject hanging handlers or failing transports. Refs: [client.go:104](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L104), [client.go:257](/Users/shengming/Documents/code/gocell/adapters/s3/client.go#L257), [objects.go:21](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L21), [client_test.go:204](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L204)

- `P2` `Delete_NotFound` is a false positive. Production code explicitly allows `404`, but the fake server always returns `204` for deletes, even on missing keys. Refs: [objects.go:95](/Users/shengming/Documents/code/gocell/adapters/s3/objects.go#L95), [client_test.go:84](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L84), [client_test.go:247](/Users/shengming/Documents/code/gocell/adapters/s3/client_test.go#L247)

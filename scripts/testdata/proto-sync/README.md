# scripts/testdata/proto-sync — hermetic regression fixture for the proto-sync gate

`green/` is a self-consistent mirror of the live repo's proto/stub layout:
`api/proto/**` (5 protos) plus the 4 generated stub dirs (10 `*.pb.go`).
It is consumed by `scripts/check-proto-sync.sh`'s embedded self-tests on every
invocation:

- green fixture → the gate must exit 0 (sensitivity);
- red fixture → derived at test time (temp copy + one comment-line injection
  via `sed -i`), the gate must exit 1 (permissiveness).

## Rules

- Refresh the mirror whenever the 5 protos or 10 stubs materially change, in
  the same commit, with `cp -r` from the live tree **after** `make proto`:

  ```bash
  cp -r api/proto scripts/testdata/proto-sync/green/api/
  cp -r internal/sso/ssopb internal/sso/authzpb internal/sso/auditpb \
    scripts/testdata/proto-sync/green/internal/sso/
  cp -r internal/handler/grpc/aeropb \
    scripts/testdata/proto-sync/green/internal/handler/grpc/
  ```

- Never edit fixture files by hand. The mirror must stay byte-identical to
  what the pinned toolchain regenerates; a stale mirror cannot false-fail the
  gate (the fixture is self-consistent), but it decays regression fidelity.
- The red variant is derived at test time; never commit a red fixture.

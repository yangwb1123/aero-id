# Black-box audited repository contracts

This is an integration-tagged **test-only** package, not another repository,
audit authority, runtime service or binary. It exercises the public repository
ports for immutable facts/anchored receipts, publication and Callback replay,
Legal Hold and their legacy cutover/backfill evidence. Production composition is
unchanged. White-box ledger tests and unrelated account/projection/command tests
remain in `internal/repository`.

The 53 existing top-level tests and their helpers moved here without changes
except the package clause. Every test retains its own PostgreSQL container,
readiness checks and migrations. Four small private audit fixtures are copied
from the root tests; the relocation guard compares their function ASTs. It does
not share a database, pool, migration result, credential or container between
packages/tests and introduces no TestMain or test filter.

Both complete Make entries still use `./...`; Go discovers this package
naturally. `make test-integration` retains serial package execution;
`make test-integration-bounded` retains native `-p 2`. Neither the 15-minute
per-package nor 20-minute outer default budget changes. Individual package
passes do not establish that the combined invocation fits or succeeds.

`make check-integration-runner` and ordinary unit tests verify the reviewed
`internal/config/testdata/repository-test-relocation.json`: original full-file
SHA-256 (normalizing only the package identifier), no old copy, exact combined
185-test name inventory and equivalent copied fixtures. The manifest is a
reviewed regression baseline, not automatic permission to regenerate hashes or
weaken assertions. Intentional test additions/changes need explicit baseline
review; never update it just to hide a missing, duplicated or altered test.

Run moved regressions by this package path when diagnosing them. Release
acceptance still requires the complete gate, actual storage/image availability
and the separately configured real-source contracts. See
[fixture diagnostics](../../../docs/runbooks/integration-fixtures.md).

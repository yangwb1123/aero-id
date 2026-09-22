# Forge observer contract fixture

`forge-run-observed-v1.json` is a byte-for-byte test mirror of Catalyst's
`docs/contracts/fixtures/forge-run-observed-v1.json`. The Go contract test
embeds this local copy so Aero-ID can run its observer boundary checks from a
standalone checkout without depending on a workspace-specific absolute path.

The fixture is evidence metadata only: it carries no prompt, message, tool,
output, credential, artifact, or raw owner data, and every authority field is
`false`.

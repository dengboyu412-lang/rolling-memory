<!--
Thanks for contributing. Keep one concern per pull request; see CONTRIBUTING.md for the
full gate set, ground rules, and DCO sign-off.

Heads-up: this repository is a release snapshot, so patches are adopted into the source tree and
credited in CHANGELOG.md rather than merged here. See CONTRIBUTING.md § "How patches are adopted,
and how we credit them".
-->

## Summary

<!-- What does this change, and why? -->

## Related issue

<!-- Fixes #... / Ref #... / n/a -->

## Type of change

- [ ] Bug fix
- [ ] New capability
- [ ] Documentation
- [ ] Other (describe below)

## Checklist

- [ ] I ran the gate set from CONTRIBUTING.md from the repository root:
      `go build ./...` · `go vet ./...` · `gofmt -l cmd internal sdk` (no output) · `go test -short ./... -count=1`
- [ ] New behavior is covered by tests that use synthetic data only — no real personal data, secrets, transcripts, or private URLs
- [ ] Changed public contracts (MCP tool schema, receipt shape, sync contract, mode names) are reflected in the matching `docs/` page and `CHANGELOG.md`
- [ ] Sensitive-data rules are respected: verbatim anchors, append-only writes, deterministic reads
- [ ] Commits are signed off (`git commit -s`, Developer Certificate of Origin 1.1)

## Notes for reviewers

<!-- Anything reviewers should know: trade-offs, migration notes, follow-ups. -->

# Releasing Griglia

The release boundary is human: CI prepares everything, but tagging and
publishing are deliberate manual steps.

## Checklist for vX.Y.Z

1. **Decide the version.** Semantic-ish: breaking protocol or CLI changes
   bump the major once past 1.0. The tag is `vX.Y.Z`; artifact names use
   `X.Y.Z`.
2. **Run full verification locally** on the release commit:

   ```bash
   gofmt -l .            # must print nothing
   go vet ./...
   go test ./...
   go test -race ./...
   CGO_ENABLED=0 go build ./cmd/griglia
   git diff --check
   ```

3. **Verify CI is green** on `main` for that commit (checks: `test` and the
   five `cross-build (os/arch)` checks).
4. **Verify cross-builds and packaging locally**:

   ```bash
   scripts/build-release.sh X.Y.Z
   ls dist/   # 4 tar.gz + 1 zip + checksums.txt
   ```

5. **Review migrations**: shipped migration files are immutable; anything new
   must be a new numbered file, covered by the migration matrix tests
   (`internal/sqlite/migrations_test.go`).
6. **Review protocol compatibility**: only additive changes within protocol
   v1; anything breaking requires a `protocol_version` bump with explicit
   justification, updated `docs/PROTOCOL.md`, and updated conformance tests.
7. **Tag and push**:

   ```bash
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

8. **Verify the draft release** the `Release` workflow created: all five
   artifacts plus `checksums.txt` are attached and the notes make sense.
9. **Verify checksums** for at least one artifact:

   ```bash
   sha256sum -c --ignore-missing checksums.txt
   ```

10. **Smoke-install one artifact**: extract it, run `griglia version`
    (it must report the tagged version and commit), then
    `griglia init && griglia task add "smoke" && griglia task list --json`
    in a scratch directory.
11. **Publish** the draft release.

## Version metadata

`scripts/build-release.sh` injects metadata at link time:

```bash
go build -ldflags "-X main.version=X.Y.Z -X main.commit=<sha> -X main.date=<utc>"
```

When link-time metadata is absent, `griglia version` falls back to the build
information the Go toolchain embeds in the binary: `go install
github.com/alle80/griglia-tui/cmd/griglia@vX.Y.Z` reports `X.Y.Z` (commit and
build date stay `unknown` — the module zip carries no VCS data), and a plain
`go build` in a checkout reports the stamped module version plus the VCS
commit and time, with `+dirty` marking a modified tree. Builds with no
embedded metadata at all still report `dev (commit unknown, built unknown)`.
Nothing at runtime ever requires Git.

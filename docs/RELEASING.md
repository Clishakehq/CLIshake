# Releasing CLIshake

Releases are automated with [GoReleaser](https://goreleaser.com). Pushing a
`vX.Y.Z` tag builds the binaries, publishes a GitHub Release, and updates the
Homebrew cask — no manual per-release steps.

## One-time setup

The tap repository already exists: **`clishakehq/homebrew-clishake`** (private
for now; make it public at launch). The only thing left to wire once is a token
so a release can push the generated cask into that separate repo — the default
CI `GITHUB_TOKEN` is scoped to this repo only.

1. Create a fine-grained Personal Access Token:
   GitHub → *Settings* → *Developer settings* → *Fine-grained tokens* → *Generate*.
   - **Repository access:** only `clishakehq/homebrew-clishake`.
   - **Permissions:** *Contents* → *Read and write*.
2. Store it as a secret on this repo:
   ```sh
   gh secret set HOMEBREW_TAP_GITHUB_TOKEN --repo clishakehq/clishake
   # paste the token when prompted
   ```

That's it. Nothing else needs configuring for future releases.

## Cutting a release

To release `X.Y.Z`:

1. Bump the source fallback version (used by `go install` / `make install`
   builds; GoReleaser overrides it from the tag for release artifacts):
   `internal/cli/root.go` → `var Version = "X.Y.Z"`.
2. Update `CHANGELOG.md`.
3. Tag and push:
   ```sh
   git commit -am "Release vX.Y.Z"
   git tag vX.Y.Z
   git push origin main vX.Y.Z
   ```

Pushing the tag triggers [`.github/workflows/release.yml`](../.github/workflows/release.yml),
which runs GoReleaser to:

- build macOS + Linux binaries (amd64 + arm64),
- publish a GitHub Release with archives + `checksums.txt`,
- commit `Casks/clishake.rb` to the tap repo.

Users then upgrade with any of: `brew upgrade clishake`, `clishake update`, or
`go install github.com/clishakehq/clishake/cmd/clishake@latest`.

## Validate the config locally (optional)

```sh
go install github.com/goreleaser/goreleaser/v2@latest
goreleaser check                                   # lint .goreleaser.yaml
goreleaser release --snapshot --clean --skip=publish  # full build to dist/, no push
```

## Going public — launch checklist

- [ ] Make `clishakehq/clishake` public.
- [ ] Make `clishakehq/.github` public — its `profile/README.md` becomes the
      org's landing page at github.com/clishakehq (it only renders while public).
- [ ] Make `clishakehq/homebrew-clishake` public (a cask can't be installed from
      a private tap without auth).
- [ ] Confirm the `HOMEBREW_TAP_GITHUB_TOKEN` secret is set (see above).
- [ ] Cut the first public release so a cask exists in the tap.
- [ ] Add the Homebrew line to the README **Install** section:
      `brew install --cask clishakehq/clishake/clishake`.
- [ ] The in-tool update check (`internal/selfupdate`) starts working
      automatically once the repo is public and releases exist — no code change.

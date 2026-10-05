# Releasing

geta, getaotel, and getavet are released together at one version, with three tags on one commit: `vX.Y.Z` (the root module), `getaotel/vX.Y.Z`, and `getavet/vX.Y.Z`. On that commit, `getaotel/go.mod` and `getavet/go.mod` require `github.com/koji-1009/geta vX.Y.Z`.

In the repository, `go.work` makes every module use the root in this checkout, so builds, tests, and CI never fetch the version a submodule requires. `go mod tidy` ignores `go.work`, so it does fetch that version.

## Steps

1. Bump the submodules' require, then commit (signed), open a pull request, and merge it:

   ```
   scripts/bump.sh vX.Y.Z
   git commit -S -am "Release vX.Y.Z"
   ```

   Until step 2, `vX.Y.Z` does not exist, so do not run `go mod tidy` in getaotel or getavet.

2. Tag the merged commit with a signed tag and push only that tag:

   ```
   git switch main
   git pull
   git tag -s vX.Y.Z -m vX.Y.Z
   git push origin vX.Y.Z
   ```

   The `release` workflow checks that `getaotel/go.mod` and `getavet/go.mod` on the tagged commit require the root at exactly `vX.Y.Z`, then creates `getaotel/vX.Y.Z` and `getavet/vX.Y.Z` on that commit. If the check fails, it creates no tag.

## Verify

After the workflow has finished, build each submodule outside the workspace against the published root:

```
go -C getaotel mod tidy
GOWORK=off go -C getaotel build ./...
go -C getavet mod tidy
GOWORK=off go -C getavet build ./...
```

`go mod tidy` adds the `vX.Y.Z` checksums to `getaotel/go.sum` and `getavet/go.sum`, which step 1 could not; without them, `GOWORK=off go build` stops at "missing go.sum entry". Commit that change in the next pull request.

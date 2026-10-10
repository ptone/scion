# hubshard

`hack/hubshard` splits the `pkg/hub` **test files** into build-tag shards, so a
shard's test binary compiles with a fraction of the memory the full `pkg/hub`
test binary needs. Production code is not touched.

- **Default build (no tags): unchanged.** Every test file compiles and runs,
  because each shard constraint is `!hubshard || hubshard_K`.
- **`-tags hubshard,hubshard_K`**: the binary contains the `pkg/hub` sources,
  the **common** test files, and only shard K's test files.

A sharded file's build line is its own constraint (if any) AND the shard
clause:

```go
//go:build !no_sqlite && (!hubshard || hubshard_3)
```

## Commands

```sh
make hubshard                    # (re)apply the tags: go run ./hack/hubshard
make hubshard-check              # fail if any file or the assignment is stale
make test-hub-shard SHARD=2      # go test -tags hubshard,hubshard_2 ./pkg/hub
make build-hub-shard SHARD=2     # compile only (go test -c), binary discarded
```

Other tags combine as usual, e.g. `go test -tags no_sqlite,hubshard,hubshard_2`.
The union of the shards runs every test exactly once, except the tests in
common files, which run in every shard.

Flags: `-n N` sets the shard count (it rebalances everything when it differs
from the assignment file), `-rebalance` recomputes all assignments, `-check`
writes nothing and fails if anything is out of date, `-v` lists the common
files (with the reason) and the multi-file groups, and `-dumpgraph FILE`
writes the file dependency graph.

## When to re-run

Re-run `make hubshard` after adding, deleting or renaming `pkg/hub` test
files, or after changing which helpers a test file uses. A new **untagged**
file is always safe: it compiles in every shard. It just costs memory in each
shard until the generator assigns it.

A file has to be common when a sharded file in a different shard starts
using a helper it declares. Until the generator has re-run, that shard fails
to compile with `undefined: helperX`. That is a loud failure, never a silently
skipped test.

## How files are classified

1. **Dependency graph (file level).** Every test file is parsed with
   `go/parser`. File X *uses* file Y when X mentions an identifier the parser
   could not resolve inside X (so not a local and not one of X's own
   declarations), and Y declares that name at top level. Methods tie a file to
   the test file that declares the receiver type, in both directions. A method
   a test file declares on a `pkg/hub` type is linked to every file that
   selects that name (`x.Name`). The analysis is syntactic and conservative:
   a false edge (for example a struct-literal key that happens to match a
   helper name) can only make a file common or group files together. It can
   never drop a needed file from a shard.
2. **Common seeds.** Files with `TestMain`, or with an `init` function that
   does more than `_ = x` discards.
3. **Closure.** Anything a common file uses is common.
4. **Groups.** The remaining files are split into connected components of
   the use graph. A group always lands in one shard, so every file a sharded
   file needs is either common or in the same shard.
5. **Cut.** While the largest group is bigger than total/`-maxgroupdiv`
   lines (default 16), one of its files moves to common, together with its
   closure. The file chosen has the best ratio of "group members using it" to
   "lines added to common".
6. **Assignment.** `assignment.txt` records every file's shard (`0` =
   common). Existing entries are kept: a group goes to the shard holding most
   of its lines. A file that was common stays common while another test file
   still uses it. New groups go to the lightest shard by lines, largest group
   first, with ties broken by name. So a file's shard does not move when
   unrelated files change.

Every step is deterministic, and the tool is idempotent: a second run changes
nothing.

## Current numbers (N=4)

1160 test files, 588,503 lines. About 530 files declare a helper that another
test file uses, and they form one connected component. The cut leaves
**304 common files (about 234k lines, 40%)**, and each shard gets about
88.7k lines. Most of the common set exists because shared helpers sit
inside large test files. The helpers themselves, with their transitive
closure, are only about 31k lines. A separate helper-extraction series moves
them into helper-only files; after it lands, re-run `make hubshard
ARGS=-rebalance` to shrink the common set.

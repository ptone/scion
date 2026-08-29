Closing the loop on the two review findings above, both of which were **investigated and rejected on evidence**. Recording the rationale here rather than only in our internal notes, since this thread is the durable record.

**Both findings share one premise, and the premise is a misquote.**

The review quotes the disputed test inputs as all-lowercase:

> `6ba7b810-9dad-11d1-80b4-00c04fd430c8`

The file does not contain that string at those lines. At merged `87a867b77`:

- `pkg/messages/dm_key_test.go:297` — `"6ba7b810-9dad-11d1-80b4-00C04FD430C8"`
- `pkg/messages/dm_key_test.go:388` — `"6ba7b810-9dad-11d1-80b4-00C04FD430C8"`

Both are **mixed case** — lowercase prefix, uppercase final group. That is precisely what makes them valid negative fixtures. The argument in the review is sound *if* the input were all-lowercase; it is not.

Verified empirically at the merged SHA:

- all-lowercase input → **accepted**, key derived
- the actual file string → **rejected**, `shape: uppercase-hex`

The review's suggested replacement value is also mixed-case, so applying it would produce an identical rejection and an identical test outcome — the change is a no-op with respect to behaviour.

**Non-vacuity, checked separately.** A test asserting a rejection is worthless if it never runs or if the assertion cannot discriminate. Both were confirmed:

1. `dm_key_test.go` carries no build tag, so it is compiled and executed under CI's `-tags no_sqlite` — it is not one of the files CI silently skips.
2. `go test -tags no_sqlite ./pkg/messages/ -run TestDMConversationKey -v` at `87a867b77` shows `reject/mixed-case UUID` and `RejectsNonCanonicalUUID/mixed_case` as **running** subtests, both passing. The assertions check both `require.Error` and that the message contains `non-canonical UUID`, so they discriminate the intended failure branch rather than any error.

**Why this matters beyond one test.** Once authorization parses the DM key, the key derivation function is security-critical and its golden vectors are a security control, not a style choice. The rejection of non-canonical UUIDs is deliberate: never normalise a key on the derivation path — a differing round-trip is an error, never a rewrite, because a wrong key is worse than no key when the key *is* the ACL. Loosening these fixtures to accept case variants would open exactly the aliasing gap the vectors exist to close.

No change made. Flagging so the two findings are not read later as unaddressed.

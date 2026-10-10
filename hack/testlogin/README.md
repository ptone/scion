# testlogin

`testlogin` gets a short-lived **member** test identity from a hub that runs
with `--enable-test-login`. It uses the hub's existing
`POST /api/v1/auth/test-login` endpoint. It is a test utility for integration
tests that need a real, non-admin user. It is not part of the product.

An operator runs it on the hub host, as a user that can read the hub's
environment file (usually root).

## Build

```sh
go build -o testlogin ./hack/testlogin
```

The tool does not link `pkg/hub`, so it builds quickly.

## Usage

```sh
# Private output directory (mode 0700)
dir=$(mktemp -d)

sudo ./testlogin mint \
  --hub-url http://127.0.0.1:8080 \
  --secret-file /etc/scion/hub.env \
  --out "$dir/token"
```

| Flag | Meaning |
|------|---------|
| `--hub-url` | Hub base URL, the port that serves `/api/v1/auth/test-login`. Plain `http` is accepted only for a loopback address; otherwise use `https`. |
| `--secret-file` | The hub's systemd `EnvironmentFile`, or a unit drop-in with `Environment=` lines, that holds the session secret. This is the only place the secret is read from. |
| `--secret-var` | The variable to read from `--secret-file`. Default: `SCION_SERVER_SESSION_SECRET`, then `SESSION_SECRET`, the same order the hub uses. If set, only this variable is used. Use it when the unit passes a differently named variable to `--session-secret`. |
| `--out` | The token file to create. It must not exist. |
| `--name-prefix` | Prefix for the generated email and display name. Default `test-fixture`. |
| `--timeout` | Overall timeout. Default 30s. |

On success the tool prints only non-secret facts: the user id, the email, the
role, the token expiry time and the token file path. Use the token as
`Authorization: Bearer $(cat "$dir/token")`.

There is no flag to choose the role. The tool always requests `member`.

### What `mint` does

1. Creates the `--out` file exclusively with mode 0600. An existing file or
   symlink at that path is never overwritten, and the hub is not contacted.
2. Reads the session secret from `--secret-file` into memory. It derives the
   user signing key the same way the hub does
   (`sha256("scion-hub-signing-key:user_signing_key:" + secret)`), then
   clears the secret.
3. Preflight: sends the test-login request with no credentials. The hub
   answers 403 when test-login is disabled and 401 when it is enabled.
   Nothing is created. The tool continues only on 401.
4. Signs a five-minute challenge (audience `scion-test-login`). This
   challenge cannot be used as an access token. It then calls test-login with
   role `member`, an email `<prefix>-<16 hex>@scion-test.invalid` and display
   name `<prefix> <16 hex>`. A 401 here means the secret file does not hold
   the secret the hub signs with. The tool does not retry with other sources.
5. Checks the result before writing anything:
   - the response and the token's claims say role `member` and the expected
     email;
   - the token is an access token and its `exp - iat` is 30 minutes or less
     (the hub issues 15 minutes);
   - `GET /api/v1/auth/me` with the token returns the same uid, role
     `member` and the expected email;
   - `GET /api/v1/users/<uid>` shows that the account was created during
     this run (`created` falls within the run, allowing 5 seconds, and
     `lastLogin` is within 5 seconds of `created`). test-login updates an
     existing user that has the same email, so an account that already
     existed is always rejected;
   - `GET /api/v1/admin/server-config` returns 403.
6. Only then writes the access token to the file.

If any step fails, the tool removes the token file and exits non-zero. If a
user was returned, its id is printed so an admin can delete it.

The refresh token in the test-login response is never stored. The response
type has no field for it, and the raw response is cleared after decoding. The
HTTP client keeps no cookies and does not follow redirects. The tool never
prints, logs or writes the secret, the derived key or any token.

## Cleanup

```sh
sudo ./testlogin cleanup --hub-url http://127.0.0.1:8080 --token-file "$dir/token"
rmdir "$dir"
```

`cleanup` reads the token's user id. It asks `/auth/me` whether the token is
still live and has role `member`, then deletes the file and confirms that it
is gone. It exits non-zero if the hub reports anything other than a member
for that user, but it still removes the file.

The tool does **not** delete the user. Deleting a user needs an admin
session. To remove the user completely:

1. With the test token, delete any agents and then any projects the test
   user created. The hub refuses to delete a user that still owns them.
2. An admin deletes the user (id printed by `mint` and `cleanup`), for
   example from the Users admin page. This removes the user's group
   memberships and bindings, and every token issued to the user stops
   working, including the refresh token that was never stored.
3. Check that `/api/v1/auth/me` with the old token returns 401.

## Security properties

- Role is fixed at `member`. test-login stores exactly the requested role on
  the user, and for `member` it only adds the user to the hub members group.
  A member request cannot produce an admin.
- A JWT whose subject has no user record is rejected with 401. After the
  user is deleted, its tokens are useless.
- The secret comes from one root-only file, never from argv or the
  environment. It is held only in memory and cleared (best effort) after use.
- The only file written is the access token: mode 0600, created
  exclusively, at a path the operator chose.
- The access token lives 15 minutes. The challenge lives 5 minutes.
- Generated emails use the reserved `.invalid` domain. They are random, so
  they do not collide with real accounts, and the freshness check rejects
  an existing account anyway.

## Tests

- `internal/testlogin` and `internal/challenge`: unit tests against an
  `httptest` fake hub. These are fast and do not link `pkg/hub`.
- `internal/hubpin`: links `pkg/hub`. It pins the key derivation and the
  challenge format to the hub, and runs `mint` and `cleanup` against an
  in-process hub backed by SQLite.

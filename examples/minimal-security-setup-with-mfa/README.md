# Minimal security setup with MFA

Minimal MFA-backed auth setup using `require mfa` for the admin user.

All endpoints are protected, including `/api/*`. The auth portal is at `/r`.

## Adding more than one MFA device

A user’s `mfa_tokens` is an array. At login, every non-disabled TOTP token is tried in order; **any one valid code succeeds**. See the [data structure](https://github.com/greenpau/go-authcrunch/blob/main/pkg/identity/user.go) and [verification logic](https://github.com/greenpau/go-authcrunch/blob/main/pkg/authn/handle_json_login.go).

### Recommended: add via the portal UI

After signing in to the auth portal:

```text
User Settings
→ Multi-Factor Authenticators
→ Add Authenticator App
```

Repeat to add another device. The portal will:

- generate a random `secret`
- assign a 40-character `id` on save
- show a QR code
- require one successful TOTP check
- append the new token to `mfa_tokens`

This is the safest path and does not require editing `users.json`. See the [official UI docs](https://docs.authcrunch.com/docs/authenticate/auth-portal#add-authenticator-app).

### Manual: edit `users.json`

Only if you must edit `/data/.local/caddy/users.json` directly:

```sh
MFA_ID=$(openssl rand -hex 20)
MFA_SECRET=$(openssl rand -hex 40)

printf 'id=%s\nsecret=%s\n' "$MFA_ID" "$MFA_SECRET"
```

- `id`: 40 hex characters — internal unique key only
- `secret`: 80 hex characters — the shared TOTP secret stored as ASCII hex
- each token needs a distinct `id`, `secret`, and `comment` (duplicates are rejected; see [add logic](https://github.com/greenpau/go-authcrunch/blob/main/pkg/identity/user.go#L494-L512))

Example JSON:

```json
"mfa_tokens": [
  {
    "id": "existing-40-char-id",
    "type": "totp",
    "algorithm": "sha1",
    "comment": "phone1",
    "secret": "existing-secret",
    "period": 30,
    "digits": 6
  },
  {
    "id": "new-40-char-id",
    "type": "totp",
    "algorithm": "sha1",
    "comment": "phone2",
    "description": "Backup authenticator",
    "secret": "new-secret",
    "period": 30,
    "digits": 6
  }
]
```

### Import the new secret into an authenticator

`users.json` stores the raw ASCII `secret`. The `otpauth://` URI `secret` parameter must be **Base32 without `=` padding** of those same bytes — not the hex string as-is, and **not Base64**. See the [Google Key URI Format](https://github.com/google/google-authenticator/wiki/Key-Uri-Format#secret).

Linux (or any system with `base32`):

```sh
MFA_SECRET_BASE32=$(
  printf '%s' "$MFA_SECRET" |
  base32 |
  tr -d '=\n'
)
```

macOS has no `base32` by default. Use Python (do **not** substitute Base64):

```sh
MFA_SECRET_BASE32=$(
  printf '%s' "$MFA_SECRET" |
  python3 -c 'import base64,sys; print(base64.b32encode(sys.stdin.buffer.read()).decode().rstrip("="))'
)
```

Or install GNU coreutils and use `gbase32`:

```sh
brew install coreutils

MFA_SECRET_BASE32=$(
  printf '%s' "$MFA_SECRET" |
  gbase32 |
  tr -d '=\n'
)
```

Build the URI (change issuer / account labels as needed):

```sh
MFA_EMAIL='user@example.com'
MFA_ISSUER='AUTHP'

MFA_URI="otpauth://totp/${MFA_ISSUER}:${MFA_EMAIL}?secret=${MFA_SECRET_BASE32}&issuer=${MFA_ISSUER}&algorithm=SHA1&digits=6&period=30"
printf '%s\n' "$MFA_URI"
```

Local QR only (never use an online QR generator — the URI contains the full MFA secret):

```sh
qrencode -t ANSIUTF8 "$MFA_URI"
```

Notes:

- Keep the raw `$MFA_SECRET` in `users.json`.
- Put `$MFA_SECRET_BASE32` only in the authenticator / `otpauth://` URI.
- Do not run `xxd -r -p` on the hex secret; caddy-security uses the hex string’s ASCII bytes.
- Adding via the portal UI performs Base32 encoding for you.

#### Renaming the label shown in the authenticator app

`AUTHP` appears twice; change **both** to the same value:

```text
otpauth://totp/AUTHP:user@example.com?...&issuer=AUTHP
                   ↑ service name                    ↑ issuer / group
```

Example display name `My Caddy`:

```sh
MFA_ISSUER='My%20Caddy'
MFA_ACCOUNT='user@example.com'

MFA_URI="otpauth://totp/${MFA_ISSUER}:${MFA_ACCOUNT}?secret=${MFA_SECRET_BASE32}&issuer=${MFA_ISSUER}&algorithm=SHA1&digits=6&period=30"
```

Typical display:

```text
My Caddy
user@example.com
```

To change the account line under the issuer, edit the value after the colon (`${MFA_ACCOUNT}`). Use `%20` for spaces. Keep both issuer fields identical or some apps may reject or mis-display the entry.

### Safe edit procedure

```sh
cp /data/.local/caddy/users.json \
   /data/.local/caddy/users.json.backup

jq empty /data/.local/caddy/users.json
```

Prefer stopping Caddy, editing and validating JSON, then starting again. After the new authenticator works, remove old tokens if desired. TOTP secrets in `users.json` are plaintext shared keys — restrict read access to the file and its backups.

On DHI-based images the process runs as uid `65532`; ensure `/data` (and any bind mounts) are writable by that user.

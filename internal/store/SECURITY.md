# Security — `azzurrotech/atp/internal/store`

## Assets protected

- **The secrets vault.** Secret values are encrypted at rest with AES-256-GCM
  (`sealWith`/`openWith`). The key is `SHA-256(masterSecret)`; the master secret
  must be at least 32 bytes (`NewSecretsVault`). The on-disk form is
  `base64(nonce || ciphertext)` (`sealedSecret.Ciphertext`).
- **Client isolation data.** The registry defines each client's id, which is the
  same value used as the song silo name, the pod table prefix and the shepherd
  scope. Path building goes through `SanitizeSegment` and fixed subdirectories.
- **Usage and billing records.** Hourly rollups (`<root>/atp/usage/…`),
  request-log lines (`<root>/atp/logs/…`) and computed `Cost` values are the
  basis for billing.
- **Platform settings.** `Settings` (default price, retention, max upload,
  request-log limit) are persisted in `clients.json`.

The admin session itself belongs to `azzurrotech/atp/internal/auth` and
`azzurrotech/atp/web`.

## Threat model and mitigations actually implemented

- **Plaintext secret exposure.** Values are sealed before they hit disk; `Set`
  returns a `Secret` whose `Value` is empty, and `List` omits values entirely.
  The test `TestSecretEncryptedAtRest` asserts the plaintext never appears in
  the sealed file.
- **Weak/absent encryption.** AES-256-GCM with a fresh random 12-byte nonce per
  `sealWith` call (`crypto/rand`) provides confidentiality and integrity; a
  tampered ciphertext fails to open.
- **Master-secret length.** `NewSecretsVault` and `Rotate` reject secrets shorter
  than 32 bytes. `Rotate` re-encrypts every stored value under a new key, so key
  changes do not require deleting data.
- **Path traversal via ids and names.** Client ids must match the web-safe regex
  and are checked against `reservedClientIDs` (`ValidateClientID`); client-derived
  file names pass through `SanitizeSegment` (`/`, `\`, `..`, NUL replaced).
- **Torn/corrupt JSON on crash.** Writes go through `atomicWrite` (temp file in
  the target directory, then `os.Rename`) so readers never observe a half-written
  file.
- **Concurrent mutation.** Every store guards its map with a mutex; `Get`/`Create`
  return copies so callers cannot mutate shared state.
- **Retention as data minimisation.** `LogStore.Prune` deletes whole daily log
  files beyond the retention window and `UsageStore.Prune` trims hourly history,
  so metadata does not accumulate indefinitely.

## Authentication and authorization model

`store` has no authentication of its own. It assumes the caller (`web`) has
already enforced the single-admin session and the per-client scoping before
invoking store methods. Ownership is expressed purely by the `client` argument:
`SecretsVault.Set/Get/List/Delete`, `UsageStore.*` and `LogStore.*` operate on
one client at a time, and there is no cross-client query API.

## Honest limits / non-claims

- **No encryption of the client registry, logs or usage files.** Only secret
  *values* are encrypted; `clients.json`, `usage/*.json` and `logs/*.log` are
  plaintext JSON, readable by anyone with filesystem access to the root.
- **No key separation.** The AES key is a single SHA-256 of the master secret.
  The same master secret also signs shepherd capability tokens (`web`), so
  compromise of one use affects the other.
- **No re-keying on its own.** `Rotate` must be called explicitly; changing the
  master secret without rotating leaves existing files undecryptable.
- **Filesystem permissions are conventional.** Directories are created `0o755`
  and log files `0o644`; the package does not tighten them. Operators must
  restrict access to the data root themselves.
- **Best-effort persistence.** Usage and log write errors are intentionally
  ignored (`_ = err`) so a failing disk cannot break request handling; unsaved
  live usage can be lost on crash.
- **Metadata, not bodies, in request logs.** `LogStore` records method, path,
  status, byte counts, duration, IP and a 200-byte user agent — never request or
  response bodies, headers or cookies. See the `web` `SECURITY.md` for the log
  field list.
- **`remote_ip` is client-supplied when behind no trusted proxy.** `web` derives
  it from `X-Forwarded-For` without validating the sender.

## Dependency note

`store` imports the Go standard library only (`encoding/json`, `os`,
`path/filepath`, `regexp`, `sort`, `strings`, `sync`, `time`, `bufio`,
`crypto/aes`, `crypto/cipher`, `crypto/rand`, `crypto/sha256`,
`encoding/base64`, `fmt`). It has no third-party or in-module dependencies.

## Reporting

Report suspected vulnerabilities privately to **security@azzurro.tech**. Do not
open a public issue or pull request describing the problem.

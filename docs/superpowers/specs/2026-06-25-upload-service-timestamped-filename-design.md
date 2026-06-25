# Upload-service: Timestamped Drive filenames

**Date:** 2026-06-25
**Service:** `upload-service`
**Status:** Approved

## Problem

Drive rejects an upload when an object with the same `fileName` already exists
in a group. Re-uploading the same file (across requests) therefore fails. We
need each upload to land in Drive under a unique name, while the client still
sees the original filename in the response.

## Solution

Send a timestamped filename to Drive; return the original (timestamp-free) name
to the client. Applies to both handlers that call `drive.UploadGroupImages`:
`HandleUploadImages` (bulk) and `HandleUploadFile` (single file).

### Filename transform

New unexported helper in `upload-service/handler.go`:

```go
// timestampedName inserts a millisecond timestamp before the file extension:
// "photo.png" -> "photo_1719312000000.png". The extension is preserved so
// Drive's content-type sniffing is unaffected; a name with no extension just
// gets the suffix appended ("README" -> "README_1719312000000").
func timestampedName(name string, milli int64) string
```

- Split on `filepath.Ext`, insert `_<milli>` between base and extension.
- Multi-dot names (`a.tar.gz`) keep only the final extension as the ext, which
  matches `filepath.Ext` semantics — acceptable.

### Clock injection (testability)

Add a `nowMilli func() int64` field to `Handler`, defaulted in `NewHandler` to
`func() int64 { return time.Now().UTC().UnixMilli() }`. `NewHandler`'s signature
is unchanged. Tests live in `package main` and override `h.nowMilli` directly for
deterministic assertions, so no existing call site changes.

### HandleUploadFile (single file)

The response `name` already uses the original `fh.Filename`, so only the upload
path changes:

- Send `timestampedName(fh.Filename, h.nowMilli())` as the `MultipartFile.Filename`
  to `UploadGroupImages`.
- `meta.name` stays `fh.Filename`. No strip-back required.

### HandleUploadImages (bulk)

The response `Name` currently echoes Drive's `resp.File.Filename`, which would be
the timestamped name. To return originals, track them:

- `preprocessFiles` builds `MultipartFile`s with timestamped names **and**
  returns a `map[string]string` mapping timestamped name → original name. It
  takes the current `milli` (computed once per request by the caller) so all
  files in a batch share a consistent timestamp source.
- In the response loop, resolve the original via
  `orig := origBySent[resp.File.Filename]`, falling back to `resp.File.Filename`
  if the key is absent (defensive). Status / error / relativePath logic is
  unchanged.

Keying the map on the name we send (echoed back by Drive) makes the mapping
robust to any reordering of Drive's response items.

## Edge cases

- **No extension:** suffix appended to the whole name.
- **Identical filenames in one bulk request, same millisecond:** would still
  collide. This is out of scope — the reported bug is re-uploads across separate
  requests. Documented here; per-file indexing can be added later if needed.
- **Rejected files** (size/type/open failures in `preprocessFiles`) never reach
  Drive and already report the original `fh.Filename` — unchanged.

## Testing (TDD: Red → Green → Refactor)

- `timestampedName`: table-driven — extension, no extension, dotfile
  (`.gitignore`), multi-dot (`a.tar.gz`), with a fixed `milli`.
- `HandleUploadFile`: fake drive captures the uploaded `Filename`; assert it is
  the timestamped form while the response attachment `name` is the original.
- `HandleUploadImages`: fake drive captures uploaded names; assert Drive receives
  timestamped names and each response item `Name` is the original. Cover mixed
  success/failure and the existing per-file-rejection paths.

## Out of scope / no change

- `docs/client-api.md`: response schema is unchanged (still the original name),
  and upload-service is neither a `chat.user.` NATS handler nor an auth-service
  HTTP route, so no update is required.
- `pkg/drive`: no change — the timestamp is applied by the handler before calling
  `UploadGroupImages`, keeping the Drive client a thin transport.

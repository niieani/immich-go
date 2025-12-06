# Google Photos Takeout Import Notes

These notes capture the workflow we discussed for importing a very large Google Photos Takeout into Immich with `immich-go`, including how to upgrade lower-quality exports later.

## Source Layout
- All Google Takeout ZIP parts live at `/Volumes/Backups26/2025/Google Photos Takeout`.
- Files share a timestamp suffix such as `takeout-20250112T093000Z-001.zip`, `takeout-20250112T093000Z-002.zip`, etc.
- Keep the ZIP format; `immich-go upload from-google-photos` can stream directly from the archives.

## Pre-checks
1. Confirm every part of each takeout set exists (e.g., `ls takeout-*.zip | wc -l`). Missing parts mean missing media/JSON.
2. Optional: set a fast temp dir if the cache disk is slow: `export IMMICHGO_TEMPDIR=/fast/ssd/tmp`.
3. Ensure you have an Immich API key (plus admin key if you plan to pause background jobs).

## Chunking the Import
Because the takeout exceeds 1 TB, run several batches instead of one monolithic upload.

### Option A: Process one timestamp at a time (with Mio defaults baked in)
First, load the same env vars Mio uses:
```
source .env.production  # sets IMMICH_URL, IMMICH_API_KEY, IMMICH_ADMIN_API_KEY
```

```
cd "/Volumes/Backups26/2025/Google Photos Takeout"
LOG=~/mio-logs/immich-upload-$(date +%Y%m%d-%H%M%S).jsonl

immich-go upload from-google-photos \
  --server="$IMMICH_URL" \
  --api-key="$IMMICH_API_KEY" \
  --admin-api-key="$IMMICH_ADMIN_API_KEY" \
  --concurrent-tasks=3 \
  --client-timeout=60m \
  --on-server-errors=continue \
  --session-tag --tag import/takeout-2025 --tag-via-sidecar \
  --manage-burst Stack --manage-heic-jpeg StackCoverHeic --manage-raw-jpeg StackCoverJPG \
  --checksum-cache /Volumes/Backups26/2025/Google\ Photos\ Takeout/.immich-go-checksum-cache \
  --ban-file .Spotlight-V100/ --ban-file .fseventsd/ --ban-file .Trashes/ --ban-file .TemporaryItems/ \
  --ban-file .DocumentRevisions-V100/ --ban-file .DS_Store --ban-file /._ --ban-file desktop.ini \
  --ban-file thumbs.db --ban-file $RECYCLE.BIN/ --ban-file "System Volume Information/" \
  --log-type JSON --log-level DEBUG --log-file "$LOG" \
  takeout-20250112T093000Z-*.zip
```
Run once per timestamp (takeout run) until all sets finish. Adjust tag/album/date-range as needed; increase `--concurrent-tasks` only after verifying server headroom.

### Flag notes
- `--concurrent-tasks=4`: balanced throughput for large archives.
- `--pause-immich-jobs=true`: reduces load while ingesting (needs admin key).
- `--on-server-errors=continue`: don’t abort on transient server issues.
- `--session-tag`: tags every imported item with the session timestamp so each batch is traceable.
- Keep defaults `--sync-albums=true` and `--takeout-tag=true` to preserve Google organization/tagging.
- Add `--include-unmatched` in a follow-up run if you must ingest media missing JSON metadata.

## Borrow good defaults from Mio’s `upload` integration
When we spawn `immich-go` from Mio we add a few flags that make later auditing and restarts easier. They’re useful for standalone Takeout runs too:

- Log everything in structured form: `--log-type JSON --log-level DEBUG --log-file <path>.jsonl` (Mio tails this live; you can replay the JSONL later).
- Tag each run: `--session-tag` plus an explicit `--tag import/<label>` to distinguish batches; keep `--tag-via-sidecar` so IPTC/XMP keywords travel through JSON sidecars.
- Normalise stacks: `--manage-burst Stack --manage-heic-jpeg StackCoverHeic --manage-raw-jpeg StackCoverJPG`.
- Avoid OS junk: add the same ban list Mio applies when walking volumes: `--ban-file .Spotlight-V100/ --ban-file .fseventsd/ --ban-file .Trashes/ --ban-file .TemporaryItems/ --ban-file .DocumentRevisions-V100/ --ban-file .DS_Store --ban-file /._ --ban-file desktop.ini --ban-file thumbs.db --ban-file $RECYCLE.BIN/ --ban-file "System Volume Information/"` (safe even when reading extracted folders instead of ZIPs).
- Enable resumability: keep a cache near the source, e.g. `--checksum-cache /Volumes/Backups26/2025/Google\ Photos\ Takeout/.immich-go-checksum-cache`.
- Concurrency: Mio pins to `--concurrent-tasks=3` for steadier throughput on NAS/SSD mixes; bump only after watching server load.
- Admin toggle: if you **don’t** have `--admin-api-key`, explicitly pass `--pause-immich-jobs=false` so Immich background jobs stay running (Mio does this when the admin key is absent).
- Album targeting: add `--into-album "Google Takeout 2025"` when you want everything grouped on arrival.
- Date scoping: `--date-range YYYY` or `YYYY-MM-DD,YYYY-MM-DD` (Mio also supports a `--continue` helper that derives a range from the last run; replicate manually by setting start/end).

Use the Option A command above as the template; it already folds in these defaults. Adjust tag/album/range as needed; the rest mirrors what Mio uses to keep uploads traceable, restartable, and sidecar-friendly.

## Monitoring & Recovery
- Logs land under `~/Library/Caches/immich-go/immich-go_YYYY-MM-DD_HH-MM-SS.log` on macOS.
- Ctrl+C safely stops after in-flight uploads; re-running the same command is safe because duplicates are skipped.
- If Immich storage is slow (NAS/HDD), drop concurrency to 2; if server is powerful/SSD, you can raise to 6–8 after confirming stability.

## Replacing Lower-Quality Google Exports Later
Google Photos may have compressed versions. When full originals become available from another source, you can upgrade the Immich assets without losing metadata.

### Automatic replacements
If a new upload has the same filename and capture time (±5 seconds) but a larger filesize, `immich-go` automatically replaces the smaller server copy even without extra flags.

### Forced replacements with `--overwrite`
Use this when you want to guarantee a replacement even if the filesize heuristic isn’t conclusive.
```
immich-go upload from-folder \
  --server=http://your-immich:2283 \
  --api-key=IMMICH_API_KEY \
  --overwrite \
  --session-tag \
  /path/to/originals
```
Requirements:
- Filenames must match the Immich originals (or rename to match before uploading).
- Capture dates must match (within 5 seconds). If the originals have drifted timestamps, normalize them first.

### Metadata retention
During replacement `immich-go` uploads the new asset, copies metadata (albums, tags, descriptions, people labels, GPS, capture time) from the old asset, and deletes the old file on the server. Google JSON-derived information therefore persists even though the file content changes.

### Safety notes
- Reruns are idempotent—already upgraded assets are detected and skipped.
- If you only want to upgrade a subset (e.g., a specific album/year), point `from-folder` at just those originals; Immich-go will upgrade matching assets and ignore everything else.

## Optional tweaks
- Disable partner/shared or trashed imports with `--include-partner=false` / `--include-trashed=false` as desired.
- RAW+JPEG stacking example: add `--manage-raw-jpeg=StackCoverRaw` to keep stacks tidy.
- For identifying imported batches later, keep `--takeout-tag=true` so each set gets a `{takeout}/takeout-YYYYMMDDTHHMMSSZ` tag.

Use this doc as the canonical playbook whenever you need to re-ingest or upgrade Google Takeout data with `immich-go`.

# Media Library

A small Go and SQLite media viewer. It scans one media folder, uses immediate child folders as categories, stores metadata, serves videos and images through a Go HTTP server, and provides a Vue frontend for playback, image viewing, title edits, and category tags.

## Setup

```bash
go mod download
npm install
npm run build
go run .
```

Run the frontend regression tests with `npm test`. Run the backend suite with `go test ./...` after building the frontend.

Open `http://127.0.0.1:5000`.

For frontend-only development, run the Go server in one terminal and Vite in another:

```bash
npm run dev
```

Vite proxies `/api`, `/media`, and `/thumb` requests to the Go server on port 5000.

Configure the video folder with `VIDEO_ROOT`:

```bash
VIDEO_ROOT=/path/to/videos go run .
```

The server scans `VIDEO_ROOT` on startup. The web UI can trigger another scan, but the folder is configured only through the environment variable.

The production frontend is embedded into the Go binary from `frontend/dist`. Run `npm run build` before `go run .`, `go test ./...`, or `go build` so the embedded files exist and are current.

Set `APP_PASSWORD` to require a password before the library can be used:

```bash
VIDEO_ROOT=/path/to/videos APP_PASSWORD='change-me' go run .
```

If `APP_PASSWORD` is unset and passkeys have never been configured or enrolled, the main login is disabled; category passwords still apply. Once credentials exist, removing `APP_PASSWORD` does not remove login protection.

### Passkey login (one owner)

Configure exact allowed origins as a comma-separated list, without paths or trailing slashes. Replace the example domain with your actual HTTPS domain:

```bash
VIDEO_ROOT=/path/to/videos \
APP_PASSWORD='your-initial-password' \
APP_AUTH_ORIGINS='https://video.example.com,http://localhost:5000' \
go run .
```

For Vite development, also explicitly add `http://localhost:5173`. Use `localhost`, rather than `127.0.0.1` or a LAN IP, for local passkeys. Localhost still requires login; only its HTTPS requirement is relaxed. A device visiting localhost connects to itself, so use an SSH tunnel if accessing the server locally from another computer.

1. Sign in with your existing password and open **Security settings**.
2. Name and add a passkey on your HTTPS domain. Your device must verify you with its biometric, PIN, or security key. No username is needed.
3. Add a backup passkey. If you need localhost access, sign in there with your password and enroll a separate localhost passkey before disabling passwords. Domain credentials do not work on localhost.
4. Use **Verify with a passkey** on the hostname where you are changing settings, then explicitly select **Disable password login**. This disables passwords everywhere. Enrollment alone never disables passwords.

Security settings list credentials by hostname. Removing credentials or changing password policy requires authentication within the last five minutes on the current origin. Disabling passwords specifically requires passkey authentication. You cannot remove the final passkey while password login is unavailable. Removing a credential or changing password policy revokes other sessions.

Existing cookies from the former password-based login stop working after upgrading; sign in again. New sessions last 30 days, persist across restarts, and are individually revoked on sign-out. Sessions use a random database-backed secret independent of `APP_PASSWORD`; changing that environment variable alone does not revoke sessions. Category passwords are unaffected.

Keep your SQLite database backed up using a consistent SQLite backup (or stop the service before copying the database and any WAL files). It contains the owner's public credentials, login policy, and session secret; protect backups like other server secrets. Private passkey keys remain with your authenticator or passkey provider. Database restoration restores the saved login state too. A hostname change requires new enrollment on that hostname.

Keep TLS termination at your existing reverse proxy. Preserve the browser's `Host` header, proxy `/api`, `/media`, and `/thumb` along with the frontend, and configure the public HTTPS origin exactly. Authentication does not trust `X-Forwarded-Host` or `X-Forwarded-Proto` to select origins or relying-party IDs. HTTPS session cookies are Secure; HTTP localhost cookies remain usable locally. Restrict direct backend network exposure according to your deployment.

If all passkeys are lost, stop the service and run recovery locally as the service user, using the same database:

```bash
sudo systemctl stop video-server
sudo -u video-server env VIDEO_DB=/opt/video-server/videos.sqlite3 \
  APP_PASSWORD='replacement-password' /opt/video-server/video-server auth-recover
```

Set the same replacement `APP_PASSWORD` in the service configuration, reload systemd, and restart the service. Recovery refuses to run while the database is locked by the server, re-enables passwords, rotates the session secret, and invalidates all sessions. Pending challenges are in memory and disappear when the server stops. Existing passkeys are retained so you can remove lost keys after signing in. An initial nonempty `APP_PASSWORD` is required when configuring passkeys for the first time.

Manage privacy in a category using **Hide category**, **Change password**, and **Unhide category**. Hidden categories appear only in the separate **Hidden categories** area. Unlocking lasts 12 hours; **Lock again** ends access in this browser. Changing a password invalidates existing unlock cookies. Changing passwords and unhiding require the current category password; there is no main-password recovery. Unhiding removes protection.

For legacy installations, `APP_LOCKED_CATEGORIES` supports `category=password` pairs separated by semicolons:

```bash
VIDEO_ROOT=/path/to/videos \
APP_PASSWORD='main-login' \
APP_LOCKED_CATEGORIES='private=vault;family=second-secret' \
go run .
```

Legacy passwords and inherited category locks are imported once into SQLite as salted PBKDF2-SHA256 hashes. Later web changes take precedence over environment settings, including after restart. Keep the database backed up. Hidden categories can be renamed after unlocking; protection follows the rename. Media lists, thumbnails, streams, and metadata operations require category access.

Install `ffmpeg` if you want generated thumbnails for videos:

```bash
sudo apt install ffmpeg
```

The scanner reads supported media files at the library root and directly inside category folders. Categories are single folder names. Nested directories are preserved, excluded from indexing, and reported as scan warnings. Uncategorized represents files at the library root.

In the viewer, videos automatically advance when playback ends. Previous and next controls navigate media within the current filtered view.

Open **Media Details → Check iPhone compatibility** to inspect a selected video with server-side `ffprobe` (included with FFmpeg). The check warns when an H.264 stream declares a level too low for its resolution or frame rate, a possible cause of iPhone playback failure. It does not modify files or guarantee playback: bitrate, reference frames, audio, file integrity and HTTP delivery are not validated. Unsupported codecs or unreadable metadata produce an inconclusive result.

From the main category page, **Shuffle all videos** starts playback across all unlocked categories and clears existing search and media filters.

Enable **Shuffle videos** in the media-list toolbar to play videos from the current filtered view in random order, skipping images. Each video plays once per cycle before reshuffling; Previous retraces the current shuffle session. A single eligible video stops at its end. The shuffle preference is remembered in this browser, while the queue and history reset on reload or filter changes.

Playback progress and history are no longer stored. On startup, existing databases have their legacy playback progress, duration, last-played, and category last-reproduced columns removed. Other media metadata is preserved.

The media list shows thumbnails. Images use the image file directly. Videos use `ffmpeg` to generate cached JPEG thumbnails on first request. Set `VIDEO_THUMB_DIR` to choose the cache folder; it defaults to `thumbnails`.

Example:

```text
/srv/videos/
  clip-a.mp4              -> Uncategorized
  cover.jpg               -> Uncategorized
  travel/
    beach.mp4             -> travel
    beach.png             -> travel
  family/
    birthday.mp4          -> family
  archive/old.mp4         -> archive
  archive/older/clip.mp4  -> ignored (scan warning)
```

You can also change a video's category from the web interface. Saving a new category physically moves the file into that folder under `VIDEO_ROOT`. Saving `Uncategorized` moves it back to the base folder. Category names must be single folder names, not paths. If a filename already exists in the target folder, the app appends `_1`, `_2`, and so on.

Category names can be renamed from the category controls. This renames the backing folder and updates active indexed media in that category. `Uncategorized` cannot be renamed because it represents files stored directly in `VIDEO_ROOT`.

## Library Management

### Find visually similar media

Use Python 3.10+ with `ffmpeg` and `ffprobe` installed:

```bash
python3 scripts/find_similar_media.py /path/to/videos
python3 scripts/find_similar_media.py /path/to/videos --recursive --json > similar-media.json
```

This read-only script finds candidate pairs despite different SHA-256 hashes,
using 64-bit difference hashes (dHash) of decoded images and sampled video frames.
It shares the exact-duplicate script's extension filtering, folder scope, and
symlink exclusions. The path defaults to `VIDEO_ROOT` when omitted.

Videos must have durations within 2% and all five evenly spaced interior samples
must differ by at most six hash bits. Adjust `--samples`, `--threshold`, or
`--duration-tolerance` to tune matching. Lower thresholds are stricter. Images
use their first frame, including animated image formats. Images and videos are
compared separately; audio is not compared.

The similarity percentage describes hash agreement, not confidence that files
are duplicates. Review each pair before removing anything: blank frames, simple
scenes, or unsampled differences can cause false matches. Cropping, trimming,
time offsets, and substantial edits can cause missed matches. Matches are pairs,
not transitive groups. The script never deletes files or edits library metadata.

To review candidates before deleting, generate a JSON report with the command
above, open **Review duplicates** in the web viewer (the dedicated `/duplicates`
page), and import the report.
Images and playable videos appear side by side with filenames, sizes, and links
to open the original media. Each preview has a **Permanently delete this file**
button that immediately deletes that one copy, with no batch selection or extra
review step. The other file is retained. All pairs containing the deleted file
are removed and the next available pair appears automatically. Choose **Keep
both** to advance without deleting, or navigate with **Previous pair** and
**Next pair**. The page uses the full desktop width and stacks previews on mobile.

Only currently indexed, accessible files with paths matching the report are
shown. Unlock hidden categories and refresh the library before importing if
needed. Generate the report against the server's absolute media paths; reports
from another filesystem location will not match. Deletions use the existing
authenticated library API and remove database records as well as files. Failed
deletions keep the pair visible for retry. Imported reports remain only in memory
and reset when you leave or reload the page. Use **Back to library** to return.

Fingerprinting prints progress to stderr. JSON contains `scanned`, `matches`, and
`errors`; unreadable media are skipped and cause exit status 1. Each decode has a
60-second timeout. Comparisons grow quadratically with the number of files, so
scan smaller folders for large libraries. Avoid modifying files during a scan.

Run the regression checks with:

```bash
python3 -m unittest discover -s scripts -p test_find_similar_media.py
```

### Find exact duplicates

After deleting media with standalone scripts, run a library scan to mark absent
paths as missing. To remove those stale database records, first ensure the media
drive is mounted, finish the scan, then stop the server:

```bash
python3 scripts/cleanup_missing_media.py --db videos.sqlite3 --root /path/to/videos
python3 scripts/cleanup_missing_media.py --db videos.sqlite3 --root /path/to/videos --apply
```

The first command previews eligible records. `--apply` creates a timestamped
SQLite backup beside the database and removes records only. Existing paths are
preserved even if marked missing, and other library roots are untouched. Pending
recovery operations must be resolved by starting the server before cleanup.
The script accepts `VIDEO_DB` and `VIDEO_ROOT` defaults. Restart and scan again
after cleanup to refresh the displayed counts. Removed records lose their stored
titles and favorites; no media files are deleted by this cleanup.

Find exact duplicates regardless of filename with the standalone Python 3 script:

```bash
python3 scripts/dedupe_media.py /path/to/videos
python3 scripts/dedupe_media.py /path/to/videos --delete
```

The default is a preview. `--delete` permanently removes extra copies, keeping the
first relative path in alphabetical order in each SHA-256 group. Only supported
media extensions are included. Files are grouped by size before hashing; matching
contents are detected across filenames, extensions, and categories. Symlinks and
hidden directories are skipped. The default scope matches the library's root and
immediate category folders; add `--recursive` to include deeper folders. Omit the
path to use `VIDEO_ROOT`.

Stop the server and other processes that modify the media folder before deletion.
The script rechecks contents before removing each copy, but concurrent folder or
file changes are not supported. It does not edit SQLite or merge metadata: choose
which paths to retain before running it if titles, favorites, or category placement
matter. Restart the server or run a library scan afterward to mark deleted paths
as missing. Reported bytes are logical file sizes; hard links may free no space.

Select media to move files, set Favorites or Watch Later, or permanently delete files. Select all applies to the current filtered list. Sorting preserves selection; changing the folder, view, search, or filters clears it. Bulk operations report individual failures and allow retrying failed items. Moves preserve metadata and choose a suffixed filename when the destination name is occupied.

Compact reduces list spacing and persists in this browser.

Categories have editable tags. The category grid supports category-name search and category-tag filters with Match all or Match any. Filters persist in `category_tag` and `category_match` URL parameters and remain selected when returning from a category. Category-tag counts count accessible categories; locked tags are hidden until unlocking.

Open a category and select **Edit category tags** to enter one tag per line, including tags containing commas. This also works for Uncategorized and unlocked protected categories. Tags are stored as JSON arrays.

Completed folder migration mappings seed each category with its former parent's name once, including surviving parent categories. Existing category tags are preserved. Seed completion is recorded transactionally, so edited or removed tags are not regenerated by startup, scans, renames, or migration reruns. Categories without a migration mapping start with no tags. Renaming a category preserves its tags; removing a category removes its metadata.

To remove category tags that match their own category name (case-insensitive), preview with `python3 scripts/remove_self_category_tags.py --db /path/to/videos.sqlite3 --dry-run`. Stop the server, then replace `--dry-run` with `--apply`. Apply creates a SQLite backup beside the database, removes only self-matching category tags, and marks pending self-tag seeds as completed so they cannot regenerate those tags. Other category tags are preserved.

Deleting media permanently removes its file and database record. Category deletion permanently deletes indexed media only; unindexed contents are retained and reported. Confirmation dialogs explain that deletion cannot be undone. Authentication and category locks apply to all deletion actions.

Categories match the folders on disk, including folders containing only non-media files with zero media counts. Every startup scan and Scan Library action removes truly empty folders bottom-up, including descendants below the media indexing depth. The library root, symlinks, hidden files, unsupported files, and nonempty folders are preserved. Missing media records keep their metadata but do not retain categories for absent folders. Uncategorized always represents the root.

Categories can be renamed, carrying unindexed contents with them. Media can be moved individually or in bulk to another flat category. Nested category destinations and subcategory controls have been removed. Locked categories cannot be renamed.

File-changing operations and scans are serialized. Database failures after a file move trigger a move back to the original location. Permanent deletion uses a temporary staging file and durable recovery journal; interrupted deletions with files still staged are recovered at startup. Back up the SQLite database and library files together.

On upgrade, existing Trash files are restored before startup scanning. Their IDs and metadata are preserved, original folders are recreated, and occupied filenames receive numbered suffixes. Migration commits each restored item separately and can resume after interruption. A missing or inaccessible legacy file stops startup with its ID and path; recover the file or fix its permissions and restart. After migration, the Trash columns and API actions are removed. Any residual unindexed contents in `.video-library-trash` stay on disk for manual recovery and remain excluded from scans.

## Migrate Existing Categories

Stop the server, then preview and apply using the same database and library root:

```bash
./video-server migrate-categories --dry-run --db /path/to/videos.sqlite3 --root /srv/videos
./video-server migrate-categories --apply --db /path/to/videos.sqlite3 --root /srv/videos
```

`--db` and `--root` default to `VIDEO_DB` and `VIDEO_ROOT`. The command never starts HTTP serving. Preview opens the database read-only and changes nothing. Apply shares an exclusive database process lock with server startup; concurrent server or migration processes are rejected.

`Travel/Japan/clip.mp4` becomes `Japan/clip.mp4`. Category tags retain the former parent name (`Travel`) on the promoted category and surviving parent. Media IDs, filenames, metadata, and timestamps are retained. Missing records receive their planned new locations without creating folders. Supported unindexed media is included.

Existing destination names are reserved case-insensitively. Conflicting subcategory names use `Parent - Subcategory`, then `_2`, `_3`, and so on. Whole folders move, preserving unindexed contents. Deeper directories remain intact and generate warnings; their media remains unindexed. Symlinks and inaccessible contents block migration before folder moves.

The migration journals destinations before moving folders and commits each folder independently. Re-run `--apply` to resume after interruption or a resolved error. Successful steps are skipped, so rerunning never regenerates edited tags. Normal startup rejects an unmigrated hierarchy with instructions to run this command. Following migration, normal scans clean empty parent folders.

During the one-time protection import, promoted categories inherit locks from their former parent through stored name aliases. An explicit lock for a promoted category takes precedence. Passwords are stored only as salted hashes. Original subcategory browser links redirect using the saved migration mapping.

Legacy Trash restoration finishes before promotion. Missing legacy Trash files stop the migration with their ID and path.

The older `scripts/flatten_videos.sh` moves files to the root without preserving indexed metadata; use the binary migration above to upgrade an existing library.

## Linux Mint AMD64 Build

Build a deployable folder for a Linux Mint amd64 virtual machine:

```bash
./scripts/build_linux_mint.sh
```

The script installs frontend dependencies if needed, builds the frontend, and
embeds it into a Linux amd64 Go binary. Copy `dist/linux-mint-amd64` to the VM
and follow its `INSTALL.txt` to install ffmpeg and configure the systemd service.
Set your media folder and password before starting the service.

## Raspberry Pi Build

Build a deployable Raspberry Pi folder from this machine:

```bash
./scripts/build_raspberry_pi.sh arm64
```

Use `arm64` for Raspberry Pi OS 64-bit. Use `armv7` for 32-bit Raspberry Pi OS:

```bash
./scripts/build_raspberry_pi.sh armv7
```

The script builds the Vite frontend, embeds it into the Go binary, and writes the output to `dist/raspberry-pi-arm64` or `dist/raspberry-pi-armv7`. Copy that folder to the Pi and follow the generated `INSTALL.txt`.

An example systemd unit is included at `systemd/video-server.service.example`. Edit `VIDEO_ROOT=/srv/videos` to match your video folder before enabling the service.

The server uses Go's standard `net/http` router. If this grows beyond a small local tool, `github.com/go-chi/chi/v5` would be a good next framework because it stays close to the standard library while adding lightweight routing and middleware.

## API

- `GET /api/config`
- `GET /api/auth/status`
- `POST /api/auth/login`
- `POST /api/auth/passkeys/{register|login}/{begin|finish}`
- `GET /api/auth/passkeys`
- `DELETE /api/auth/passkeys/{id}`
- `PATCH /api/auth/password` (`{"enabled": true|false}`)
- `POST /api/auth/logout`
- `GET /api/videos`
- `GET /api/videos/<id>`
- `POST /api/scan` returns the existing scan fields plus `removed_folders`, `removed_categories`, and `cleanup_warnings`.
- `PATCH /api/videos/<id>` with `{ "title": "New title" }` and optional `category`, `favorited`, and `watch_later`
- `POST /api/videos/<id>/delete`
- `DELETE /api/videos/<id>` (permanently deletes the file and record)
- `POST /api/videos/bulk` with unique `ids` (maximum 1,000), `action`, and action parameters. Actions: `move` with explicit `category`; `favorited` or `watch_later` with boolean `value`; `delete` for permanent deletion. Returns `results` containing `id`, `success`, and `error` or updated `video`.
- `POST /api/categories/rename` with `{ "from": "old", "to": "new" }`
- `POST /api/categories/move` returns 410 because category nesting was removed.
- `POST /api/categories/delete` with `{ "category": "name" }` permanently deletes indexed media and returns `deleted`, `results`, and `folder_retained`
- `GET /media/<id>` serves the media file
- `GET /thumb/<id>` serves an image thumbnail

Supported video extensions: `.mp4`, `.m4v`, `.mov`, `.webm`, `.mkv`, `.avi`, `.wmv`, `.flv`, `.mpeg`, `.mpg`.

Supported image extensions: `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.bmp`, `.tif`, `.tiff`, `.avif`.

Compatibility: subcategory endpoints return 410, and obsolete subcategory request fields return 400. Media and folder deletion endpoints now permanently delete indexed files. Bulk clients must use `delete` instead of `trash`. The old Trash endpoints return 404; the `trash`, `restore`, and `purge` bulk actions return 400.
## Automatic Visual Category Tags

Run `python3 classifier/classify_media.py` on the media server to suggest tags
for existing categories from an allowed JSON tag list. The script examines
visual media inside each category using a local or remote Ollama vision model.
See [CLI setup and examples](classifier/README.md). It produces a report by
default; `--apply` adds new tags to categories while preserving existing tags.
It never assigns categories, moves files, or modifies media records.

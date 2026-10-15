# Media Library

A small Go and SQLite media viewer. It scans one media folder, uses immediate child folders as categories, stores metadata, serves videos and images through a Go HTTP server, and provides a Vue frontend for playback, image viewing, title edits, and tags.

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

If `APP_PASSWORD` is unset, password protection is disabled.

Set `APP_LOCKED_CATEGORIES` to require an extra password for specific categories after the main app login. Use `category=password` pairs separated by semicolons:

```bash
VIDEO_ROOT=/path/to/videos \
APP_PASSWORD='main-login' \
APP_LOCKED_CATEGORIES='private=vault;family=second-secret' \
go run .
```

Locked categories still appear in the category grid, but their media list, thumbnails, and streams stay blocked until the extra category password is entered. Locked category names are environment-driven, so renaming a locked category is intentionally blocked.

Install `ffmpeg` if you want generated thumbnails for videos:

```bash
sudo apt install ffmpeg
```

The scanner reads supported media files at the library root and directly inside category folders. Categories are single folder names. Nested directories are preserved, excluded from indexing, and reported as scan warnings. Uncategorized represents files at the library root.

In the viewer, videos automatically advance when playback ends. Previous and next controls navigate media within the current filtered view.

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

Select media to move files, add/remove/replace tags, set Favorites or Watch Later, or permanently delete files. Select all applies to the current filtered list. Sorting preserves selection; changing the folder, view, search, or filters clears it. Bulk operations report individual failures and allow retrying failed items. Moves preserve metadata and choose a suffixed filename when the destination name is occupied.

Tags opens an exact, case-insensitive tag browser across unlocked media. Select multiple tags with Match all or Match any; filters also work inside a category and combine with search, media type, sorting, Favorites, and Watch Later. Tag selections persist in the URL using repeated `tag` parameters and `match=any` (the default is all). Enter one tag per line when editing; commas within a tag are preserved. Tags are stored as JSON arrays, with older comma-separated values converted automatically. Compact reduces list spacing and persists in this browser.

Categories have their own editable tags, independent of video tags. The category grid supports category-name search and category-tag filters with Match all or Match any. Filters persist in `category_tag` and `category_match` URL parameters and remain selected when returning from a category. Category-tag counts count accessible categories; locked tags are hidden until unlocking.

Open a category and select **Edit category tags** to enter one tag per line, including tags containing commas. This also works for Uncategorized and unlocked protected categories. Saving category tags does not change any video tags.

Completed folder migration mappings seed each category with its former parent's name once, including surviving parent categories. Existing category tags are preserved. Seed completion is recorded transactionally, so edited or removed tags are not regenerated by startup, scans, renames, or migration reruns. Categories without a migration mapping start with no tags. Renaming a category preserves its tags; removing a category removes its metadata.

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

`Travel/Japan/clip.mp4` becomes `Japan/clip.mp4` with the tag `Travel`. Direct media in `Travel` keeps its folder and also receives that tag. Existing tags and media IDs, filenames, metadata, and timestamps are retained. Missing records receive their planned new locations without creating folders. Supported unindexed media is included.

Existing destination names are reserved case-insensitively. Conflicting subcategory names use `Parent - Subcategory`, then `_2`, `_3`, and so on. Whole folders move, preserving unindexed contents. Deeper directories remain intact and generate warnings; their media remains unindexed. Symlinks and inaccessible contents block migration before folder moves.

The migration journals destinations before moving folders and commits each folder independently. Re-run `--apply` to resume after interruption or a resolved error. Successful steps are skipped, so rerunning never regenerates edited tags. Normal startup rejects an unmigrated hierarchy with instructions to run this command. Following migration, normal scans clean empty parent folders.

Promoted categories inherit locks from their former parent through stored name aliases; no passwords are stored. Keep the original parent entries in `APP_LOCKED_CATEGORIES`. An explicit lock for a promoted category takes precedence. Original subcategory browser links redirect using the saved migration mapping.

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
- `POST /api/auth/logout`
- `GET /api/videos`
- `GET /api/videos/<id>`
- `POST /api/scan` returns the existing scan fields plus `removed_folders`, `removed_categories`, and `cleanup_warnings`.
- `PATCH /api/videos/<id>` with `{ "title": "New title", "tags": ["tag one", "tag two"] }`
- `POST /api/videos/<id>/delete`
- `DELETE /api/videos/<id>` (permanently deletes the file and record)
- `POST /api/videos/bulk` with unique `ids` (maximum 1,000), `action`, and action parameters. Actions: `move` with explicit `category`; `tags` with `tags` and `tag_mode` (`add`, `remove`, `replace`); `favorited` or `watch_later` with boolean `value`; `delete` for permanent deletion. Returns `results` containing `id`, `success`, and `error` or updated `video`.
- `POST /api/categories/rename` with `{ "from": "old", "to": "new" }`
- `POST /api/categories/move` returns 410 because category nesting was removed.
- `POST /api/categories/delete` with `{ "category": "name" }` permanently deletes indexed media and returns `deleted`, `results`, and `folder_retained`
- `GET /media/<id>` serves the media file
- `GET /thumb/<id>` serves an image thumbnail

Supported video extensions: `.mp4`, `.m4v`, `.mov`, `.webm`, `.mkv`, `.avi`, `.wmv`, `.flv`, `.mpeg`, `.mpg`.

Supported image extensions: `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.bmp`, `.tif`, `.tiff`, `.avif`.

Compatibility: subcategory endpoints return 410, and obsolete subcategory request fields return 400. Media and folder deletion endpoints now permanently delete indexed files. Bulk clients must use `delete` instead of `trash`. The old Trash endpoints return 404; the `trash`, `restore`, and `purge` bulk actions return 400.
## Automatic Visual Tags and Categories

Run `python3 classifier/classify_media.py` on the media server to tag and classify
indexed media from explicit JSON tag/category lists. The script calls a local or
remote Ollama vision model directly, with no classifier HTTP service or web UI.
See [CLI setup and examples](classifier/README.md). It produces a report by
default; `--apply` replaces tags, moves files, and updates SQLite automatically.

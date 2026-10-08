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

The scanner reads supported media files at the library root, inside categories, and inside their immediate subcategories. Categories and subcategories are single folder names; a third folder level is ignored. Uncategorized represents files at the library root and cannot have subcategories.

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
  family/archive/old.mp4  -> family / archive
  family/archive/older/clip.mp4 -> ignored
```

You can also change a video's category from the web interface. Saving a new category physically moves the file into that folder under `VIDEO_ROOT`. Saving `Uncategorized` moves it back to the base folder. Category names must be single folder names, not paths. If a filename already exists in the target folder, the app appends `_1`, `_2`, and so on.

Category names can be renamed from the category controls. This renames the backing folder and updates active indexed media in that category. Trashed items retain their original restoration location. `Uncategorized` cannot be renamed because it represents files stored directly in `VIDEO_ROOT`.

## Library Management

Select media to move files, add/remove/replace tags, set Favorites or Watch Later, or move files to Trash. Select all applies to the current filtered list. Sorting preserves selection; changing the folder, view, search, or filters clears it. Bulk operations report individual failures and allow retrying failed items. Moves preserve metadata and choose a suffixed filename when the destination name is occupied.

Tags opens an exact, case-insensitive tag browser across unlocked media. Compact reduces list spacing and persists in this browser.

Deleting media or a category now moves indexed files to `VIDEO_ROOT/.video-library-trash/<media-id>/`, preserving metadata and original locations. Trash has no automatic expiry. Restore recreates the original folders or uses a chosen destination; occupied filenames receive `_1`, `_2`, and so on. Permanent deletion and Empty Trash cannot be undone. Empty Trash only deletes items whose categories are currently accessible.

The trash directory is reserved and excluded from scans. Trash cannot be streamed or classified. Missing trash files remain listed with a restore error and can still be permanently removed from the index. Category locks continue to apply to trashed files, and zero-count categories remain available for unlocking and restoration.

Subcategories can be renamed, moved to another category, promoted to a top-level category, or trashed. Folder moves reject existing destinations and unsupported nesting. Renaming or moving a folder carries unindexed contents with it. Trashing a folder only affects indexed media and removes the folder only if it becomes empty; unindexed contents are retained and reported. Locked categories cannot be renamed or moved across category boundaries.

File-changing operations and scans are serialized. Database failures after a file move trigger a move back to the original location; failures to restore the original path are reported and logged. Back up both the SQLite database and library files together, including the reserved trash directory.

## Flatten Subfolders

To move supported media files from subfolders into the base folder:

```bash
./scripts/flatten_videos.sh /path/to/videos
```

The script moves media files from subfolders only. If a filename already exists in the base folder, it appends `_1`, `_2`, and so on.

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
- `POST /api/scan`
- `PATCH /api/videos/<id>` with `{ "title": "New title", "tags": ["tag one", "tag two"] }`
- `POST /api/videos/<id>/delete`
- `DELETE /api/videos/<id>` (also moves to Trash)
- `POST /api/videos/bulk` with unique `ids` (maximum 1,000), `action`, and action parameters. Actions: `move` with explicit `category` and `subcategory`; `tags` with `tags` and `tag_mode` (`add`, `remove`, `replace`); `favorited` or `watch_later` with boolean `value`; `trash`; `restore` with optional destination; `purge` for trashed items only. Returns `results` containing `id`, `success`, and `error` or updated `video`.
- `GET /api/trash`
- `POST /api/trash/<id>/restore` with optional `category` and `subcategory`
- `DELETE /api/trash/<id>` permanently deletes a trashed item
- `POST /api/trash/empty` permanently deletes accessible trashed items and returns per-item results
- `POST /api/categories/rename` with `{ "from": "old", "to": "new" }`
- `POST /api/categories/move` with `{ "from": "source", "target": "destination" }` (flat categories only)
- `POST /api/categories/delete` with `{ "category": "name" }` moves indexed media to Trash and returns `deleted`, `results`, and `folder_retained`
- `POST /api/subcategories/rename` with `{ "category": "Travel", "subcategory": "Trips", "name": "Journeys" }`
- `POST /api/subcategories/move` with `category`, `subcategory`, `target_category`, and `target_subcategory`; an empty target subcategory promotes the folder to a new top-level category
- `POST /api/subcategories/delete` with `category` and `subcategory`; returns per-item results and `folder_retained`
- `GET /media/<id>` serves the media file
- `GET /thumb/<id>` serves an image thumbnail

Supported video extensions: `.mp4`, `.m4v`, `.mov`, `.webm`, `.mkv`, `.avi`, `.wmv`, `.flv`, `.mpeg`, `.mpg`.

Supported image extensions: `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.bmp`, `.tif`, `.tiff`, `.avif`.

Compatibility: the existing media/category delete endpoints now preserve files in Trash rather than deleting them permanently. Clients needing permanent deletion must first trash an item, then use a Trash deletion endpoint.
# Visual Classification

Optional visual classification runs on a separate GPU machine. See
[classifier setup and evaluation](classifier/README.md) for model configuration,
Docker deployment, authentication, and benchmarking. Set `CLASSIFIER_URL` and
`CLASSIFIER_TOKEN` on this server and install FFmpeg/FFprobe. Parent categories
offer a classification action and approval queue; approved destinations move
files into subfolders.

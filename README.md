# Media Library

A small Go and SQLite media viewer. It scans one media folder, uses immediate child folders as categories, stores metadata, serves videos and images through a Go HTTP server, and provides a Vue frontend for playback, image viewing, title edits, and tags.

## Setup

```bash
go mod download
go run .
```

Open `http://127.0.0.1:5000`.

Configure the video folder with `VIDEO_ROOT`:

```bash
VIDEO_ROOT=/path/to/videos go run .
```

The server scans `VIDEO_ROOT` on startup. The web UI can trigger another scan, but the folder is configured only through the environment variable.

Set `APP_PASSWORD` to require a password before the library can be used:

```bash
VIDEO_ROOT=/path/to/videos APP_PASSWORD='change-me' go run .
```

If `APP_PASSWORD` is unset, password protection is disabled.

The scanner reads supported media files directly inside the selected folder and inside immediate child folders. Child folder names become categories. Deeper nested folders are ignored.

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
  family/archive/old.mp4  -> ignored
```

You can also change a video's category from the web interface. Saving a new category physically moves the file into that folder under `VIDEO_ROOT`. Saving `Uncategorized` moves it back to the base folder. Category names must be single folder names, not paths. If a filename already exists in the target folder, the app appends `_1`, `_2`, and so on.

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

The output is written to `dist/raspberry-pi-arm64` or `dist/raspberry-pi-armv7`. Copy that folder to the Pi and follow the generated `INSTALL.txt`.

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
- `GET /media/<id>` serves the media file

Supported video extensions: `.mp4`, `.m4v`, `.mov`, `.webm`, `.mkv`, `.avi`, `.wmv`, `.flv`, `.mpeg`, `.mpg`.

Supported image extensions: `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.bmp`, `.tif`, `.tiff`, `.avif`.

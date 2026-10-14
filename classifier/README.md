# Standalone Visual Media Classifier

Run this Python 3.10+ script on the Linux media server. It reads the existing app
SQLite database and media files, extracts JPEG previews with FFmpeg, and calls
an Ollama vision model directly. Ollama can run on this server or a separate GPU
machine. The web app and the former classifier HTTP adapter are not involved.
No Python packages need installation.

Install FFmpeg/FFprobe and use an Ollama model that supports vision and structured
outputs. Set `CLASSIFIER_MODEL` or supply `--model` with the exact installed model
tag. See [Ollama's visual structured-output example](https://github.com/ollama/ollama-python/blob/main/examples/structured-outputs-image.py).
Choose the model using representative images and videos on your hardware.

## Configure the Ollama Server

These steps target a Linux GPU host using systemd, including Ubuntu/Linux Mint.
Run them on the machine that will perform inference. For supported hardware and
drivers, consult [Ollama's GPU guide](https://docs.ollama.com/gpu).

### Install and Start

Install Ollama using its [official Linux installer](https://docs.ollama.com/linux):

```sh
curl -fsSL https://ollama.com/install.sh | sh
sudo systemctl enable --now ollama
systemctl status ollama --no-pager
curl --fail http://127.0.0.1:11434/api/tags
```

For NVIDIA hardware, verify the installed driver with `nvidia-smi`. Install
FFmpeg on the **media server**, where previews are extracted:

```sh
sudo apt update
sudo apt install ffmpeg
ffmpeg -version
ffprobe -version
```

### Install a Vision Model

For an initial test, [Gemma 3 4B](https://ollama.com/library/gemma3:4b) supports
image input. Its suitability depends on your GPU memory and classification
accuracy; evaluate it on your media before a large automatic batch.

```sh
ollama pull gemma3:4b
ollama list
export CLASSIFIER_MODEL='gemma3:4b'
```

Set `CLASSIFIER_MODEL` on the **media server** too, or pass `--model gemma3:4b`
to the script. Pulling a model installs it on the Ollama host; it does not copy
the model to the media server.

### Connect from Another Server

When both processes run on the same machine, keep Ollama's default localhost
binding and use `--ollama-url http://127.0.0.1:11434`.

For a separate GPU host, configure a systemd override using
[Ollama's environment-variable instructions](https://docs.ollama.com/faq):

```sh
sudo systemctl edit ollama.service
```

Add this inside the override's editable section. Replace the example address
with the GPU host's private network address:

```ini
[Service]
Environment="OLLAMA_HOST=192.168.1.50:11434"
```

```sh
sudo systemctl daemon-reload
sudo systemctl restart ollama
systemctl cat ollama
curl --fail http://192.168.1.50:11434/api/tags
```

Ollama's local API has no built-in authentication. Restrict access to the media
server's IP through your existing firewall; do not expose port 11434 publicly.
For a host already using UFW, an example rule is:

```sh
sudo ufw allow from 192.168.1.20 to any port 11434 proto tcp
```

Replace `192.168.1.20` with the media server's private IP. This rule alone does
not remove existing broad rules or establish a default-deny firewall policy.
For access outside a trusted private network, use a VPN or a TLS reverse proxy
with authentication. `OLLAMA_TOKEN` is a client bearer token for such a proxy;
setting it does not add authentication to Ollama itself. `OLLAMA_ORIGINS` is not
needed for this Python client.

On the **media server**, verify connectivity and configure the client:

```sh
curl --fail http://192.168.1.50:11434/api/tags
export OLLAMA_URL='http://192.168.1.50:11434'
export CLASSIFIER_MODEL='gemma3:4b'
python3 classifier/classify_media.py \
  --db videos.sqlite3 --tags classifier/tags.example.json --limit 1
```

The last command performs a visual test without changing files or tags. After
inference, run `ollama ps` on the GPU host to inspect its CPU/GPU allocation.
For connection failures, check the service, private address and firewall. For
missing-model errors, check `ollama list` on the GPU host and use the exact tag.
For timeouts, try `--frames 2 --timeout 300` and inspect GPU memory usage.
View service errors with `journalctl -u ollama -n 100 --no-pager`.

## Lists

Supply UTF-8 JSON arrays. The model can select only these exact values:

```json
["Nature/Forests", "Nature/Beaches", "People", "Uncategorized"]
```

Category labels are `Category` or `Category/Subcategory`. `Uncategorized` means
the media root. New listed folders are created when a file is assigned there.
The model never invents categories. An ambiguous category result is `null`,
which leaves the current location unchanged.

```json
["forest", "beach", "people", "indoors", "vehicles"]
```

Tags support multiple matches. Existing tags are **replaced** with selected tags
by default; no matches clears them. Use `--tag-mode add` to preserve existing tags.
Example lists are included in this directory; edit copies to match your library.

## Run

First scan the library in the web app so its file metadata is current. Preview:

```sh
python3 classifier/classify_media.py \
  --db /srv/video_server/videos.sqlite3 \
  --categories classifier/categories.example.json \
  --tags classifier/tags.example.json \
  --model your-vision-model:tag \
  --ollama-url http://GPU_HOST:11434 \
  --limit 10 --report /tmp/media-preview.jsonl
```

For automatic application, stop the web app, run the same command with `--apply`,
then restart it. The process directly updates SQLite and moves files; stopping
the app avoids concurrent scans and file edits during a move. A database-adjacent
lock file prevents simultaneous CLI runs. Back up the database before a first
large batch. Offline execution uses the server account's filesystem access,
including any categories locked in the browser.

```sh
python3 classifier/classify_media.py \
  --db /srv/video_server/videos.sqlite3 \
  --categories /srv/video_server/categories.json \
  --tags /srv/video_server/tags.json \
  --model your-vision-model:tag --apply \
  --report /tmp/media-applied.jsonl
```

Omit `--categories` for tagging only; omit `--tags` for classification only.
`--source-category Nature` restricts input to that parent and its subcategories.
Repeat `--id 123` to process specific IDs or retry failed items. `--limit N`
processes the first N active items by ID. Each run re-evaluates its selected media.
Missing and trashed media are excluded. Stale indexed metadata is rejected.

Images are resized to fit 768 by 768. Videos use up to eight evenly sampled
frames excluding the beginning and end; short clips use fewer frames. `--frames`
accepts 1 through 8. `--timeout` defaults to 180 seconds for each extraction or
inference operation. All selected tasks share one visual inference per item.
Filenames and existing tags are not sent to the model.

`OLLAMA_URL` and `CLASSIFIER_MODEL` provide defaults for their flags. If an
Ollama reverse proxy requires bearer authentication, set `OLLAMA_TOKEN`. Use a
trusted network or TLS proxy for remote inference. The script sends only visual
previews and the supplied lists. Old `CLASSIFIER_URL`/`CLASSIFIER_TOKEN` settings
and old classifier job tables are no longer used; historical tables are left
untouched.

Results are JSONL, one record per item with ID, destination, tags, explanation,
applied status and any error. Errors leave that item's metadata unchanged and
processing continues. Exit codes: 0 success, 1 errors, 2 invalid arguments,
130 interrupted. Ctrl-C stops between operations and cancels application through
transaction rollback. A forced kill or power failure between a filesystem move
and database commit cannot be rolled back automatically; rescan the library to
reconcile such a failure.

File collisions get a numbered suffix. Moves use hard links followed by unlink
to avoid overwriting destinations, so source and destination must be on the same
filesystem; moves across mount points fail without changing the source. Folder
symlinks are rejected. Library IDs, titles, favorites and watch-later flags are
preserved.

Run tests with `python3 -m unittest discover -s classifier -p 'test_*.py'`.

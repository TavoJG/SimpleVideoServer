# Standalone Visual Category Tagger

Run this Python 3.10+ script on the Linux media server. It reads the existing app
SQLite database and media files, extracts JPEG previews with FFmpeg, and suggests
tags for each existing category. Ollama can run on this server or a separate GPU
machine. The web app and the former classifier HTTP adapter are not involved.
No Python packages need installation.

Only `categories.tags` is updated. Existing category tags are always preserved;
new suggestions are added case-insensitively without duplicates. The script
does not assign categories, move files, or change media records.

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

## Allowed Tags

Supply a UTF-8 JSON array of allowed category tags:

```json
["forest", "beach", "people", "indoors", "vehicles"]
```

The model selects only these exact values. Existing tags not in this list remain
untouched. Tags such as `"Art, Music"` are supported. Edit a copy of
`tags.example.json` to match your library. No category list is needed.

Each sampled media item provides visual evidence. Its suggested tags are
combined with those from the other samples in the category. An empty suggestion
adds nothing and never clears tags. Samples do not receive their own tags.

## Run

First scan the library in the web app so category and file metadata are current.
The database must have the app's category-tag migration; start the updated app
once if the script reports a missing `categories.tags` column. Preview:

```sh
python3 classifier/classify_media.py \
  --db /srv/video_server/videos.sqlite3 \
  --tags classifier/tags.example.json \
  --model your-vision-model:tag \
  --ollama-url http://GPU_HOST:11434 \
  --limit 10 --report /tmp/category-tags-preview.jsonl
```

For automatic application, stop the web app, run the same command with `--apply`,
then restart it. Stopping the app avoids scans, renames and category deletion
during inference. SQLite transactions merge suggestions with the latest stored
tags, preserving additions made since inference started. A database-adjacent lock
prevents simultaneous CLI runs. Offline execution uses the server account's
filesystem access, including any categories locked in the browser.

```sh
python3 classifier/classify_media.py \
  --db /srv/video_server/videos.sqlite3 \
  --tags /srv/video_server/tags.json \
  --model your-vision-model:tag --apply \
  --report /tmp/category-tags-applied.jsonl
```

`--tags` is required. Repeat `--source-category Forests` to target specific
categories, including `Uncategorized` for the root. `--limit N` limits the number
of categories, ordered by category ID. By default, up to eight media items per
category are sampled evenly across media IDs. Use `--media-limit N` to change
that count or `--media-limit 0` to inspect all indexed media in each category.
This sampling is deterministic, not random; use all media for broader coverage.
Missing media is excluded, stale metadata is rejected, and empty categories are
skipped. Re-running the script adds only newly suggested tags.

Category assignment remains a manual app operation.

Images are resized to fit 768 by 768. Videos use up to eight evenly sampled
frames excluding the beginning and end; short clips use fewer frames. `--frames`
accepts 1 through 8. `--timeout` defaults to 180 seconds for each extraction or
inference operation. Each sampled media item uses one visual inference.
Filenames and existing tags are not sent to the model.

`OLLAMA_URL` and `CLASSIFIER_MODEL` provide defaults for their flags. If an
Ollama reverse proxy requires bearer authentication, set `OLLAMA_TOKEN`. Use a
trusted network or TLS proxy for remote inference. The script sends only visual
previews and the supplied tag list. Old `CLASSIFIER_URL`/`CLASSIFIER_TOKEN` settings
and old classifier job tables are no longer used; historical tables are left
untouched.

Results are JSONL, one record per category: category ID/name, existing tags,
suggested tags, added tags, merged tags, evidence per sampled media ID, applied
status and errors. In preview mode, `added_tags` means proposed additions.
Successful samples can contribute tags even if another sample fails; errors are
reported and produce exit code 1. If all samples fail, nothing is applied.
Changed/deleted categories or changed evidence prevent application for that
category. Exit codes: 0 success, 1 errors, 2 invalid arguments, 130 interrupted.
Database writes are transactional; the filesystem is never modified.

Run tests with `python3 -m unittest discover -s classifier -p 'test_*.py'`.

Tags are read and written as JSON arrays in SQLite, preserving comma-containing tags. Older comma-separated values remain readable.

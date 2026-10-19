# Standalone Visual Category Tagger

Run this Python 3.10+ script on the Linux media server. It reads the existing app
SQLite database and media files, extracts JPEG previews with FFmpeg, and suggests
tags for each existing category. Ollama can run on this server or a separate GPU
machine. The web app and the former classifier HTTP adapter are not involved.
No Python packages need installation.

Only `categories.tags` is updated. Existing category tags are always preserved;
at most one best-supported tag is added per category per run, case-insensitively
without duplicates. The script
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

Each sampled media item selects its single best-matching tag, or none when the
evidence is insufficient. The category's proposed tag is the one selected by the
most sampled items. This is a vote count, not a calibrated probability. Ties use
the order in your allowed tag list. Existing category tags do not affect the
winner: if it is already present, nothing is added rather than choosing a less
supported tag. An empty suggestion adds nothing and never clears tags.
Samples do not receive their own tags.

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

### Apply a Reviewed Preview Without Running Ollama Again

Keep the JSONL preview file from `--report`. After reviewing it, stop the web
app and apply the saved suggestions directly:

```sh
python3 classifier/classify_media.py \
  --db /srv/video_server/videos.sqlite3 \
  --apply-report /tmp/category-tags-preview.jsonl \
  --report /tmp/category-tags-applied.jsonl
```

Restart the web app afterward. This mode makes no Ollama calls and extracts no
frames. It does not require `--model`, `--tags`, or a running Ollama server.
Optionally supply `--tags /path/to/tags.json` to enforce a current allowed list.
`--apply-report` applies changes directly; do not combine it with `--apply`.

All report records are validated before any database writes. Each category's ID
and name must still match. Suggestions are merged with the latest stored tags,
never the preview's old tag list, so later additions are preserved. Reapplying
the same preview does not duplicate tags. Only `suggested_tags` are applied;
`tags` and `added_tags` in the preview are informational.

Older previews containing multiple suggestions are reduced to one winner using
the saved media evidence. Ties use the order of `suggested_tags` in that report.
The same one-tag limit applies when replaying a report.

New previews include the sampled media's database snapshots and file identity.
Changed, moved, deleted or missing evidence prevents applying that category.
Older category previews without snapshots are also accepted: current media
membership is checked, but original file content cannot be verified. Progress
prints a notice for these older reports.

Records with preview errors or a `skipped` status are skipped. Successful
categories can still be applied when another category fails. Errors produce
exit code 1. To exclude a category, remove its entire JSONL record from a copy of
the preview; to approve fewer tags, edit its `suggested_tags` list. Duplicate
category records and malformed JSON are rejected. Keep input and output report
paths different so the original preview remains available.

### Progress and Output

Progress is printed immediately to **stdout**: category position, sampled-media
position, preview extraction, waiting for Ollama, tag counts, errors and a final
summary. Each completed category prints its full proposed tag list, for example
`Proposed tags for 'Nature': ["forest"]`. An empty list is printed as
`[]`. Saved-report application also prints progress and proposed tags for each
category; its applied/skipped/failed message identifies whether they were saved.

Ollama requests use [HTTP streaming](https://docs.ollama.com/api/streaming).
While the model loads or processes images before its first response chunk, the
script prints elapsed-time updates every 10 seconds. Once chunks arrive, it
reports stream activity and the number of received characters. It assembles the
complete JSON and validates the allowed tags before accepting any suggestions;
partial, interrupted or malformed responses never supply tags.

Use `--report /path/to/results.jsonl` to save valid JSONL separately from the
progress text. Without `--report`, JSONL results go to stderr. `--quiet` disables
progress and restores JSONL on stdout when no report file is specified, for
example:

```sh
python3 classifier/classify_media.py \
  --db /srv/video_server/videos.sqlite3 \
  --apply-report /tmp/category-tags-preview.jsonl --quiet \
  > /tmp/category-tags-applied.jsonl
```

Do not redirect output to the input preview file: the shell truncates redirected
files before the script starts. Prefer separate `--report` paths, which the
script checks before opening.

`Uncategorized` is a special category and is always excluded from analysis and
saved-report application, even when explicitly selected or present in an older
report. Its existing tags remain untouched.

`--tags` is required for inference. Repeat `--source-category Forests` to target specific
managed categories. `--limit N` limits the number
of categories, ordered by category ID. By default, up to eight media items per
category are sampled evenly across media IDs. Use `--media-limit N` to change
that count or `--media-limit 0` to inspect all indexed media in each category.
This sampling is deterministic, not random; use all media for broader coverage.
Missing media is excluded, stale metadata is rejected, and empty categories are
skipped. Re-running the script adds only newly suggested tags.

### Resume After Failures or Interruption

Checkpointing is automatic. Successful media results are committed immediately
to `<database-path>.classifier-checkpoint.sqlite3`, even before a category has
finished. If a request fails, the process is interrupted, or saving category tags
fails, rerun the same command. Completed media are reused without frame
extraction or Ollama calls; failed and unfinished media are retried. This also
lets a preview run supply cached results to a later `--apply` run.

For example, run this command again after an interruption:

```sh
python3 classifier/classify_media.py \
  --db /opt/video-server/videos.sqlite3 \
  --tags /opt/video-server/tags.json --model gemma3:4b \
  --ollama-url http://GPU_HOST:11434 \
  --media-limit 0 --frames 8 --timeout 300 --apply \
  --report /tmp/category-tags-applied.jsonl
```

The script still visits selected categories and checks current media metadata,
but skips expensive analysis for completed items. Categories are rebuilt from
all selected cached and fresh results, then merged with their latest tags.
The output report is rewritten for the new run, including reused results.
The report is not the checkpoint, so a partially written report does not lose
completed media work.

Progress marks reused media with `reused checkpoint; no Ollama call`; reports
include `cached_media` and `analyzed_media` counts, and the final stdout summary
shows the totals.

Results are invalidated when the database path, media identity/path/category,
size or modification time, model name, Ollama URL, frame count, allowed tag list
or script's inference version changes. Category tag edits and timeout changes
do not invalidate results. File changes are detected through filesystem identity,
size and timestamps, not a full content hash. If a model is replaced under the
same model tag at the same server URL, use a new checkpoint path for fresh results.

This single-tag update increments the inference version, so older checkpoints
are automatically bypassed once. Subsequent runs reuse the new single-tag
results normally.

Use `--checkpoint /path/to/resume.sqlite3` to choose another checkpoint file.
Use `--no-checkpoint` to force fresh analysis without reading or updating the
checkpoint. Keep the checkpoint with the database when deploying subsequent
script updates. It contains model results, not images or video frames.
The cache is separate from the app database and never changes media records.
`--apply-report` operates independently and neither needs nor updates a checkpoint.

`--source-category` and `--limit` select inference categories and cannot be used
with `--apply-report`; saved-report mode uses the categories in the file.

Category assignment remains a manual app operation.

Images are resized to fit 768 by 768. Videos use up to eight evenly sampled
frames excluding the beginning and end; short clips use fewer frames. `--frames`
accepts 1 through 8. Each sampled media item uses one visual inference.
Filenames and existing tags are not sent to the model.

### Streaming Timeouts

The defaults retain eight media samples and eight frames, with a 300-second
overall request limit, matching the tested `--media-limit 8 --frames 8 --timeout 300`
configuration. Each media item has its own request and deadline.

| Option | Default | Purpose |
| --- | --- | --- |
| `--timeout` | 300 seconds | Overall Ollama request deadline; also the timeout for each FFmpeg/FFprobe operation |
| `--connect-timeout` | 10 seconds | Socket connection and TLS setup timeout |
| `--read-timeout` | Same as `--timeout` | Maximum idle socket-read wait, including headers and the first chunk |
| `--progress-interval` | 10 seconds | Interval between stdout waiting/streaming updates |

Streaming does not accelerate inference. Model loading and image encoding can
still take time before the first chunk. Keep the idle-read timeout generous for
that phase. Incoming data allows reads to continue, but does not extend the
overall deadline. On deadline expiry, the client shuts down the connection and
reports an error; cancellation of GPU work depends on the Ollama server. DNS
resolution uses the operating system resolver and can exceed socket timeouts.

For a slower model, allow ten minutes overall while retaining a five-minute
idle-read limit:

```sh
python3 classifier/classify_media.py \
  --db /srv/video_server/videos.sqlite3 \
  --tags /srv/video_server/tags.json --model gemma3:4b \
  --ollama-url http://GPU_HOST:11434 \
  --media-limit 8 --frames 8 --timeout 600 --read-timeout 300 \
  --progress-interval 10 --report /tmp/category-tags-preview.jsonl
```

Timeout errors distinguish connection, idle-read, and overall-deadline failures.
All durations must be positive. `--quiet` also suppresses streaming activity
updates. `--apply-report` does not open a network connection, so it needs no
streaming or timeout changes.

The client connects directly to `--ollama-url` using HTTP or verified HTTPS,
including URLs with a reverse-proxy path prefix. Forward-proxy environment
variables such as `HTTP_PROXY` are not used; configure a reverse proxy or VPN
when needed.

`OLLAMA_URL` and `CLASSIFIER_MODEL` provide defaults for their flags. If an
Ollama reverse proxy requires bearer authentication, set `OLLAMA_TOKEN`. Use a
trusted network or TLS proxy for remote inference. The script sends only visual
previews and the supplied tag list. Old `CLASSIFIER_URL`/`CLASSIFIER_TOKEN` settings
and old classifier job tables are no longer used; historical tables are left
untouched.

Results are JSONL, one record per category: category ID/name, existing tags,
suggested tags, added tags, merged tags, evidence per sampled media ID, applied
status and errors. Evidence includes snapshots for later application checks.
In preview mode, `added_tags` means proposed additions.
Successful samples can contribute tags even if another sample fails; errors are
reported and produce exit code 1 during inference. If all samples fail, nothing
is applied. Saved-report mode skips categories whose preview contains errors.
Changed/deleted categories or changed evidence prevent application for that
category. Exit codes: 0 success, 1 errors, 2 invalid arguments, 130 interrupted.
Database writes are transactional; the filesystem is never modified.

Run tests with `python3 -m unittest discover -s classifier -p 'test_*.py'`.

Tags are read and written as JSON arrays in SQLite, preserving comma-containing tags. Older comma-separated values remain readable.

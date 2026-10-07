# Visual classifier

The video app sends JPEG previews only. This service uses a pretrained vision
model hosted by Ollama. No shared media volume is needed. Install FFmpeg and
FFprobe on the video server and Ollama on the GPU machine.

Select a vision-capable model compatible with your GPU, pull its exact tag with
`ollama pull <model:tag>`, and set `CLASSIFIER_MODEL` to that tag. Model selection
must be verified against your labeled sample before routine use. Ollama's
[vision and structured output example](https://github.com/ollama/ollama-python/blob/main/examples/structured-outputs-image.py)
documents the inference capabilities used here.

On the GPU machine:

```sh
export CLASSIFIER_TOKEN='replace-with-a-random-secret'
export CLASSIFIER_MODEL='your-vision-model:tag'
python3 classifier/service.py
```

Alternatively build the adapter with `docker build -t media-classifier classifier`
and run on Linux with:

```sh
docker run --rm --network host -e CLASSIFIER_TOKEN -e CLASSIFIER_MODEL media-classifier
```

Ollama runs on the GPU host independently; the adapter itself needs no GPU.
`OLLAMA_URL` defaults to `http://127.0.0.1:11434`.
`INFERENCE_TIMEOUT_SECONDS` defaults to 150 and adapter `PORT` to 8090.
The adapter permits one inference at a time; concurrent calls return 503.
`GET /health` checks runtime reachability and whether the configured model is installed.

On the video server set `CLASSIFIER_URL=http://GPU_HOST:8090` and the same
`CLASSIFIER_TOKEN`, then restart the app. `CLASSIFIER_TIMEOUT_SECONDS` defaults
to 180 and includes frame extraction. Use a trusted private network or a TLS
reverse proxy; the bearer token and media previews travel to this endpoint.
Keep Ollama bound to localhost when the adapter runs on the same host.

Open a parent category, choose **Classify media**, review or edit destinations,
select items, and choose **Apply selected**. New destination names create folders
when approved. Failed items can be retried with **Classify media** after the job
finishes; already moved files are excluded. Review results survive app restarts;
unfinished jobs become interrupted. Cancellation stops app processing but an
in-flight inference may continue on the GPU until its timeout.

## Evaluation

Prepare representative JPEGs or the same evenly sampled video frames used by
the app, resized to fit 768 by 768. A dataset JSON file contains:

```json
[
  {"parent_category":"Nature", "existing_subcategories":["Forests"],
   "images":["samples/forest.jpg"], "expected":"Forests"},
  {"parent_category":"Nature", "images":["samples/ambiguous.jpg"], "expected":null}
]
```

Paths are relative to the dataset. Include ambiguous samples, proposed new
subcategories, short clips, and changing scenes. Run:

```sh
python3 classifier/benchmark.py dataset.json --url http://GPU_HOST:8090
```

Record model tag, GPU model/VRAM, dataset size, accuracy and mean latency from
the output. Observe peak VRAM with `nvidia-smi` during the run. Compare model
choices using the same data. New names use exact-match scoring, so establish
canonical expected names and manually review reasonable synonyms. No GPU or
labeled collection is bundled with this repository.

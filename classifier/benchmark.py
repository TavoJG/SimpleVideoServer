"""Evaluate labeled JPEG previews against a running classifier."""
import argparse
import base64
import json
import os
from pathlib import Path
import time
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("dataset", type=Path)
parser.add_argument("--url", default="http://127.0.0.1:8090")
args = parser.parse_args()
samples = json.loads(args.dataset.read_text())
records = []
for sample in samples:
    body = {"parent_category": sample["parent_category"],
            "existing_subcategories": sample.get("existing_subcategories", []),
            "images": [base64.b64encode((args.dataset.parent / p).read_bytes()).decode() for p in sample["images"]]}
    started = time.monotonic()
    try:
        request = urllib.request.Request(args.url.rstrip("/") + "/classify", data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json", "Authorization": "Bearer " + os.environ["CLASSIFIER_TOKEN"]})
        with urllib.request.urlopen(request, timeout=180) as response:
            result = json.load(response)
        prediction = None if result["abstain"] else result["subcategory"]
        records.append({"expected": sample["expected"], "prediction": prediction,
                        "correct": prediction == sample["expected"], "seconds": time.monotonic() - started})
    except (OSError, ValueError, KeyError) as error:
        records.append({"correct": False, "error": str(error), "seconds": time.monotonic() - started})
print(json.dumps({"samples": len(records), "accuracy": sum(r["correct"] for r in records) / len(records) if records else None,
                  "mean_seconds": sum(r["seconds"] for r in records) / len(records) if records else None,
                  "results": records}, indent=2))

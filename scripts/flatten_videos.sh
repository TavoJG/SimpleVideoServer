#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 /path/to/media-folder" >&2
  exit 2
fi

base_dir="${1%/}"
if [[ ! -d "$base_dir" ]]; then
  echo "Folder does not exist: $base_dir" >&2
  exit 1
fi

is_media() {
  case "${1,,}" in
    *.mp4|*.m4v|*.mov|*.webm|*.mkv|*.avi|*.wmv|*.flv|*.mpeg|*.mpg|*.jpg|*.jpeg|*.png|*.gif|*.webp|*.bmp|*.tif|*.tiff|*.avif) return 0 ;;
    *) return 1 ;;
  esac
}

target_path() {
  local source_path="$1"
  local filename stem ext candidate index
  filename="$(basename "$source_path")"
  stem="${filename%.*}"
  ext="${filename##*.}"

  if [[ "$filename" == "$ext" ]]; then
    ext=""
    candidate="$base_dir/$stem"
  else
    ext=".$ext"
    candidate="$base_dir/$stem$ext"
  fi

  index=1
  while [[ -e "$candidate" ]]; do
    candidate="$base_dir/${stem}_${index}${ext}"
    index=$((index + 1))
  done
  printf '%s\n' "$candidate"
}

count=0
while IFS= read -r -d '' file; do
  if ! is_media "$file"; then
    continue
  fi

  destination="$(target_path "$file")"
  mv -- "$file" "$destination"
  echo "Moved: $file -> $destination"
  count=$((count + 1))
done < <(find "$base_dir" -mindepth 2 -type f -print0)

echo "Moved $count media file(s)."

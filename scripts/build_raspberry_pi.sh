#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/build_raspberry_pi.sh [arm64|armv7]

Builds a Raspberry Pi deploy folder under dist/.

Targets:
  arm64  Raspberry Pi OS 64-bit, Pi 3/4/5
  armv7  Raspberry Pi OS 32-bit, Pi 2/3/4/5
USAGE
}

target="${1:-arm64}"
case "$target" in
  arm64)
    goarch="arm64"
    goarm=""
    ;;
  armv7)
    goarch="arm"
    goarm="7"
    ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    usage
    exit 2
    ;;
esac

app_name="video-server"
out_dir="dist/raspberry-pi-${target}"
binary="${out_dir}/${app_name}"

rm -rf "$out_dir"
mkdir -p "$out_dir"

echo "Building frontend..."
npm run build

echo "Building ${app_name} for linux/${goarch}${goarm:+ GOARM=${goarm}}..."
if [[ -n "$goarm" ]]; then
  env CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" go build -buildvcs=false -trimpath -ldflags="-s -w" -o "$binary" .
else
  env CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -buildvcs=false -trimpath -ldflags="-s -w" -o "$binary" .
fi

cp README.md "$out_dir/README.md"
cp systemd/video-server.service.example "$out_dir/video-server.service.example"

cat > "$out_dir/INSTALL.txt" <<'INSTALL'
Example install on the Raspberry Pi:

  sudo useradd --system --home /opt/video-server --shell /usr/sbin/nologin video-server
  sudo mkdir -p /opt/video-server
  sudo cp -R video-server README.md video-server.service.example /opt/video-server/
  sudo chown -R video-server:video-server /opt/video-server
  sudo cp /opt/video-server/video-server.service.example /etc/systemd/system/video-server.service
  sudo systemctl daemon-reload
  sudo systemctl enable --now video-server

Edit /etc/systemd/system/video-server.service first if your video folder is not /srv/videos.
Install ffmpeg if you want generated thumbnails for videos:

  sudo apt update
  sudo apt install -y ffmpeg
INSTALL

echo "Built ${out_dir}"
echo "Copy that directory to the Raspberry Pi, then follow ${out_dir}/INSTALL.txt."

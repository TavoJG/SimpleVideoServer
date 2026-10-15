#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  -h|--help)
    echo "Usage: scripts/build_linux_mint.sh"
    echo "Builds a Linux Mint amd64 deploy folder under dist/linux-mint-amd64."
    exit 0
    ;;
  "") ;;
  *)
    echo "Usage: scripts/build_linux_mint.sh" >&2
    exit 2
    ;;
esac
if [[ $# -gt 1 ]]; then
  echo "Usage: scripts/build_linux_mint.sh" >&2
  exit 2
fi

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"

out_dir="dist/linux-mint-amd64"
mkdir -p "$out_dir"

if [[ ! -d node_modules ]]; then
  echo "Installing frontend dependencies..."
  npm ci
fi

echo "Building frontend..."
npm run build

echo "Building video-server for linux/amd64..."
env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 go build \
  -buildvcs=false -trimpath -ldflags="-s -w" \
  -o "$out_dir/video-server" .

cp README.md "$out_dir/README.md"
cp systemd/video-server.service.example "$out_dir/video-server.service.example"

cat > "$out_dir/INSTALL.txt" <<'INSTALL'
Copy this entire folder to your Linux Mint amd64 VM, then run these commands
from the copied folder:

  sudo apt update
  sudo apt install -y ffmpeg
  id video-server >/dev/null 2>&1 || sudo useradd --system --home /opt/video-server --shell /usr/sbin/nologin video-server
  sudo mkdir -p /opt/video-server
  sudo cp video-server README.md video-server.service.example /opt/video-server/
  sudo chmod +x /opt/video-server/video-server
  sudo chown -R video-server:video-server /opt/video-server
  sudo cp /opt/video-server/video-server.service.example /etc/systemd/system/video-server.service
  sudoedit /etc/systemd/system/video-server.service

Set VIDEO_ROOT to your media folder and APP_PASSWORD to your chosen password
before starting the service. The video-server user needs read and directory
traversal access to your media, plus write access for moves and permanent deletion.

  sudo systemctl daemon-reload
  sudo systemctl enable --now video-server
  sudo systemctl status video-server

Open http://<mint-vm-ip>:5000 in your browser. Configure the VM network so the
host can reach it. If UFW is active, allow TCP port 5000 as appropriate.

For later binary updates, copy the new binary into /opt/video-server and run:

  sudo systemctl restart video-server
INSTALL

echo "Built ${out_dir}"
echo "Copy that directory to the Mint VM, then follow ${out_dir}/INSTALL.txt."

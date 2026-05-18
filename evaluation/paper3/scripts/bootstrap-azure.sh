#!/usr/bin/env bash
# bootstrap-azure.sh — one-shot VM setup for Paper 3 experiments
# Run as the non-root user (e.g. azureuser) immediately after SSH.
# Usage: bash bootstrap-azure.sh
set -euo pipefail

echo "=== [1/6] System packages ==="
sudo apt-get update -qq
sudo apt-get install -y --no-install-recommends \
    git curl wget ca-certificates gnupg lsb-release \
    build-essential jq unzip

echo "=== [2/6] Docker CE ==="
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
    | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
  https://download.docker.com/linux/ubuntu \
  $(lsb_release -cs) stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update -qq
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
sudo usermod -aG docker "$USER"
echo "Docker $(docker --version)"

echo "=== [3/6] Go 1.22 ==="
GO_VER="1.22.10"
wget -q "https://go.dev/dl/go${GO_VER}.linux-amd64.tar.gz"
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf "go${GO_VER}.linux-amd64.tar.gz"
rm "go${GO_VER}.linux-amd64.tar.gz"
# Persist in .bashrc so future SSH sessions see it
if ! grep -q '/usr/local/go/bin' ~/.bashrc; then
    echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
fi
export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
echo "Go $(go version)"

echo "=== [4/6] Fabric binaries (cryptogen, configtxgen) ==="
FABRIC_VER="2.5.10"
curl -sSL https://raw.githubusercontent.com/hyperledger/fabric/main/scripts/install-fabric.sh \
    | bash -s -- --fabric-version "${FABRIC_VER}" binary
# Binaries land in ./bin/
export PATH=$PATH:$HOME/bin
echo "cryptogen   : $(cryptogen version 2>&1 | head -1)"
echo "configtxgen : $(configtxgen --version 2>&1 | head -1)"

echo "=== [5/6] Node.js 20 + Caliper CLI ==="
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt-get install -y nodejs
npm install -g --unsafe-perm \
    @hyperledger/caliper-cli@0.6.0 2>&1 | tail -5
caliper --version

echo "=== [6/6] Python deps for plots ==="
sudo apt-get install -y python3-pip python3-venv
# Ubuntu 24.04 enforces PEP 668 (no global pip installs) — use a venv instead.
python3 -m venv "$HOME/plot-env"
"$HOME/plot-env/bin/pip" install --quiet matplotlib pandas numpy seaborn
# Wrapper so 'plot-results.py' can be called directly without activating the venv.
echo '#!/usr/bin/env bash' | sudo tee /usr/local/bin/plot-python > /dev/null
echo "exec $HOME/plot-env/bin/python3 \"\$@\"" | sudo tee -a /usr/local/bin/plot-python > /dev/null
sudo chmod +x /usr/local/bin/plot-python
echo "  → Python venv at ~/plot-env, use 'plot-python script.py'"

echo ""
echo "=============================================="
echo " Bootstrap complete. LOG OUT AND BACK IN so"
echo " the docker group membership takes effect."
echo " Then run: newgrp docker"
echo "=============================================="

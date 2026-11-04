#!/usr/bin/env bash
# ==============================================================================
# AutoHost Self-Hosted 1-Click Installer
# Installs Docker Compose stack (Postgres + API + Web) and the host Agent
# ==============================================================================

set -euo pipefail

BOLD='\033[1m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

error() {
    echo -e "${RED}[ERROR]${NC} $*" >&2
}

banner() {
    echo -e "${BLUE}${BOLD}"
    cat << "EOF"
    _         _        _   _           _   
   / \  _   _| |_ ___ | | | | ___  ___| |_ 
  / _ \| | | | __/ _ \| |_| |/ _ \/ __| __|
 / ___ \ |_| | || (_) |  _  | (_) \__ \ |_ 
/_/   \_\__,_|\__\___/|_| |_|\___/|___/\__|

      Self-Hosted Unified Installer
EOF
    echo -e "${NC}"
}

# 1. Root verification
check_root() {
    if [[ $EUID -ne 0 ]]; then
        error "This script must be run as root or with sudo:"
        echo "  sudo curl -fsSL https://raw.githubusercontent.com/mazapanuwu13/autohost-cloud-api/develop/scripts/install.sh | bash"
        exit 1
    fi
}

# 2. OS & Architecture Detection
detect_arch() {
    local os
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    if [[ "$os" != "linux" ]]; then
        error "AutoHost currently supports Linux only."
        exit 1
    fi

    local machine
    machine="$(uname -m)"
    case "$machine" in
        x86_64|amd64)
            ARCH="amd64"
            ;;
        aarch64|arm64)
            ARCH="arm64"
            ;;
        *)
            error "Unsupported architecture: $machine (only amd64 and arm64 are supported)"
            exit 1
            ;;
    esac
    info "Detected system: Linux ($ARCH)"
}

# 3. Detect primary host IP address, hostname, and Tailscale VPN
detect_ip() {
    # Detect Node Hostname
    if [[ -n "${AUTOHOST_NODE_NAME:-}" ]]; then
        NODE_HOSTNAME="$AUTOHOST_NODE_NAME"
    else
        NODE_HOSTNAME=$(hostname 2>/dev/null || cat /etc/hostname 2>/dev/null || uname -n || echo "autohost-master")
    fi
    info "Detected server hostname: $NODE_HOSTNAME"

    # Detect primary host LAN IP
    if [[ -n "${AUTOHOST_HOST_IP:-}" ]]; then
        HOST_IP="$AUTOHOST_HOST_IP"
    else
        HOST_IP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{print $7}' | head -n1 || true)
        if [[ -z "$HOST_IP" ]]; then
            HOST_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
        fi
        if [[ -z "$HOST_IP" ]]; then
            HOST_IP="127.0.0.1"
        fi
    fi
    info "Detected host LAN IP: $HOST_IP"

    # Detect Tailscale VPN IP if active
    TAILSCALE_IP=""
    if [[ -n "${AUTOHOST_TAILSCALE_IP:-}" ]]; then
        TAILSCALE_IP="$AUTOHOST_TAILSCALE_IP"
    elif command -v tailscale &>/dev/null; then
        TAILSCALE_IP=$(tailscale ip -4 2>/dev/null || true)
    fi
    if [[ -z "$TAILSCALE_IP" ]]; then
        TAILSCALE_IP=$(ip -4 addr show dev tailscale0 2>/dev/null | awk '/inet / {print $2}' | cut -d/ -f1 || true)
    fi

    if [[ -n "$TAILSCALE_IP" ]]; then
        success "Detected Tailscale VPN IP: $TAILSCALE_IP"
        API_ADVERTISED_IP="${AUTOHOST_API_ADVERTISED_IP:-$TAILSCALE_IP}"
    else
        API_ADVERTISED_IP="${AUTOHOST_API_ADVERTISED_IP:-$HOST_IP}"
    fi
}

# 4. Check & Install Dependencies (Docker, Compose, Curl)
check_dependencies() {
    info "Checking required dependencies..."

    if ! command -v curl &>/dev/null; then
        info "Installing curl..."
        apt-get update -y && apt-get install -y curl || yum install -y curl || true
    fi

    if ! command -v docker &>/dev/null; then
        info "Docker not found. Installing Docker engine via official get.docker.com script..."
        curl -fsSL https://get.docker.com | sh
        systemctl enable --now docker
    fi

    # Check for docker compose plugin
    if ! docker compose version &>/dev/null; then
        if command -v docker-compose &>/dev/null; then
            DOCKER_COMPOSE_CMD="docker-compose"
        else
            info "Installing Docker Compose plugin..."
            apt-get update -y && apt-get install -y docker-compose-plugin || true
            DOCKER_COMPOSE_CMD="docker compose"
        fi
    else
        DOCKER_COMPOSE_CMD="docker compose"
    fi

    success "Docker is installed and running ($($DOCKER_COMPOSE_CMD version 2>/dev/null | head -n1))."
}

# 5. Detect high-capacity disk mounts
detect_storage() {
    # If the user explicitly provided AUTOHOST_STORAGE_DIR, honor it immediately
    if [[ -n "${AUTOHOST_STORAGE_DIR:-}" ]]; then
        STORAGE_DIR="$AUTOHOST_STORAGE_DIR"
        info "Using specified storage directory: $STORAGE_DIR"
        return 0
    fi

    # Read existing STORAGE_DIR from .env if present
    local env_file="${AUTOHOST_INSTALL_DIR:-/opt/autohost}/.env"
    if [[ -f "$env_file" ]]; then
        local existing_storage
        existing_storage=$(grep "^AUTOHOST_STORAGE_DIR=" "$env_file" 2>/dev/null | cut -d= -f2- | tr -d '"' | tr -d "'" || true)
        if [[ -n "$existing_storage" ]]; then
            STORAGE_DIR="$existing_storage"
        fi
    fi

    # Detect mounted physical disks (starting with /dev/, excluding /boot, EFI, firmware)
    local mount_points=()
    local total_sizes=()
    local avail_sizes=()

    while read -r mount total avail dev; do
        [[ -z "$mount" ]] && continue
        if [[ "$mount" =~ ^/boot || "$mount" =~ firmware || "$mount" =~ /efi ]]; then
            continue
        fi
        local total_gb=$(( total / 1048576 ))
        local avail_gb=$(( avail / 1048576 ))
        mount_points+=("$mount")
        total_sizes+=("$total_gb")
        avail_sizes+=("$avail_gb")
    done < <(df -Pk 2>/dev/null | awk '$1 ~ /^\/dev/ {print $6, $2, $4, $1}' | sort -u -k1,1)

    local count=${#mount_points[@]}
    local root_avail=0
    local root_total=0
    local largest_non_root_mount=""
    local largest_non_root_avail=0
    local largest_non_root_total=0

    for ((i=0; i<count; i++)); do
        local m="${mount_points[i]}"
        local t="${total_sizes[i]}"
        local a="${avail_sizes[i]}"
        if [[ "$m" == "/" ]]; then
            root_avail="$a"
            root_total="$t"
        else
            if (( a > largest_non_root_avail )); then
                largest_non_root_mount="$m"
                largest_non_root_avail="$a"
                largest_non_root_total="$t"
            fi
        fi
    done

    local selected_mount="/"

    # If there is a non-root mount with more capacity (e.g., /media/disco with 200GB vs / with 20GB)
    if [[ -n "$largest_non_root_mount" ]] && (( largest_non_root_avail > root_avail )); then
        info "Detected secondary storage drive at $largest_non_root_mount (${largest_non_root_total} GB total, ${largest_non_root_avail} GB free)."

        # Interactive selection if stdin is a terminal
        if [[ -t 0 ]] && [[ "${DEBIAN_FRONTEND:-}" != "noninteractive" ]]; then
            echo ""
            echo -e "${YELLOW}${BOLD}Multiple storage disks detected:${NC}"
            echo -e "  [1] / (Root system disk)           - ${root_avail} GB free (${root_total} GB total)"
            echo -e "  [2] ${largest_non_root_mount} (Secondary disk) - ${largest_non_root_avail} GB free (${largest_non_root_total} GB total) ${GREEN}${BOLD}[Recommended]${NC}"
            echo ""
            read -r -p "Where should AutoHost store databases, backups & containers? [1/2, default: 2]: " choice
            if [[ "$choice" == "1" ]]; then
                selected_mount="/"
            else
                selected_mount="$largest_non_root_mount"
            fi
        else
            # Non-interactive: auto-select larger drive if root has < 50GB or secondary is at least 2x larger
            if (( root_avail < 50 )) || (( largest_non_root_avail > (root_avail * 2) )); then
                info "Auto-selecting high-capacity storage drive: $largest_non_root_mount"
                selected_mount="$largest_non_root_mount"
            else
                selected_mount="/"
            fi
        fi
    fi

    if [[ "$selected_mount" == "/" ]]; then
        STORAGE_DIR="${STORAGE_DIR:-/var/lib/autohost}"
    else
        STORAGE_DIR="${selected_mount%/}/autohost"
    fi

    info "AutoHost primary storage directory set to: $STORAGE_DIR"
}

# 6. Directory Setup & Seamless Migration
setup_directories() {
    INSTALL_DIR="${AUTOHOST_INSTALL_DIR:-/opt/autohost}"
    AGENT_DIR="${AUTOHOST_LOCAL_AGENT_DIR:-/etc/autohost}"

    info "Setting up directories in $INSTALL_DIR and $STORAGE_DIR..."
    mkdir -p "$INSTALL_DIR" "$STORAGE_DIR" "$STORAGE_DIR/shared" "$AGENT_DIR"
    chmod 755 "$INSTALL_DIR" "$STORAGE_DIR/shared"
    chmod 750 "$STORAGE_DIR" "$AGENT_DIR"

    # If STORAGE_DIR is on an external disk (not /var/lib/autohost)
    if [[ "$STORAGE_DIR" != "/var/lib/autohost" ]]; then
        # If /var/lib/autohost exists and has data (e.g. user mounted disk AFTER initial install)
        if [[ -d "/var/lib/autohost" && ! -L "/var/lib/autohost" ]]; then
            if [[ -n "$(ls -A /var/lib/autohost 2>/dev/null)" ]]; then
                info "Migrating existing data from /var/lib/autohost to $STORAGE_DIR..."
                rsync -a /var/lib/autohost/ "$STORAGE_DIR/" 2>/dev/null || cp -a /var/lib/autohost/* "$STORAGE_DIR/" 2>/dev/null || true
                mv /var/lib/autohost "/var/lib/autohost.bak.$(date +%s)"
                ln -s "$STORAGE_DIR" /var/lib/autohost
                success "Existing data migrated and symlinked /var/lib/autohost -> $STORAGE_DIR"
            else
                rmdir /var/lib/autohost 2>/dev/null || true
                ln -s "$STORAGE_DIR" /var/lib/autohost
            fi
        elif [[ ! -e "/var/lib/autohost" ]]; then
            ln -s "$STORAGE_DIR" /var/lib/autohost
            info "Created system symlink /var/lib/autohost -> $STORAGE_DIR"
        fi
    fi
}

# 7. Configure Docker data-root on secondary disk if applicable
configure_docker_storage() {
    if [[ "$STORAGE_DIR" == "/var/lib/autohost" ]]; then
        return 0
    fi

    local docker_target_dir="${STORAGE_DIR}/docker"
    local daemon_json="/etc/docker/daemon.json"

    # If daemon.json already has data-root configured, preserve it
    if [[ -f "$daemon_json" ]] && grep -q '"data-root"' "$daemon_json"; then
        return 0
    fi

    mkdir -p "$docker_target_dir"
    info "Configuring Docker data-root on secondary disk ($docker_target_dir)..."

    # If existing Docker root has data, sync it before switching
    if [[ -d "/var/lib/docker" ]] && [[ -n "$(ls -A /var/lib/docker 2>/dev/null)" ]]; then
        info "Syncing existing Docker data to $docker_target_dir..."
        systemctl stop docker 2>/dev/null || true
        rsync -a /var/lib/docker/ "$docker_target_dir/" 2>/dev/null || cp -a /var/lib/docker/* "$docker_target_dir/" 2>/dev/null || true
    fi

    mkdir -p /etc/docker
    if [[ ! -f "$daemon_json" ]]; then
        cat << EOF > "$daemon_json"
{
  "data-root": "$docker_target_dir"
}
EOF
    else
        if command -v python3 &>/dev/null; then
            python3 -c "import json, os; p='$daemon_json'; d=json.load(open(p)) if os.path.exists(p) else {}; d['data-root']='$docker_target_dir'; json.dump(d, open(p,'w'), indent=2)" 2>/dev/null || true
        else
            sed -i '1s/{/{\n  "data-root": "'"${docker_target_dir//\//\\/}"'",/' "$daemon_json" 2>/dev/null || true
        fi
    fi

    if systemctl is-active docker &>/dev/null; then
        info "Restarting Docker service with new data-root..."
        systemctl restart docker || true
    fi
    success "Docker configured to store all container volumes and images on $docker_target_dir"
}

# 6. Generate .env and compose.yml
write_configuration() {
    local env_file="$INSTALL_DIR/.env"
    local compose_file="$INSTALL_DIR/compose.yml"

    if [[ ! -f "$env_file" ]]; then
        info "Generating initial environment configuration ($env_file)..."
        local pg_pass
        pg_pass=$(openssl rand -hex 16 2>/dev/null || date +%s%N | sha256sum | head -c 32)
        local jwt_secret
        jwt_secret=$(openssl rand -hex 32 2>/dev/null || date +%s%N | sha256sum | head -c 64)

        cat << EOF > "$env_file"
# ==============================================================================
# AutoHost Self-Hosted Environment Variables
# Auto-generated on $(date -u +"%Y-%m-%dT%H:%M:%SZ")
# ==============================================================================

POSTGRES_USER=autohost
POSTGRES_PASSWORD=${pg_pass}
POSTGRES_DB=autohost

JWT_SECRET=${jwt_secret}

ALLOW_REGISTRATION=false
AUTO_ENROLL_LOCAL_NODE=true

# Master Node Identity & Tailscale VPN
LOCAL_NODE_HOSTNAME=${NODE_HOSTNAME}
LOCAL_NODE_IP=${HOST_IP}
LOCAL_NODE_IP_VPN=${TAILSCALE_IP}

AUTOHOST_STORAGE_DIR=${STORAGE_DIR}
AUTOHOST_LOCAL_AGENT_DIR=${AGENT_DIR}
LOCAL_AGENT_GRPC_ADDRESS=127.0.0.1:9090

API_PORT=8080
GRPC_PORT=9090
WEB_PORT=3000

# Public web & API URL (Prioritizes Tailscale VPN IP for remote node pairing)
NEXT_PUBLIC_API_URL=http://${API_ADVERTISED_IP}:8080
EOF
        chmod 600 "$env_file"
        success "Generated fresh secrets in $env_file"
    else
        info "Preserving existing $env_file (syncing node hostname and Tailscale IP)"
        if ! grep -q "^AUTOHOST_STORAGE_DIR=" "$env_file"; then
            echo "AUTOHOST_STORAGE_DIR=${STORAGE_DIR}" >> "$env_file"
        else
            sed -i "s|^AUTOHOST_STORAGE_DIR=.*|AUTOHOST_STORAGE_DIR=${STORAGE_DIR}|" "$env_file"
        fi
        if ! grep -q "^LOCAL_NODE_HOSTNAME=" "$env_file"; then
            echo "LOCAL_NODE_HOSTNAME=${NODE_HOSTNAME}" >> "$env_file"
        else
            sed -i "s|^LOCAL_NODE_HOSTNAME=.*|LOCAL_NODE_HOSTNAME=${NODE_HOSTNAME}|" "$env_file"
        fi
        if ! grep -q "^LOCAL_NODE_IP=" "$env_file"; then
            echo "LOCAL_NODE_IP=${HOST_IP}" >> "$env_file"
        else
            sed -i "s|^LOCAL_NODE_IP=.*|LOCAL_NODE_IP=${HOST_IP}|" "$env_file"
        fi
        if ! grep -q "^LOCAL_NODE_IP_VPN=" "$env_file"; then
            echo "LOCAL_NODE_IP_VPN=${TAILSCALE_IP}" >> "$env_file"
        else
            sed -i "s|^LOCAL_NODE_IP_VPN=.*|LOCAL_NODE_IP_VPN=${TAILSCALE_IP}|" "$env_file"
        fi
        if [[ -n "$TAILSCALE_IP" ]]; then
            sed -i "s|^NEXT_PUBLIC_API_URL=http://127.0.0.1:8080|NEXT_PUBLIC_API_URL=http://${TAILSCALE_IP}:8080|" "$env_file"
            sed -i "s|^NEXT_PUBLIC_API_URL=http://localhost:8080|NEXT_PUBLIC_API_URL=http://${TAILSCALE_IP}:8080|" "$env_file"
        fi
    fi

    info "Writing Docker Compose file ($compose_file)..."
    cat << 'EOF' > "$compose_file"
services:
  postgres:
    image: postgres:16-alpine
    container_name: autohost-db
    restart: unless-stopped
    environment:
      POSTGRES_USER: ${POSTGRES_USER:-autohost}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-autohost_secret}
      POSTGRES_DB: ${POSTGRES_DB:-autohost}
    command:
      - "postgres"
      - "-c"
      - "shared_buffers=128MB"
      - "-c"
      - "max_connections=50"
    volumes:
      - ${AUTOHOST_STORAGE_DIR:-/var/lib/autohost}/postgres:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER:-autohost} -d ${POSTGRES_DB:-autohost}"]
      interval: 5s
      timeout: 5s
      retries: 10

  migrate:
    image: ghcr.io/arturo-enriquez-v/autohost-cloud-api:latest
    container_name: autohost-migrate
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      DATABASE_URL: postgres://${POSTGRES_USER:-autohost}:${POSTGRES_PASSWORD:-autohost_secret}@postgres:5432/${POSTGRES_DB:-autohost}?sslmode=disable
    entrypoint: ["/usr/local/bin/autohost-migrate", "up"]
    restart: "no"

  api:
    image: ghcr.io/arturo-enriquez-v/autohost-cloud-api:latest
    container_name: autohost-api
    restart: unless-stopped
    user: "0:0"
    depends_on:
      postgres:
        condition: service_healthy
      migrate:
        condition: service_completed_successfully
    ports:
      - "${API_PORT:-8080}:8080"
      - "${GRPC_PORT:-9090}:9090"
    environment:
      SERVICE_MODE: "all"
      PORT: "8080"
      GRPC_PORT: "9090"
      DATABASE_URL: postgres://${POSTGRES_USER:-autohost}:${POSTGRES_PASSWORD:-autohost_secret}@postgres:5432/${POSTGRES_DB:-autohost}?sslmode=disable
      JWT_SECRET: ${JWT_SECRET:-change_me_in_production_autohost_selfhosted_secret_32b}
      ALLOW_REGISTRATION: ${ALLOW_REGISTRATION:-false}
      AUTO_ENROLL_LOCAL_NODE: ${AUTO_ENROLL_LOCAL_NODE:-true}
      LOCAL_NODE_HOSTNAME: ${LOCAL_NODE_HOSTNAME:-}
      LOCAL_NODE_IP: ${LOCAL_NODE_IP:-}
      LOCAL_NODE_IP_VPN: ${LOCAL_NODE_IP_VPN:-}
      AUTOHOST_STORAGE_DIR: /var/lib/autohost
      AUTOHOST_LOCAL_AGENT_DIR: /etc/autohost
      LOCAL_AGENT_GRPC_ADDRESS: ${LOCAL_AGENT_GRPC_ADDRESS:-127.0.0.1:9090}
      GOOGLE_CLIENT_ID: ${GOOGLE_CLIENT_ID:-}
    volumes:
      - ${AUTOHOST_STORAGE_DIR:-/var/lib/autohost}:/var/lib/autohost
      - ${AUTOHOST_LOCAL_AGENT_DIR:-/etc/autohost}:/etc/autohost

  web:
    image: ghcr.io/arturo-enriquez-v/autohost-cloud-lite:latest
    container_name: autohost-web
    restart: unless-stopped
    depends_on:
      - api
    ports:
      - "${WEB_PORT:-3000}:3000"
    environment:
      API_URL: ${INTERNAL_API_URL:-http://api:8080}
      INTERNAL_API_URL: ${INTERNAL_API_URL:-http://api:8080}
      NEXT_PUBLIC_API_URL: ${NEXT_PUBLIC_API_URL:-http://localhost:8080}
EOF
}

# 7. Start Docker Compose Stack
start_compose() {
    info "Pulling and launching AutoHost Cloud containers..."
    cd "$INSTALL_DIR"
    $DOCKER_COMPOSE_CMD pull
    $DOCKER_COMPOSE_CMD up -d

    info "Waiting for AutoHost API to initialize and generate local node credentials..."
    local attempts=0
    local max_attempts=30
    while [[ $attempts -lt $max_attempts ]]; do
        if [[ -f "$AGENT_DIR/agent.yaml" ]]; then
            # Ensure agent.yaml has proper keys (agent_token and api_url)
            if grep -q "^api_token:" "$AGENT_DIR/agent.yaml" && ! grep -q "^agent_token:" "$AGENT_DIR/agent.yaml"; then
                sed -i 's/^api_token:/agent_token:/' "$AGENT_DIR/agent.yaml"
            fi
            if ! grep -q "^api_url:" "$AGENT_DIR/agent.yaml"; then
                sed -i '/^grpc_address:/i api_url: "http://127.0.0.1:8080"' "$AGENT_DIR/agent.yaml"
            fi
            if ! grep -q "^storage_dir:" "$AGENT_DIR/agent.yaml"; then
                echo "storage_dir: \"$STORAGE_DIR\"" >> "$AGENT_DIR/agent.yaml"
            fi
            if ! grep -q "^shared_dir:" "$AGENT_DIR/agent.yaml"; then
                echo "shared_dir: \"$STORAGE_DIR/shared\"" >> "$AGENT_DIR/agent.yaml"
            fi
            success "Local node credentials and mTLS certificates verified in $AGENT_DIR/agent.yaml"
            return 0
        fi
        sleep 2
        attempts=$((attempts + 1))
    done

    warn "Timed out waiting for $AGENT_DIR/agent.yaml (will continue setup and agent will auto-reconnect once generated)"
}

# 8. Install & Configure Host Agent
install_agent() {
    info "Configuring AutoHost Agent on host..."

    # Check if binary already installed
    local agent_bin="/usr/local/bin/autohost-agent"
    if [[ ! -x "$agent_bin" ]]; then
        if command -v autohost-agent &>/dev/null; then
            agent_bin="$(command -v autohost-agent)"
            info "Found existing agent binary at $agent_bin"
        else
            info "Downloading autohost-agent ($ARCH)..."
            local download_url=""
            # Try latest release assets from GitHub
            local release_tag
            release_tag=$(curl -sSL "https://api.github.com/repos/arturo-enriquez-v/autohost-agent/releases/latest" 2>/dev/null | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' || true)
            if [[ -z "$release_tag" ]]; then
                release_tag="v1.11.0"
            fi

            local tmp_dir
            tmp_dir=$(mktemp -d)
            local tar_name="autohost-agent_${release_tag#v}_linux_${ARCH}.tar.gz"
            local tar_url="https://github.com/arturo-enriquez-v/autohost-agent/releases/download/${release_tag}/${tar_name}"

            if curl -fsSL "$tar_url" -o "$tmp_dir/agent.tar.gz" 2>/dev/null; then
                tar -xzf "$tmp_dir/agent.tar.gz" -C "$tmp_dir"
                local found_bin
                found_bin=$(find "$tmp_dir" -type f -name "autohost-agent" | head -n1)
                if [[ -n "$found_bin" ]]; then
                    install -m 755 "$found_bin" /usr/local/bin/autohost-agent
                    agent_bin="/usr/local/bin/autohost-agent"
                fi
            else
                # Fallback to direct raw binary if tarball not matching
                local bin_url="https://github.com/arturo-enriquez-v/autohost-agent/releases/download/${release_tag}/autohost-agent-linux-${ARCH}"
                if curl -fsSL "$bin_url" -o /usr/local/bin/autohost-agent 2>/dev/null; then
                    chmod +x /usr/local/bin/autohost-agent
                    agent_bin="/usr/local/bin/autohost-agent"
                else
                    warn "Could not automatically download precompiled binary for autohost-agent."
                    warn "If compiling from source, place the binary in /usr/local/bin/autohost-agent."
                fi
            fi
            rm -rf "$tmp_dir"
        fi
    fi

    # Setup systemd service
    info "Setting up systemd service /etc/systemd/system/autohost-agent.service..."
    cat << EOF > /etc/systemd/system/autohost-agent.service
[Unit]
Description=AutoHost Monitoring Agent
After=network.target docker.service
Wants=network.target

[Service]
Type=simple
ExecStart=${agent_bin} ${AGENT_DIR}/agent.yaml
Restart=always
RestartSec=5s
KillMode=process

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    if systemctl enable --now autohost-agent; then
        success "autohost-agent service enabled and started."
    else
        warn "Could not start autohost-agent service. Run 'journalctl -u autohost-agent -e' for details."
    fi
}

# 9. Main function
main() {
    banner
    check_root
    detect_arch
    detect_ip
    detect_storage
    check_dependencies
    setup_directories
    configure_docker_storage
    write_configuration
    start_compose
    install_agent

    echo ""
    echo -e "${GREEN}${BOLD}========================================================================${NC}"
    echo -e "${GREEN}${BOLD}       🎉 AutoHost Self-Hosted Stack Installed Successfully!           ${NC}"
    echo -e "${GREEN}${BOLD}========================================================================${NC}"
    echo ""
    echo -e "  ${BOLD}Node Name:${NC}          ${GREEN}${NODE_HOSTNAME}${NC}"
    echo -e "  ${BOLD}Web Dashboard:${NC}      ${BLUE}http://${API_ADVERTISED_IP}:3000${NC}"
    echo -e "  ${BOLD}API Endpoint:${NC}       ${BLUE}http://${API_ADVERTISED_IP}:8080${NC}"
    if [[ -n "$TAILSCALE_IP" ]]; then
        echo -e "  ${BOLD}Tailscale VPN IP:${NC}   ${GREEN}${TAILSCALE_IP}${NC} (configured for remote node pairing)"
    fi
    echo -e "  ${BOLD}Host LAN IP:${NC}        ${HOST_IP}"
    echo -e "  ${BOLD}Storage Path:${NC}       ${GREEN}${STORAGE_DIR}${NC}"
    if [[ "$STORAGE_DIR" != "/var/lib/autohost" ]]; then
        echo -e "  ${BOLD}Docker Data-Root:${NC}   ${GREEN}${STORAGE_DIR}/docker${NC} (configured on high-capacity disk)"
    fi
    echo -e "  ${BOLD}gRPC Port:${NC}          9090"
    echo -e "  ${BOLD}Configuration:${NC}      $INSTALL_DIR/compose.yml"
    echo -e "  ${BOLD}Local Node Agent:${NC}   $AGENT_DIR/agent.yaml (systemd: autohost-agent)"
    echo ""
    echo -e "${YELLOW}Next Steps:${NC}"
    echo -e "  1. Open ${BOLD}http://${API_ADVERTISED_IP}:3000${NC} in your browser."
    echo -e "  2. Complete the First-Run Setup to create your Administrator account."
    echo -e "  3. Your local node ${BOLD}(${NODE_HOSTNAME})${NC} will automatically show ${GREEN}${BOLD}Online${NC} in the dashboard."
    echo ""
    echo -e "To manage containers:  ${BOLD}cd $INSTALL_DIR && $DOCKER_COMPOSE_CMD ps${NC}"
    echo -e "To view agent logs:    ${BOLD}journalctl -u autohost-agent -f${NC}"
    echo -e "${GREEN}${BOLD}========================================================================${NC}"
}

main "$@"


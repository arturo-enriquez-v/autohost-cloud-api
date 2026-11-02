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

# 3. Detect primary host IP address
detect_ip() {
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
    info "Detected host IP: $HOST_IP"
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

# 5. Directory Setup
setup_directories() {
    INSTALL_DIR="${AUTOHOST_INSTALL_DIR:-/opt/autohost}"
    STORAGE_DIR="${AUTOHOST_STORAGE_DIR:-/var/lib/autohost}"
    AGENT_DIR="${AUTOHOST_LOCAL_AGENT_DIR:-/etc/autohost}"

    info "Setting up directories in $INSTALL_DIR..."
    mkdir -p "$INSTALL_DIR" "$STORAGE_DIR" "$AGENT_DIR"
    chmod 755 "$INSTALL_DIR"
    chmod 750 "$STORAGE_DIR" "$AGENT_DIR"
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

AUTOHOST_STORAGE_DIR=${STORAGE_DIR}
AUTOHOST_LOCAL_AGENT_DIR=${AGENT_DIR}
LOCAL_AGENT_GRPC_ADDRESS=127.0.0.1:9090

API_PORT=8080
GRPC_PORT=9090
WEB_PORT=3000

# Public web & API URL
NEXT_PUBLIC_API_URL=http://${HOST_IP}:8080
EOF
        chmod 600 "$env_file"
        success "Generated fresh secrets in $env_file"
    else
        info "Preserving existing $env_file"
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
    check_dependencies
    setup_directories
    write_configuration
    start_compose
    install_agent

    echo ""
    echo -e "${GREEN}${BOLD}========================================================================${NC}"
    echo -e "${GREEN}${BOLD}       🎉 AutoHost Self-Hosted Stack Installed Successfully!           ${NC}"
    echo -e "${GREEN}${BOLD}========================================================================${NC}"
    echo ""
    echo -e "  ${BOLD}Web Dashboard:${NC}      ${BLUE}http://${HOST_IP}:3000${NC}"
    echo -e "  ${BOLD}API Endpoint:${NC}       ${BLUE}http://${HOST_IP}:8080${NC}"
    echo -e "  ${BOLD}gRPC Port:${NC}          9090"
    echo -e "  ${BOLD}Configuration:${NC}      $INSTALL_DIR/compose.yml"
    echo -e "  ${BOLD}Local Node Agent:${NC}   $AGENT_DIR/agent.yaml (systemd: autohost-agent)"
    echo ""
    echo -e "${YELLOW}Next Steps:${NC}"
    echo -e "  1. Open ${BOLD}http://${HOST_IP}:3000${NC} in your browser."
    echo -e "  2. Complete the First-Run Setup to create your Administrator account."
    echo -e "  3. Your local node will automatically show ${GREEN}${BOLD}Online${NC} in the dashboard."
    echo ""
    echo -e "To manage containers:  ${BOLD}cd $INSTALL_DIR && $DOCKER_COMPOSE_CMD ps${NC}"
    echo -e "To view agent logs:    ${BOLD}journalctl -u autohost-agent -f${NC}"
    echo -e "${GREEN}${BOLD}========================================================================${NC}"
}

main "$@"


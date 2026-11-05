#!/usr/bin/env bash
# ==============================================================================
# AutoHost Self-Hosted Stack & Agent Uninstaller
# Cleans up containers, systemd services, binaries, certificates, and data.
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
    echo -e "${RED}${BOLD}"
    cat << "EOF"
    _         _        _   _           _   
   / \  _   _| |_ ___ | | | | ___  ___| |_ 
  / _ \| | | | __/ _ \| |_| |/ _ \/ __| __|
 / ___ \ |_| | || (_) |  _  | (_) \__ \ |_ 
/_/   \_\__,_|\__\___/|_| |_|\___/|___/\__|

       Self-Hosted Complete Cleanup
EOF
    echo -e "${NC}"
}

check_root() {
    if [[ $EUID -ne 0 ]]; then
        error "This script must be run as root or with sudo:"
        echo "  sudo bash scripts/uninstall.sh"
        exit 1
    fi
}

confirm_uninstall() {
    if [[ "${1:-}" == "-y" || "${1:-}" == "--yes" || "${DEBIAN_FRONTEND:-}" == "noninteractive" ]]; then
        return 0
    fi

    if [[ -t 0 ]]; then
        echo -e "${YELLOW}${BOLD}WARNING: This will completely remove AutoHost containers, agent service,${NC}"
        echo -e "${YELLOW}${BOLD}database, certificates, and data directories (/opt/autohost, /var/lib/autohost, /etc/autohost).${NC}"
        echo ""
        read -r -p "Are you sure you want to proceed with full cleanup? [y/N]: " answer
        case "$answer" in
            [yY][eE][sS]|[yY])
                return 0
                ;;
            *)
                info "Cleanup aborted by user."
                exit 0
                ;;
        esac
    fi
}

cleanup() {
    banner
    check_root
    confirm_uninstall "${1:-}"

    # 1. Stop and remove host agent service
    info "Stopping and disabling autohost-agent systemd service..."
    systemctl stop autohost-agent 2>/dev/null || true
    systemctl disable autohost-agent 2>/dev/null || true
    rm -f /etc/systemd/system/autohost-agent.service
    rm -f /usr/lib/systemd/system/autohost-agent.service
    systemctl daemon-reload 2>/dev/null || true
    success "Agent systemd service removed."

    # 2. Stop and remove Docker containers
    info "Stopping and removing AutoHost Docker containers and networks..."
    local custom_storage=""
    if [[ -f "/opt/autohost/.env" ]]; then
        custom_storage=$(grep "^AUTOHOST_STORAGE_DIR=" "/opt/autohost/.env" 2>/dev/null | cut -d= -f2- | tr -d '"' | tr -d "'" || true)
    fi

    if [[ -d "/opt/autohost" ]] && command -v docker &>/dev/null; then
        (cd /opt/autohost && docker compose down -v --remove-orphans 2>/dev/null || true)
    fi

    if command -v docker &>/dev/null; then
        docker rm -f autohost-web autohost-api autohost-db autohost-migrate 2>/dev/null || true
        # Remove cached/old AutoHost images so reinstall pulls fresh ones
        info "Removing local AutoHost Docker images..."
        docker rmi -f ghcr.io/arturo-enriquez-v/autohost-cloud-lite:latest \
                      ghcr.io/arturo-enriquez-v/autohost-cloud-api:latest 2>/dev/null || true
    fi
    success "Docker containers and images cleaned."

    # 3. Remove agent binaries
    info "Removing autohost-agent binary..."
    rm -f /usr/local/bin/autohost-agent
    rm -f /usr/bin/autohost-agent
    success "Agent binary removed."

    # 4. Remove directories and data
    info "Removing configuration, certificates and databases..."
    rm -rf /opt/autohost
    rm -rf /etc/autohost

    # Handle symlinked or standard /var/lib/autohost
    if [[ -L "/var/lib/autohost" ]]; then
        local symlink_target
        symlink_target=$(readlink "/var/lib/autohost" 2>/dev/null || true)
        rm -f /var/lib/autohost
        if [[ -n "$symlink_target" && -d "$symlink_target" ]]; then
            info "Removing external storage target ($symlink_target)..."
            rm -rf "$symlink_target"
        fi
    else
        rm -rf /var/lib/autohost
    fi

    if [[ -n "$custom_storage" && -d "$custom_storage" && "$custom_storage" != "/var/lib/autohost" ]]; then
        info "Removing custom storage directory ($custom_storage)..."
        rm -rf "$custom_storage"
    fi

    # Remove temporary backup files if any
    rm -rf /var/lib/autohost.bak.* 2>/dev/null || true

    echo ""
    echo -e "${GREEN}${BOLD}========================================================================${NC}"
    echo -e "${GREEN}${BOLD}       ✅ AutoHost has been completely removed from this system!        ${NC}"
    echo -e "${GREEN}${BOLD}========================================================================${NC}"
    echo ""
    echo -e "You can now run a fresh installation at any time with:"
    echo -e "  ${BOLD}curl -fsSL https://raw.githubusercontent.com/mazapanuwu13/autohost-cloud-api/develop/scripts/install.sh | sudo bash${NC}"
    echo ""
}

cleanup "$@"


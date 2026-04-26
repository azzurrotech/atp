#!/bin/bash

set -e

# ============================================
# COLORS
# ============================================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()    { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[OK]${NC} $1"; }
log_warn()    { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error()   { echo -e "${RED}[ERROR]${NC} $1"; }


# ============================================
# FUNCTIONS
# ============================================


check_hosts_file() {
    log_info "Checking /etc/hosts for local domains..."
    local domains=("ai.localhost" "nc.localhost" "site.localhost" "git.localhost" "mail.localhost" "authentik.localhost")
    local missing=()
    
    for domain in "${domains[@]}"; do
        if ! grep -q "127.0.0.1 $domain" /etc/hosts 2>/dev/null; then
            missing+=("$domain")
        fi
    done
    
    if [ ${#missing[@]} -gt 0 ]; then
        log_warn "Missing domains in /etc/hosts: ${missing[*]}"
        log_info "Add these lines to /etc/hosts:"
        for domain in "${missing[@]}"; do
            echo "    127.0.0.1 $domain"
        done
        log_info "Run: sudo nano /etc/hosts (Linux/Mac) or edit C:\\Windows\\System32\\drivers\\etc\\hosts (Windows)"
        read -p "Press Enter after editing hosts file, or Ctrl+C to cancel..."
    else
        log_success "All domains configured in /etc/hosts"
    fi
}

detect_profiles() {
    # ... (Logic remains the same, but now includes 'auth' profile) ...
    if [ -n "$ENABLE_AUTH" ]; then
        PROFILE="auth"
        log_info "Auth service enabled"
    elif [ -n "$ENABLE_ALL" ]; then
        PROFILE="all"
        log_info "All services enabled via ENABLE_ALL"
    # ... (rest of logic) ...
}

start_containers() {
    log_info "Starting Docker containers with profile: $PROFILE"
    
    if [ "$PROFILE" == "all" ]; then
        docker compose up -d --build
    else
        docker compose --profile "$PROFILE" up -d --build
    fi
    
    log_success "Containers started"
}

wait_for_services() {
    log_info "Waiting for services to be healthy..."
    
    # Wait for Ollama
    log_info "Waiting for Ollama..."
    for i in {1..60}; do
        if docker compose exec -T ollama curl -sf http://localhost:11434/api/tags > /dev/null 2>&1; then
            log_success "Ollama is ready"
            break
        fi
        if [ $i -eq 60 ]; then
            log_error "Ollama failed to start"
            exit 1
        fi
        sleep 2
    done

    # Wait for Authentik if enabled
    if docker compose ps | grep -q "authentik"; then
        log_info "Waiting for Authentik..."
        for i in {1..60}; do
            if docker compose exec -T authentik curl -sf http://localhost:9000/api/v3/ping > /dev/null 2>&1; then
                log_success "Authentik is ready"
                break
            fi
            sleep 2
        done
    fi
    
    # Wait for DB if enabled
    if docker compose ps | grep -q "ai-db"; then
        log_info "Waiting for Database..."
        for i in {1..30}; do
            if docker compose exec -T db mysqladmin ping -h localhost > /dev/null 2>&1; then
                log_success "Database is ready"
                break
            fi
            sleep 2
        done
    fi
}

check_hosts_file() {
    log_info "Checking /etc/hosts for local domains..."
    local domains=("ai.localhost" "nc.localhost" "site.localhost" "git.localhost" "mail.localhost")
    local missing=()
    
    for domain in "${domains[@]}"; do
        if ! grep -q "127.0.0.1 $domain" /etc/hosts 2>/dev/null; then
            missing+=("$domain")
        fi
    done
    
    if [ ${#missing[@]} -gt 0 ]; then
        log_warn "Missing domains in /etc/hosts: ${missing[*]}"
        log_info "Add these lines to /etc/hosts:"
        for domain in "${missing[@]}"; do
            echo "    127.0.0.1 $domain"
        done
        log_info "Run: sudo nano /etc/hosts (Linux/Mac) or edit C:\\Windows\\System32\\drivers\\etc\\hosts (Windows)"
        read -p "Press Enter after editing hosts file, or Ctrl+C to cancel..."
    else
        log_success "All domains configured in /etc/hosts"
    fi
}

detect_profiles() {
    log_info "Detecting enabled services..."
    
    # Check if .env exists
    if [ ! -f .env ]; then
        log_warn ".env not found. Creating from template..."
        cp .env.example .env 2>/dev/null || touch .env
    fi
    
    # Detect which profiles to use based on environment
    if [ -n "$ENABLE_ALL" ]; then
        PROFILE="all"
        log_info "All services enabled via ENABLE_ALL"
    elif [ -n "$ENABLE_MAIL" ]; then
        PROFILE="mail"
        log_info "Mail service enabled"
    elif [ -n "$ENABLE_STORAGE" ]; then
        PROFILE="storage"
        log_info "Storage service enabled"
    elif [ -n "$ENABLE_WEBSITE" ]; then
        PROFILE="website"
        log_info "Website service enabled"
    elif [ -n "$ENABLE_VCS" ]; then
        PROFILE="vcs"
        log_info "VCS service enabled"
    elif [ -n "$ENABLE_WEBUI" ]; then
        PROFILE="webui"
        log_info "WebUI service enabled"
    else
        PROFILE="core"
        log_info "Core service only (Ollama)"
    fi
}

start_containers() {
    log_info "Starting Docker containers with profile: $PROFILE"
    
    if [ "$PROFILE" == "all" ]; then
        docker compose up -d --build
    else
        docker compose --profile "$PROFILE" up -d --build
    fi
    
    log_success "Containers started"
}

wait_for_services() {
    log_info "Waiting for services to be healthy..."
    
    # Wait for Ollama
    log_info "Waiting for Ollama..."
    for i in {1..60}; do
        if docker compose exec -T ollama curl -sf http://localhost:11434/api/tags > /dev/null 2>&1; then
            log_success "Ollama is ready"
            break
        fi
        if [ $i -eq 60 ]; then
            log_error "Ollama failed to start"
            exit 1
        fi
        sleep 2
    done
    
    # Wait for DB if enabled
    if docker compose ps | grep -q "ai-db"; then
        log_info "Waiting for Database..."
        for i in {1..30}; do
            if docker compose exec -T db mysqladmin ping -h localhost > /dev/null 2>&1; then
                log_success "Database is ready"
                break
            fi
            sleep 2
        done
    fi
}

pull_ollama_models() {
    log_info "Pulling Ollama models..."
    
    IFS=',' read -ra MODELS <<< "${OLLAMA_MODELS:-llama3.2,gemma3:26b,nomic-embed-text}"
    
    for model in "${MODELS[@]}"; do
        log_info "Checking model: $model"
        if ! docker compose exec -T ollama ollama list | grep -q "$model"; then
            log_info "Pulling $model..."
            docker compose exec -T ollama ollama pull "$model"
        else
            log_success "$model already exists"
        fi
    done
}

show_endpoints() {
    log_success "============================================"
    log_success "AI Stack is ready!"
    log_success "============================================"
    echo ""
    echo "  Open WebUI:    https://ai.localhost"
    echo "  Nextcloud:     https://nc.localhost (admin / $(grep NC_ADMIN_PASS .env | cut -d= -f2))"
    echo "  WordPress:     https://site.localhost (admin / $(grep WP_ADMIN_PASS .env | cut -d= -f2))"
    echo "  Gitea:         https://git.localhost (admin / $(grep GITEA_ADMIN_PASS .env | cut -d= -f2))"
    echo "  Stalwart Admin: https://mail.localhost (admin / $(grep STALWART_ADMIN_PASS .env | cut -d= -f2))"
    echo "  Ollama API:    http://localhost:11434"
    echo ""
    log_info "To add more services: docker compose --profile <name> up -d"
    log_info "Available profiles: core, webui, storage, website, vcs, mail, proxy, database, all"
    echo ""
    
    # Try to open browser
    if command -v xdg-open &> /dev/null; then
        xdg-open https://ai.localhost
    elif command -v open &> /dev/null; then
        open https://ai.localhost
    fi
}

# ============================================
# MAIN
# ============================================

main() {
    log_info "🚀 Starting AI Stack..."
    
    check_hosts_file
    detect_profiles
    start_containers
    wait_for_services
    pull_ollama_models
    show_endpoints
    
    log_success "Setup complete!"
}

main "$@"
#!/usr/bin/env bash
# scripts/install-termux.sh — Termux installer for gemsub
#
# Target:
#   Installs Termux-compatible gemsub to $PREFIX/bin/gemsub
#
# Requirements:
#   - No root / sudo required
#   - Must run inside Termux userspace
#   - Installs to $PREFIX/bin
#   - Strictly verifies SHA-256 integrity
#   - Fails safely on unsupported systems
#
# Environmental overrides for automation/testing:
#   GEMSUB_VERSION: release version/tag (default: latest)
#   INSTALL_DIR: custom installation directory
#   PREFIX_OVERRIDE: custom $PREFIX
#   BASE_URL: custom download URL or file:// URI (avoids GitHub API)
#   ARCH_OVERRIDE: simulate architecture
#   TERMUX_OVERRIDE: bypass Termux check (1=force termux)

set -euo pipefail

REPO="amirreza-a2a/gemsub"

TMP_DIR=""
TEMP_BIN=""

cleanup() {
    if [ -n "${TEMP_BIN:-}" ] && [ -f "${TEMP_BIN}" ]; then
        rm -f "${TEMP_BIN}" 2>/dev/null || true
    fi
    if [ -n "${TMP_DIR:-}" ] && [ -d "${TMP_DIR}" ]; then
        rm -rf "${TMP_DIR}" 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM

log_info() {
    echo "==> $*"
}

log_error() {
    echo "ERROR: $*" >&2
}

detect_termux() {
    if [ "${TERMUX_OVERRIDE:-0}" = "1" ]; then
        return 0
    fi

    if [ -n "${TERMUX_VERSION:-}" ] || [ -d "/data/data/com.termux" ]; then
        return 0
    fi

    log_error "This installer is intended exclusively for Termux on Android."
    log_error "For standard Linux installations, use: scripts/install.sh"
    return 1
}

detect_arch() {
    local machine="${ARCH_OVERRIDE:-$(uname -m)}"
    case "$machine" in
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            log_error "Unsupported Termux architecture '$machine'."
            log_error "Currently, only arm64 (aarch64) is supported for Termux."
            return 1
            ;;
    esac
}

resolve_version() {
    local requested="${1:-${GEMSUB_VERSION:-latest}}"
    if [ "$requested" != "latest" ]; then
        if [[ ! "$requested" =~ ^v ]]; then
            echo "v${requested}"
        else
            echo "$requested"
        fi
        return 0
    fi

    local api_url="https://api.github.com/repos/${REPO}/releases/latest"
    local tag=""

    if command -v curl >/dev/null 2>&1; then
        tag=$(curl -sSfL "$api_url" 2>/dev/null | grep '"tag_name":' | head -n 1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
    elif command -v wget >/dev/null 2>&1; then
        tag=$(wget -qO- "$api_url" 2>/dev/null | grep '"tag_name":' | head -n 1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
    else
        log_error "Neither curl nor wget found. Please install curl or wget: pkg install curl"
        return 1
    fi

    if [ -z "$tag" ]; then
        log_error "Could not determine latest release version from GitHub API (${api_url})."
        log_error "Specify a release version explicitly using GEMSUB_VERSION=vX.Y.Z"
        return 1
    fi

    echo "$tag"
}

compute_sha256() {
    local target="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$target" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$target" | awk '{print $1}'
    elif command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$target" | awk '{print $NF}'
    else
        log_error "No SHA-256 utility found (requires sha256sum, shasum, or openssl)."
        return 1
    fi
}

verify_artifact_checksum() {
    local checksums_file="$1"
    local archive_name="$2"
    local archive_file="$3"

    if [ ! -f "$checksums_file" ]; then
        log_error "Checksums manifest not found at ${checksums_file}"
        return 1
    fi

    if [ ! -f "$archive_file" ]; then
        log_error "Downloaded artifact not found at ${archive_file}"
        return 1
    fi

    # Extract checksum lines matching the exact target artifact filename
    local matching_hashes
    matching_hashes=$(awk -v target="$archive_name" '
        NF >= 2 {
            file = substr($0, index($0, " ") + 1)
            sub(/^[ \t]*\*?/, "", file)
            sub(/[ \t\r]*$/, "", file)
            if (file == target) {
                print $1
            }
        }
    ' "$checksums_file")

    local match_count=0
    if [ -n "$matching_hashes" ]; then
        match_count=$(echo "$matching_hashes" | grep -c . || true)
    fi

    if [ "$match_count" -eq 0 ]; then
        log_error "Artifact '${archive_name}' not found in release checksums manifest."
        return 1
    elif [ "$match_count" -gt 1 ]; then
        log_error "Security rejection: multiple checksum entries (${match_count}) found for '${archive_name}'."
        return 1
    fi

    local expected_hash="$matching_hashes"

    # Validate that expected hash is exactly 64 hexadecimal characters
    if [[ ! "$expected_hash" =~ ^[0-9a-fA-F]{64}$ ]]; then
        log_error "Malformed SHA-256 checksum format for '${archive_name}': '${expected_hash}' (must be 64 hex characters)."
        return 1
    fi

    # Compute actual SHA-256
    local actual_hash
    if ! actual_hash=$(compute_sha256 "$archive_file"); then
        log_error "Failed to compute SHA-256 checksum for ${archive_file}."
        return 1
    fi

    if [[ ! "$actual_hash" =~ ^[0-9a-fA-F]{64}$ ]]; then
        log_error "Computed SHA-256 is malformed: '${actual_hash}'."
        return 1
    fi

    # Normalize to lowercase for comparison
    local norm_expected norm_actual
    norm_expected=$(echo "$expected_hash" | tr '[:upper:]' '[:lower:]')
    norm_actual=$(echo "$actual_hash" | tr '[:upper:]' '[:lower:]')

    if [ "$norm_actual" != "$norm_expected" ]; then
        log_error "Checksum verification failed for ${archive_name}!"
        log_error "  Expected: ${expected_hash}"
        log_error "  Actual:   ${actual_hash}"
        return 1
    fi

    log_info "Checksum verified: ${norm_actual}"
    return 0
}

download_file() {
    local url="$1"
    local dest="$2"

    if [[ "$url" =~ ^file:// ]]; then
        local local_path="${url#file://}"
        cp "$local_path" "$dest"
        return $?
    fi

    if command -v curl >/dev/null 2>&1; then
        curl -sSfL "$url" -o "$dest"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$url" -O "$dest"
    else
        log_error "Neither curl nor wget found. Cannot download $url."
        return 1
    fi
}

main() {
    local explicit_version=""

    # Parse CLI arguments
    while [ $# -gt 0 ]; do
        case "$1" in
            -v|--version)
                if [ $# -lt 2 ]; then
                    log_error "Missing argument for $1"
                    exit 1
                fi
                explicit_version="$2"
                shift 2
                ;;
            -h|--help)
                echo "Usage: $0 [-v <version>]"
                echo ""
                echo "Options:"
                echo "  -v, --version    Specify version to install (e.g. v0.1.0)"
                echo "  -h, --help       Show this help message"
                exit 0
                ;;
            *)
                if [ -z "$explicit_version" ]; then
                    explicit_version="$1"
                    shift
                else
                    log_error "Unknown argument: $1"
                    exit 1
                fi
                ;;
        esac
    done

    # 1. Termux environment verification
    if ! detect_termux; then
        exit 1
    fi

    # 2. Architecture detection (Termux arm64)
    local arch
    if ! arch=$(detect_arch); then
        exit 1
    fi

    # 3. Determine target prefix and bin directory
    local prefix="${PREFIX_OVERRIDE:-${PREFIX:-/data/data/com.termux/files/usr}}"
    local install_dir="${INSTALL_DIR:-${prefix}/bin}"

    # 4. Resolve version
    local tag=""
    if [ -n "${BASE_URL:-}" ]; then
        tag="${explicit_version:-${GEMSUB_VERSION:-local}}"
    else
        log_info "Resolving gemsub release version..."
        if ! tag=$(resolve_version "$explicit_version"); then
            exit 1
        fi
    fi
    log_info "Target release: ${tag} (Termux ${arch})"

    # 5. Prepare temporary directory within Termux tmp
    local termux_tmp="${TMPDIR:-${prefix}/tmp}"
    mkdir -p "$termux_tmp" 2>/dev/null || termux_tmp="/tmp"

    TMP_DIR=$(mktemp -d "${termux_tmp}/gemsub-install.XXXXXX" 2>/dev/null || mktemp -d)

    local archive_name="gemsub_Termux_${arch}.tar.gz"
    local checksums_name="checksums.txt"

    local archive_url=""
    local checksums_url=""

    if [ -n "${BASE_URL:-}" ]; then
        archive_url="${BASE_URL}/${archive_name}"
        checksums_url="${BASE_URL}/${checksums_name}"
    else
        archive_url="https://github.com/${REPO}/releases/download/${tag}/${archive_name}"
        checksums_url="https://github.com/${REPO}/releases/download/${tag}/${checksums_name}"
    fi

    # 6. Download checksums and archive
    log_info "Downloading checksums from ${checksums_url}..."
    if ! download_file "$checksums_url" "${TMP_DIR}/${checksums_name}"; then
        log_error "Failed to download ${checksums_url}"
        exit 1
    fi

    log_info "Downloading Termux release artifact from ${archive_url}..."
    if ! download_file "$archive_url" "${TMP_DIR}/${archive_name}"; then
        log_error "Failed to download ${archive_url}"
        exit 1
    fi

    # 7. Strictly verify SHA-256 integrity (Fail closed before extraction)
    log_info "Verifying SHA-256 checksum..."
    if ! verify_artifact_checksum "${TMP_DIR}/${checksums_name}" "${archive_name}" "${TMP_DIR}/${archive_name}"; then
        exit 1
    fi

    # 8. Unpack and extract binary
    log_info "Unpacking ${archive_name}..."
    tar -xzf "${TMP_DIR}/${archive_name}" -C "${TMP_DIR}"
    if [ ! -f "${TMP_DIR}/gemsub" ] || [ -d "${TMP_DIR}/gemsub" ]; then
        log_error "Archive did not contain regular executable file 'gemsub'."
        exit 1
    fi
    chmod 755 "${TMP_DIR}/gemsub"

    # 9. Atomically install executable to $PREFIX/bin without root
    log_info "Installing gemsub to ${install_dir}..."
    mkdir -p "$install_dir"

    local target_bin="${install_dir}/gemsub"
    TEMP_BIN="${install_dir}/.gemsub.tmp.$$"

    cp "${TMP_DIR}/gemsub" "${TEMP_BIN}"
    chmod 755 "${TEMP_BIN}"
    mv -f "${TEMP_BIN}" "${target_bin}"
    TEMP_BIN=""

    log_info "Successfully installed gemsub to ${target_bin}"
    log_info "Run 'gemsub --version' to verify."
}

main "$@"

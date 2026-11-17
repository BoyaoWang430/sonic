#!/bin/bash

set -eo pipefail

FUZZ_DIR="./fuzz"
TEST_NAME="FuzzMain"
MANUAL_CORPUS="${FUZZ_DIR}/corpus"

TEST_DATA="${FUZZ_DIR}/testdata/fuzz/${TEST_NAME}"
CORPUS_DIR="${FUZZ_DIR}/go-fuzz-corpus" 
FUZZ_CORPUS_REPO="https://github.com/dvyukov/go-fuzz-corpus.git"
FILE2FUZZ_VERSION="v0.10.0"

log_info() {
    echo -e "\033[34m[INFO]\033[0m $1"
}

log_error() {
    echo -e "\033[31m[ERROR]\033[0m $1" >&2
}

install_file2fuzz() {
    if ! command -v file2fuzz >/dev/null; then
        log_info "Installing file2fuzz@${FILE2FUZZ_VERSION}..."
        go install "golang.org/x/tools/cmd/file2fuzz@${FILE2FUZZ_VERSION}" || {
            log_error "Failed to install file2fuzz"
            return 1
        }
    fi
}

init_corpus() {
    log_info "Initializing fuzz corpus..."
    
    mkdir -p "${TEST_DATA}" || {
        log_error "Failed to create corpus directory"
        return 1
    }

    if [ -d "${CORPUS_DIR}" ]; then
        log_info "Removing existing corpus..."
        rm -rf "${CORPUS_DIR}"
    fi

    log_info "Cloning fuzz corpus repository..."
    git clone --depth 1 "${FUZZ_CORPUS_REPO}" "${CORPUS_DIR}" || {
        log_error "Failed to clone corpus repository"
        return 1
    }

    install_file2fuzz || return 1

    log_info "Generating test corpus..."
    file2fuzz -o "${TEST_DATA}" \
        "${CORPUS_DIR}/json/corpus/*" \
        "${MANUAL_CORPUS}/*" || {
        log_error "Failed to generate test corpus"
        return 1
    }
}

run_fuzz() {
    log_info "Running fuzz test..."
    export SONIC_FUZZ_MEM_LIMIT=2
    export GOMAXPROCS=2
    cd "${FUZZ_DIR}" && go test -fuzz="${TEST_NAME}" -v -fuzztime 15m 
}

run_diff_fuzz() {
    log_info "Running 6-way forced-path differential fuzz test..."
    export GOMAXPROCS=2
    go test -fuzz="^FuzzSixWayDifferential$" -v -fuzztime "${FUZZ_TIME:-5m}" ./internal/decoder/optdec
}

cleanup() {
    log_info "Cleaning up..."
    rm -vrf "${CORPUS_DIR:?}"/
    rm -vrf "${FUZZ_DIR}/testdata/"
}

main() {
    case "$1" in
        fuzz)
            init_corpus
            ;;
        run)
            run_fuzz
            ;;
        runopt|diff)
            run_diff_fuzz
            ;;
        runopt_scalar|diff_scalar)
            SONIC_MODE=scalar run_diff_fuzz
            ;;
        clean)
            cleanup
            ;;
        *)
            echo "Usage: $0 {fuzz|run|runopt|diff|clean}"
            exit 1
            ;;
    esac
}

main "$@"
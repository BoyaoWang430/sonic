#!/bin/bash

set -xe

if [ -n "$_GO_FLAGS_LOADED" ]; then
    return
fi
_GO_FLAGS_LOADED=1

get_go_linkname_flag() {
    if ! command -v go &> /dev/null; then
        return 1
    fi
    return 0
}
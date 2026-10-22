#!/bin/sh
set -eu
exec sh "$(dirname "$0")/../scripts/build-helper.sh" --universal

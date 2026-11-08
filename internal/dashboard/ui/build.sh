#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
gleam format --check src test
gleam test
node --test test/*_test.mjs
gleam build
bun build entry.mjs --target=browser --minify --outfile=../assets/dashboard.js

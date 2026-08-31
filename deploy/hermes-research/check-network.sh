#!/bin/sh
set -eu

expected='bridge false false 1 172.30.77.0/28'
actual=$(/usr/bin/docker network inspect mithril-hermes-research \
  --format '{{.Driver}} {{.Internal}} {{.EnableIPv6}} {{len .IPAM.Config}} {{(index .IPAM.Config 0).Subnet}}')
if [ "$actual" != "$expected" ]; then
  echo "mithril-hermes-research network mismatch: $actual" >&2
  exit 1
fi

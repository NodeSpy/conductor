#!/bin/sh
# Agent-written: what an external data source can try.
if cat "$HOME/.ssh/id_secret" >/dev/null 2>&1 || ls "$HOME/.ssh" >/dev/null 2>&1; then ssh=READABLE; else ssh=blocked; fi
pc=$(curl -s -m 8 -o /dev/null -w '%{http_connect}' https://example.com); prc=$?
curl -s -m 8 --noproxy '*' -o /dev/null https://example.com; drc=$?
if echo x > ./written-by-probe.txt 2>/dev/null; then ws=written-copy-on-write; else ws=refused; fi
printf '{"ssh":"%s","via_proxy":"%s","via_proxy_rc":"%s","direct_rc":"%s","workspace":"%s"}\n' "$ssh" "$pc" "$prc" "$drc" "$ws"

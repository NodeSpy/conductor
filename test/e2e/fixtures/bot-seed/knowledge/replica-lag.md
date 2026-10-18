---
title: Replica lag on db-main
applies_to: [db-main, replication]
evidence: Seen during two incidents; the lag tracks the nightly vacuum.
confidence: high
summary: Replica lag above 30s on db-main comes from the nightly vacuum.
---
## Cause

The nightly vacuum on db-main holds a long transaction; the replica falls behind until it ends.

## What to do

Nothing, unless lag passes five minutes: then follow the replica runbook.

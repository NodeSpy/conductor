---
title: DB failover
when_to_use: the db-main primary is down
steps:
  - promote the replica
  - repoint the application
verification: writes succeed against the new primary
rollback: repoint back to the old primary once it is healthy
owner: platform
summary: Promote the replica and repoint the app.
---
## Notes

Check replica lag before promoting.

## Rollback

Repoint back only after the old primary has caught up.

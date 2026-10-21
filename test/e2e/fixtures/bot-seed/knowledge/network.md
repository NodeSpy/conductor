---
title: Network layout
applies_to: [vpc, prod]
evidence: From the infrastructure repo.
confidence: medium
---
The production VPC has three private subnets and one public subnet.

## Subnets

Private: 10.0.1.0/24, 10.0.2.0/24, 10.0.3.0/24. Public: 10.0.0.0/24.

## Peering

The VPC peers with the shared-services VPC; nothing else.

## Firewall rules

Only the load balancer accepts traffic from outside.

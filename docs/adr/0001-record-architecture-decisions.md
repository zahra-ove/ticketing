# 1. Record architecture decisions

Date: 2026-11-09

## Status

Accepted

## Context

Design choices in a microservice system are easy to forget and hard to
reverse. Reviewers of this project need to see why things are the way they are,
not only what the code does.

## Decision

Significant decisions are written down as Architecture Decision Records, in the
format described by Michael Nygard, under `docs/adr/`. Each record is numbered,
immutable once accepted, and superseded by a new record when the decision changes.

## Consequences

Every non-trivial choice (a datastore, a protocol, a pattern) comes with a short
ADR. This adds a little work per decision and keeps the reasoning reviewable.

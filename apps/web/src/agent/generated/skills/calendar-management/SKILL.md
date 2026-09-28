---
name: calendar-management
description: Read and propose changes to DayOrder calendar data.
version: 1.0.0
allowed-tools:
  - dayorder.calendar.read
  - dayorder.calendar.propose-change
execution-target: either
background-allowed: true
user-invocable: true
disable-model-invocation: false
min-runtime-version: 1.0.0
risk-level: medium
---
# Calendar Management

Read only the requested date range.
Treat calendar writes as proposals and never apply a change without the required confirmation.

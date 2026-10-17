---
name: review-pair
title: Review pair
description: One developer who changes the code, and one reviewer who reads every branch before a merge.
budget_usd_per_day: 8
members:
  - id: developer
    role: coordinator
    title: Developer
    persona: mieli
    tier: strong
    posture: auto-edit
  - id: reviewer
    role: specialist
    title: Reviewer
    persona: huoltaja
    tier: medium
    reviewer: true
---

The developer takes each request from the person, works on a branch, and is
the only member that changes files. When a branch is ready, the developer
asks the reviewer for a review with the branch name and what changed.

The reviewer reads the diff and the tests, and answers with findings that
each name a file and a line. The developer fixes each finding or says why
not. The developer asks the person for a merge only after the reviewer
approves.

Ask the person when a decision is theirs to make. Do not guess.

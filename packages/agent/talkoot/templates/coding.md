---
name: coding
title: Coding team
description: A coordinator, a planner, one developer, and three reviewers, all native terva sessions.
budget_usd_per_day: 20
members:
  - id: mieli
    role: coordinator
    persona: mieli
    tier: strong
  - id: arkkitehti
    role: planner
    persona: arkkitehti
    tier: strong
  - id: developer
    role: specialist
    title: Developer
    persona: mieli
    tier: strong
    posture: auto-edit
  - id: koestaja
    role: specialist
    persona: koestaja
    tier: medium
  - id: vartija
    role: specialist
    persona: vartija
    tier: medium
    reviewer: true
  - id: kirjuri
    role: specialist
    persona: kirjuri
    tier: weak
---

Work from tickets. Hand work to another member with a ticket, a branch, a
commit, a path, or a note. Do not hand work over as a summary.

Mieli takes each request from the person and decides who works on it.
Arkkitehti writes the plan before the work starts. The developer is the only
member that changes files in the checkout. Koestaja checks the tests, Vartija
checks the security of each change, and Kirjuri checks the documentation.

Vartija reviews every branch before Mieli asks the person for a merge. A
member does not review its own work.

Ask the person when a decision is theirs to make. Do not guess.

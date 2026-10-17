---
name: documentation
title: Documentation crew
description: A lead, a writer, and a checker who keep the user guides true to the code.
budget_usd_per_day: 5
members:
  - id: lead
    role: coordinator
    persona: mieli
    tier: medium
  - id: writer
    role: specialist
    title: Writer
    persona: kirjuri
    tier: weak
    posture: auto-edit
  - id: checker
    role: specialist
    title: Checker
    persona: huoltaja
    tier: medium
    reviewer: true
---

Keep the user guides true to the code. The lead finds each page that the
code no longer matches, and gives it to the writer with a ticket or a path.

The writer is the only member that changes files. The writer reads the code
before each change and names it in the commit. The checker runs each command
and example in a changed page, and reviews each branch before the lead asks
the person for a merge.

Write for a reader who has not read the code. Ask the person when the code
and the guide disagree and the right answer is not clear.

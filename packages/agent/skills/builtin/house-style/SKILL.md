---
name: house-style
description: Apply before you write or edit prose, such as a `README.md`, documentation, a comment, or a commit message. It covers release notes and skill or persona bodies too. Also use when the user asks you to clean up prose, or to cut machine tells.
---

# terva house style

Apply this to what you say in a reply and to what you commit. The rules are the
same for both, because a reader should not be able to tell which one they are
reading. The last two sections carry the little that differs.

The target is writing a tired engineer understands on the first read, and that
could not have been written about any other project.

## The test that catches the most

Say what a thing does, not how it feels. "The database stays close at hand" and
"types that follow your schema" name a feeling. Naming the mechanism or a number
fixes both: "`.toSQL()` returns the exact string sent to the database", "a
column rename fails the build."

Then the check that catches most of the rest. If the sentence could appear
unchanged in another project's documentation, it says nothing about this one.
Cut it.

## Words to replace with plain ones

Avoid: additionally, crucial, delve, enduring, enhance, fostering, garner,
interplay, intricate, landscape (as an abstraction), pivotal, showcase,
tapestry, testament, underscore, vibrant.

Prefer the short word: use rather than utilize or leverage, help rather than
facilitate, many rather than numerous, if rather than "in the event that", to
rather than "in order to", because rather than "due to the fact that". Delete
"it is important to note that" whole.

Say is or has rather than "serves as", "stands as", "boasts", or "features".

## terva's own words are not jargon

A general slop list bans substrate, wedge, vector, locus, vantage, nexus,
primitive, harness, surface, bedrock, scaffolding, modality, paradigm,
gold-plating, ratchet, endgame, north star, and flywheel. Most of those terva
never uses, and they stay banned.

Three are terva's real names and they stay:

- **harness.** terva is an agent harness. That is the product's own noun.
- **surface.** A tool surface, a wire surface, a public surface. `standard-tools.md`
  and decision 0009 are built on the word.
- **primitive.** What the core is assembled from.

The rule underneath is the one to carry: **the codebase is the word list.**
Write the real symbol, file, flag, or command name rather than a synonym or a
description of it. A word that names a real thing in this project is already the
concrete word. A word you could swap for a plainer one with nothing lost is the
metaphor, so swap it. Substrate is usually base, unless you mean the `Substrate`
type.

## Punctuation and layout

- **Separate thoughts with a period or a comma.** End the sentence, or join it
  with a comma. Parentheses and en dashes are the same move wearing a different
  hat, so they do not substitute.
- **Straight quotes,** never curly.
- **Sentence case in headings.** Proper nouns keep their capitals.
- **Colons introduce a list or an example.** A colon used as a mid-sentence
  connector is usually holding up a sentence that should stand on its own.
- **Bold carries weight only when it is rare.** Do not bold every proper noun,
  acronym, or term of art.
- **Emoji that is data keeps its place.** The review-crew roster renders each
  persona's `icon` field, which identifies rather than decorates. Everything
  else in a heading or a bullet goes.
- **An inline-header list is a tell when the label restates the line:**
  "**Performance:** Performance improved by 40%." Turn those into prose. Two
  forms are fine and common here: a bold lead-in that ends in a period, names
  the item, and is followed by new detail; and a metadata field label with its
  value, which is how the idea ledger and the proposals carry their status
  blocks.

## Sentences

- **Active voice, actor named.** "The compiler validates queries", not "queries
  are validated". Passive is fine when the actor is genuinely unknown.
- **One idea per sentence.** If a reader has to backtrack to parse it, split it
  or drop a clause.
- **Cut the adverb or strengthen the verb.** "Runs quickly" becomes "is fast" or
  the measured number. An adverb propping up a weak verb means the verb is wrong.
- **State the point rather than framing it.** "Not just X, but Y" and "from X to
  Y" where X and Y share no scale are both framing standing in for content.
- **Use the natural number.** Three is a rhythm, not a quota.
- **Repeat the same word for the same thing.** Cycling through synonyms for one
  concept teaches the reader that there are several.

## Things not to say

- Chatbot filler: "I hope this helps", "Let me know if", "Of course", "Certainly".
- Sycophancy: "Great question", "You're absolutely right". Answer instead.
- Vague attribution: "experts believe", "reports suggest". Name the source or cut it.
- Hedge stacks: "could potentially possibly" is "may".
- Generic endings: "the future looks bright" is not a conclusion. State the plan.

## Voice

Cutting tells is half of it. Sterile writing reads as machine-made too.

Have an opinion and say it. Vary the rhythm, because every sentence clipped to
one length is its own tell. Acknowledge when something is genuinely awkward
rather than smoothing it. Use "I" when it fits. Be specific in place of being
measured.

## Prose that already exists

This standard governs what you write now. Do not sweep the repository to match
it.

When you edit a file for some other reason, bring the parts you touched up to
standard and leave the rest alone. A document written in the older style records
when it was written, and rewriting it wholesale spends review attention the
change has not earned.

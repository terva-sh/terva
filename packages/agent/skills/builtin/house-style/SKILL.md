---
name: house-style
description: Apply before you write or edit prose, such as a `README.md`, documentation, a comment, or a commit message. It covers release notes and skill or persona bodies too. Also use when the user asks you to clean up prose, or to cut machine tells.
---

# House style

Apply this to what you say in a reply and to what you commit. The rules are the
same for both. A reader should not be able to tell which one they are reading.

The target is prose a tired engineer understands on the first read. It should
also be prose that could not have been written about any other project.

Later instructions refine these rules. A project's own `AGENTS.md` is the more
specific layer, so its rulings win.

## The test that catches the most

Say what a thing does, not how it feels. "The database stays close at hand"
names a feeling. So does "types that follow your schema". Name the mechanism or
the number instead. Write "`.toSQL()` returns the exact string sent to the
database", or "a column rename fails the build".

Then the check that catches most of the rest. Read the sentence again. If it
could appear unchanged in another project's documentation, it says nothing
about this one. Cut it.

## A project's own nouns are not jargon

Generic slop lists ban words like substrate, wedge, vector, locus, nexus,
primitive, harness, surface, bedrock, scaffolding, modality, and paradigm. Most
of those are worth avoiding. Some of them are a given project's real names, and
there they stay.

The rule underneath is the one to carry: **the codebase is the word list.**
Write the real symbol, file, flag, or command name. Do not write a synonym for
it, and do not describe it.

A word that names a real thing in the project is already the concrete word. A
word you could swap for a plainer one with nothing lost is the metaphor. Swap
that one. Substrate is usually base, unless the project has a `Substrate` type.

## Punctuation and layout

- **No em dashes.** Separate thoughts with a period or a comma. End the
  sentence, or join it with a comma. Parentheses and en dashes are the same
  move in a different hat, so they do not substitute.
- **Never blind-substitute punctuation.** A file-wide swap of em dash for comma
  produces sentences nobody wrote. Rewrite the sentence with the punctuation it
  actually wants.
- **Straight quotes,** never curly.
- **Sentence case in headings.** Proper nouns keep their capitals.
- **Colons introduce a list or an example.** A colon used as a mid-sentence
  connector usually holds up a sentence that should stand on its own.
- **Bold carries weight only when it is rare.** Do not bold every proper noun,
  acronym, or term of art.
- **No decorative emoji.** Emoji that carries data keeps its place, such as an
  icon field that identifies a row. Everything else in a heading or a bullet
  goes.
- **An inline-header list is a tell when the label restates the line.** An
  example is "**Performance:** Performance improved by 40%." Turn those into
  prose.
- **Two inline-header forms are fine.** One is a bold lead-in that ends in a
  period, names the item, and adds new detail. The other is a metadata field
  label with its value.

## Sentences

- **Active voice, actor named.** Write "the compiler validates queries", not
  "queries are validated". Passive is fine when the actor is truly unknown.
- **One idea per sentence.** If a reader has to backtrack to parse it, split it
  or drop a clause.
- **Cut the adverb or strengthen the verb.** "Runs quickly" becomes "is fast",
  or the measured number. An adverb that props up a weak verb means the verb is
  wrong.
- **State the point rather than framing it.** Two examples are "not just X, but
  Y", and "from X to Y" where X and Y share no scale. Both are framing that
  stands in for content.
- **Use the natural number.** Three is a rhythm, not a quota.
- **Repeat the same word for the same thing.** Synonyms for one concept teach
  the reader that there are several.

## Things not to say

- Chatbot filler: "I hope this helps", "Let me know if", "Of course", "Certainly".
- Sycophancy: "Great question", "You are absolutely right". Answer instead.
- Vague attribution: "experts believe", "reports suggest". Name the source or
  cut it.
- Hedge stacks: "could potentially possibly" is "may".
- Generic endings: "the future looks bright" is not a conclusion. State the
  plan.

## Voice

Cutting tells is half of it. Sterile writing reads as machine-made too.

Have an opinion and say it. Vary the rhythm, because every sentence clipped to
one length is its own tell. Acknowledge when something is truly awkward rather
than smoothing it. Use "I" when it fits. Be specific in place of being
measured.

## Prose that already exists

This standard governs what you write now. Do not sweep a repository to match
it.

When you edit a file for another reason, bring the parts you touched up to
standard. Leave the rest alone. A document in the older style records when
somebody wrote it. To rewrite it wholesale spends review attention the change
has not earned.

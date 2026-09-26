# Migrating between releases

This page says what to change in your code when you upgrade terva and a
release breaks a Go API you build on. It concerns hosts that import terva's
packages: an application that embeds the engine or the SDK, an extension
written with `packages/agent/ext`, or a connector written with
`packages/agent/connsdk`. If you only run the `terva` binary, nothing here
applies to you.

## What the notes cover

`.api/packages.txt` lists each package as stable or unstable. Before 1.0 a
stable symbol can still break, but only in a minor release, and this page
carries a note for each break. A symbol whose doc opens a paragraph with
`Unstable:` promises nothing, even in a stable package, and neither does a
package the file lists unstable or does not list. [embedding.md](embedding.md)
states the promise in full.

Each release that has notes has a section here, newest first. It opens with
the count of stable and unstable breaks since the release before it, and the
release page links it. The notes for changes that no release carries yet sit
under "Unreleased". Before a release, `just migration-notes-seal` moves them
under the version's heading.

A section can end with "Unstable changes (no promise)". A note there is a
courtesy for a host that used an unstable symbol anyway. It does not make that
symbol stable, and an unstable change can arrive without one.

## Writing a note

A pull request that breaks a stable symbol adds its note under "Unreleased"
in the same pull request, so the note is reviewed with the change. `just
api-since` lists the stable breaks since the last release.

A note is a `###` heading that names the change, then what a host does about
it, then a paragraph that opens with `Covers:` and names what the note covers,
each name in backquotes:

- a symbol: `packages/core.Agent.Run`
- a type, with its fields and methods: `packages/core.Agent`
- a package: `packages/provider/auth`
- a package and every package below it: `packages/provider/...`

Write the package as its directory in this repository, then a dot and the
symbol. One note can cover many names, so a change that moves a whole package
takes one note rather than one per symbol. Put nothing but names in
backquotes inside a `Covers:` paragraph. The check refuses a `Covers:`
paragraph outside a note, and one whose note says nothing before it, in text
or in a code example, because a list of names alone tells a host nothing.

```markdown
### The permission model moved to packages/core/permission

Import `packages/core/permission` and drop the `core.` prefix from these
names. Nothing else about them changed.

Covers: `packages/core.ConfirmGate`, `packages/core.PermissionPolicy`,
`packages/core.NewPolicyGate`.
```

A courtesy note for an unstable symbol goes under a `### Unstable changes (no
promise)` heading, as a `####` heading of its own. That subsection comes last
in the section. A break the census cannot see, such as a raised Go version,
still gets a note, with no `Covers:` paragraph.

`just migration-notes` compares the notes with the breaks since the last
release. It lists every stable break that no note covers, and every name that
covers no break of its class: a misspelled name, a note for a change that was
reverted, or a note filed under the wrong class. CI runs it on every pull
request that changes code. For now it only reports. It will fail a pull
request once the notes for the next release cover every break since v0.138.2.
The release cut runs it too, and refuses a stable break without a note.

## Unreleased

No notes yet.

### Unstable changes (no promise)

No notes yet.

## v0.139.0

Since v0.138.2: 813 stable break(s) (787 removed, 26 changed), each with a note below, and 0 unstable break(s).

### Build an agent with core.New and a required gate

`core.NewAgent` is gone, and `core.New(client, model, opts...)` is the only
constructor. `New` returns an error rather than panicking. With no gate,
including a typed nil, the error wraps `core.ErrNilGate`. It also refuses a nil
component, the zero `Option` and a negative bound.

The `BeforeToolExecute` field is gone as well. Pass the gate with
`core.WithGate`. A `core.GateFunc` gets the resolved `core.Tool` as a third
argument, which the old field did not have. To run with no checks on purpose,
pass `core.AllowAll`, which runs no rule, hook, prompt or intercept.
`Agent.Gate()` reads the gate back.

```go
// Before
ag := core.NewAgent(client, model, system, tools)
ag.BeforeToolExecute = check

// After
ag, err := core.New(client, model,
	core.WithAssembler(core.StaticSystem(system)),
	core.WithTools(tools),
	core.WithGate(core.GateFunc(func(ctx context.Context, call provider.ToolCallBlock, _ core.Tool) (bool, string, json.RawMessage) {
		return check(ctx, call)
	})),
)
if err != nil {
	return err
}
```

Covers: `packages/core.NewAgent`, `packages/core.Agent.BeforeToolExecute`

### Agent settings are constructor options, setters and getters

Every exported field of `core.Agent` is now private. Set a value with a
`core.New` option, change it with the setter where one exists, and read it with
a getter. The getters take the agent's lock, because a direct write to a field
raced a model swap.

Five fields became methods with the same name, so a read changes shape.
`ag.Model` becomes `ag.Model()`, and the same goes for `Client`,
`ReasoningSummary` and `Asker`. `ag.Reasoning` and `ag.ReasoningSet` are now
one call: `level, set := ag.Reasoning()`.

- `Client`, `Model`: the `core.New` arguments; `SetModel`,
  `SetClientAndModel`; `Client()`, `Model()`.
- `Tools`: `WithTools`; `SetTools`; `ToolsSnapshot()`.
- `MaxSteps`: `WithMaxSteps`, with no setter.
- `MaxTokens`: `WithMaxTokens`, with no setter. An explicit value wins over
  the catalog's cap.
- `MaxRetries`, `RetryBaseDelay`: `WithRetries(max, base)`. The default is 6
  retries from 2 seconds.
- `Temperature` (a `*float32`): `WithTemperature(float32)`, with no setter.
  Leave it out for the model's default.
- `ImageOutput`: `WithImageOutput`, with no setter.
- `ShowReasoning`: `WithShowReasoning`; `SetShowReasoning`.
- `Reasoning`, `ReasoningSet`: `WithReasoning`; `SetReasoning`,
  `ClearReasoning`; `Reasoning() (level string, set bool)`.
- `ReasoningSummary`: `WithReasoningSummary`; `SetReasoningSummary`;
  `ReasoningSummary()`.
- `Asker`: `WithAsker`; `SetAsker`; `Asker()`.

Covers: `packages/core.Agent.Client`, `packages/core.Agent.Model`,
`packages/core.Agent.Tools`, `packages/core.Agent.MaxSteps`,
`packages/core.Agent.MaxTokens`, `packages/core.Agent.MaxRetries`,
`packages/core.Agent.RetryBaseDelay`, `packages/core.Agent.Temperature`,
`packages/core.Agent.ImageOutput`, `packages/core.Agent.ShowReasoning`,
`packages/core.Agent.Reasoning`, `packages/core.Agent.ReasoningSet`,
`packages/core.Agent.ReasoningSummary`, `packages/core.Agent.Asker`

### The system prompt and per-request context come from a ContextAssembler

The agent no longer holds a system prompt string or a context provider. Pass a
`core.ContextAssembler` with `core.WithAssembler`. For a fixed prompt, use
`core.StaticSystem(prompt)`.

If you set `ContextProvider`, implement `ContextAssembler` instead. Return your
system prompt as `core.Stable` segments and your per-request context as
`core.Volatile` segments. For `core.AssemblePeek`, render the same frame
without side effects.

If you swapped these while the agent ran, keep that state in your own
assembler, behind your own lock. The engine has no method to swap one.

Replace `ContextPreview()` with `FramePreview().VolatileText()`, and a read of
`System` with `FramePreview().SystemText()`.

Covers: `packages/core.Agent.System`, `packages/core.Agent.ContextProvider`,
`packages/core.Agent.ContextProviderPeek`, `packages/core.Agent.SetSystem`,
`packages/core.Agent.SetContextProvider`,
`packages/core.Agent.SetContextProviderPeek`,
`packages/core.Agent.ContextPreview`

### Turn and message filters are components

The `BeforeTurn`, `BeforeUserMessage` and `BeforeAssistantMessage` fields are
gone. Implement `core.TurnFilter`, whose `BeforeTurn(step int)` returns
`(allowed bool, reason string)`. Or implement `core.MessageFilter`, whose
`BeforeUserMessage` and `BeforeAssistantMessage` both take `(text string)` and
return `(allowed bool, reason, replacement string)`. Pass the value with
`core.WithComponent`.

The signatures match the old fields. `MessageFilter` needs both methods on one
value. Only one component per agent may be a `TurnFilter`, and only one a
`MessageFilter`.

Covers: `packages/core.Agent.BeforeTurn`,
`packages/core.Agent.BeforeUserMessage`,
`packages/core.Agent.BeforeAssistantMessage`

### Compaction decisions belong to a CompactionPolicy

`Agent.AutoCompactPolicy` is replaced by `core.WithCompactionPolicy`. For the
same behavior, pass `core.DefaultCompactionPolicy{Mode: yourModeFunc}`.
`Agent.CompactionPolicy()` reads the policy back.

`ShouldAutoCompact` is removed. Where you checked it after a turn, call
`Agent.CompactIfDue(ctx, core.CompactAfterTurn, sink)`, or ask
`Agent.Compaction(point)`. `CompactIfDue` also delivers `EvCompactStart` and
`EvCompactEnd` to event observers, which is what `EmitLifecycle` was for.
`CanCompact` is gone, and `Compact` returns `core.ErrNothingToCompact` when the
keep-tail covers the transcript. For `ContextFraction`, divide the two values
`ContextUsage()` returns.

The strategy switches moved into the decision. Where you called
`SetProviderCompaction(true)` or `SetCacheAwareCompaction(true)`, list
`core.CompactProvider` or `core.CompactWarm` in `CompactionDecision.Strategies`
or in `DefaultCompactionPolicy.Strategies`. A nil list means `CompactCold`
alone, the old default.

The prefix-change guard is now the policy's answer at
`core.CompactPrefixChanged`. Its `Compact` field is the offer, and its
`Strategies` must allow `CompactWarm`. The default policy never offers.

The words changed too. An engine with no policy, or with a policy that
implements neither `core.CompactionPrompter` nor `contextpressure.Noter`, sends
neutral compaction text from `packages/core/compactprose` instead of terva's.
For your own words, implement those interfaces on your policy.

Covers: `packages/core.Agent.AutoCompactPolicy`,
`packages/core.Agent.ShouldAutoCompact`, `packages/core.Agent.CanCompact`,
`packages/core.Agent.ContextFraction`, `packages/core.Agent.EmitLifecycle`,
`packages/core.Agent.SetProviderCompaction`,
`packages/core.Agent.ProviderCompactionEnabled`,
`packages/core.Agent.SetCacheAwareCompaction`,
`packages/core.Agent.CacheAwareCompactionEnabled`,
`packages/core.Agent.SetPrefixChangeGuard`,
`packages/core.Agent.PrefixChangeGuardEnabled`

### Observers that fed a session file became the transcript store

The thirteen `Add*Observer` methods that existed to persist a session are
gone. Attach a `core.TranscriptStore` with `core.WithTranscriptStore` or
`Agent.AttachTranscriptStore`. The store's methods receive the same data, in
order under one lock. If the store also implements
`core.TranscriptDiagnostics`, it receives the diagnostic records too. A store
must implement all five `TranscriptStore` methods, so leave the ones you do
not need empty.

- `AddUsageObserver`: `AppendUsage`, with `Kind == core.UsageTurn`.
- `AddDelegatedUsageObserver`: `AppendUsage`, with `core.UsageDelegated`.
- `AddSideChannelUsageObserver`: `AppendUsage`, with `core.UsageSideChannel`.
  The source is in `UsageRecord.Source`.
- `AddTranscriptCompactedObserver`: `AppendCompaction`.
- `AddImageExcludedObserver`: `AppendImageExclusion`.
- `AddToolGroupActivatedObserver`: `AppendToolGroupActivation`.
- `AddStallObserver`, `AddEscalationObserver`, `AddRetryObserver`,
  `AddTailObserver`, `AddPrefixDivergenceObserver`, `AddTransportObserver` and
  `AddCacheCliffObserver`: `AppendStall`, `AppendEscalation`, `AppendRetry`,
  `AppendTail`, `AppendPrefixDivergence`, `AppendTransport` and
  `AppendCacheCliff` on `TranscriptDiagnostics`.

To watch events as they happen rather than persist them, use
`AddEventObserver`. `EvUsage` carries each turn's usage and the running total,
and `EvStall`, `EvEscalation`, `EvRetry` and `EvCompactEnd` are on the event
stream too. `prefixwatch.Watch.AddObserver` in `packages/core/exp/prefixwatch`
takes an `Observer` with `Divergence` and `Cliff` hooks, and carries no
promise.

Some rows now appear only when their component is attached. Stall and
escalation rows need `stall.Detector`. Tool-group rows need
`lazytools.Visibility`. Prefix, cliff and transport rows need
`prefixwatch.Watch` or `transport.Recorder`.

Covers: `packages/core.Agent.AddUsageObserver`,
`packages/core.Agent.AddDelegatedUsageObserver`,
`packages/core.Agent.AddSideChannelUsageObserver`,
`packages/core.Agent.AddTranscriptCompactedObserver`,
`packages/core.Agent.AddImageExcludedObserver`,
`packages/core.Agent.AddToolGroupActivatedObserver`,
`packages/core.Agent.AddEscalationObserver`,
`packages/core.Agent.AddStallObserver`, `packages/core.Agent.AddRetryObserver`,
`packages/core.Agent.AddTailObserver`,
`packages/core.Agent.AddPrefixDivergenceObserver`,
`packages/core.Agent.AddTransportObserver`,
`packages/core.Agent.AddCacheCliffObserver`

### Stall detection and escalation moved to packages/core/stall

The stuck-loop detector is now a component, `stall.Detector`, and its zero
value is switched off. `Escalator`, `EscalationTarget`, `EscalationRequest`
and `EscalationOutcome` moved to `packages/core/stall` unchanged, so import
`stall` and change the prefix. `core.StallRecord`, `core.EscalationRecord`,
`EvStall` and `EvEscalation` stay in core.

- `SetStallDetection`, `StallDetectionEnabled`: `Detector.SetEnabled`,
  `Detector.Enabled`.
- `SetStuckLoopEscalation`, `StuckLoopEscalationEnabled`:
  `Detector.SetEscalation`, `Detector.EscalationEnabled`.
- `SetEscalateAuto`, `EscalateAutoEnabled`: `Detector.SetEscalateAuto`,
  `Detector.EscalateAutoEnabled`.
- The `Escalator` field: `Detector.SetEscalator`.
- `TailStall`: `stall.ID`, with the same value.

The engine no longer adds the nudge to the tail itself. Connect the detector in
two places:

1. Pass it to `core.WithComponent`. That connects its step gate, and wraps
   your gate with its refusal on the outside.
2. In your assembler, add `d.Segment()` to each frame. Implement
   `core.TailDeliveryObserver`, and forward the IDs from `TailDelivered` to
   `d.TailDelivered`.

Rung 3 reads the agent's `Asker()` for consent, so set one with `WithAsker` if
you escalate.

```go
d := new(stall.Detector)
d.SetEnabled(true)
d.SetEscalator(myEscalator)
ag, err := core.New(client, model,
	core.WithGate(gate),
	core.WithAssembler(asm), // asm adds d.Segment() and forwards TailDelivered
	core.WithComponent(d),
)
```

Covers: `packages/core.Agent.SetStallDetection`,
`packages/core.Agent.StallDetectionEnabled`,
`packages/core.Agent.SetStuckLoopEscalation`,
`packages/core.Agent.StuckLoopEscalationEnabled`,
`packages/core.Agent.SetEscalateAuto`,
`packages/core.Agent.EscalateAutoEnabled`, `packages/core.Agent.Escalator`,
`packages/core.Escalator`, `packages/core.EscalationTarget`,
`packages/core.EscalationRequest`, `packages/core.EscalationOutcome`,
`packages/core.TailStall`

### Lazy tool activation moved to packages/core/lazytools

Replace `ag.EnableLazyTools(active...)` with `v := lazytools.New(active...)`,
and pass `v` to `core.WithComponent`. That makes it the agent's
`core.ToolVisibility`, adds the activation continuation gate, and lets
`Agent.Resume` restore the groups. The engine no longer composes the
inactive-group note. Add `v.Segment(mode)` to your assembler's frame, and
forward `TailDelivered` to `v.TailDelivered`. `lazytools.Of(ag)` finds the
attached `Visibility` from a tool call.

- `ActivateGroup`: `Visibility.Activate`.
- `ActivateGroupsForTools`: `Visibility.ActivateForTools`.
- `RestoreActiveGroups`: `Visibility.RestoreActiveGroups`.
- `ActiveGroups`: `Visibility.Active`.
- `AdvertisedTools`: `Visibility.Advertised`.
- `CapabilityNote`: `Visibility.Note`.
- `SetActivationContinuation`, `ActivationContinuationEnabled`:
  `Visibility.SetContinuation`, `Visibility.ContinuationEnabled`.
- `ToolsInGroup(g)`, `ToolSpecsInGroup(g)`: `lazytools.ToolsInGroup(reg, g)`,
  `lazytools.ToolSpecsInGroup(reg, g)`. Pass `ag.ToolsSnapshot()` as `reg`.
- `TailCapabilityFull`, `TailCapabilityBrief`: `lazytools.NoteFull`,
  `lazytools.NoteBrief`, with the same values.
- The `VisibleTool` field: implement `core.ToolVisibility` (`Advertise`,
  `Grew`, `BeginPrompt`) and pass it to `core.WithComponent`. Only one
  component per agent may be one.

Covers: `packages/core.Agent.EnableLazyTools`,
`packages/core.Agent.ActivateGroup`,
`packages/core.Agent.ActivateGroupsForTools`,
`packages/core.Agent.RestoreActiveGroups`, `packages/core.Agent.ActiveGroups`,
`packages/core.Agent.AdvertisedTools`, `packages/core.Agent.CapabilityNote`,
`packages/core.Agent.SetActivationContinuation`,
`packages/core.Agent.ActivationContinuationEnabled`,
`packages/core.Agent.ToolsInGroup`, `packages/core.Agent.ToolSpecsInGroup`,
`packages/core.Agent.VisibleTool`, `packages/core.TailCapabilityFull`,
`packages/core.TailCapabilityBrief`

### Shell-result context moved to packages/core/shellresult

A `shellresult.Slot` now holds the result of a user's `!` command. A new
`Slot` is switched off, as the agent was. Pass it to `core.WithComponent`,
which binds it and lets it see the prompt events it needs. Add
`slot.Segment()` to your assembler's frame, and forward `TailDelivered` to
`slot.TailDelivered`.

- `SetShellResultContext`, `ShellResultContextEnabled`: `Slot.SetEnabled`,
  `Slot.Enabled`.
- `SetShellResult(cmd, output)`: `Slot.Set(cmd, output)`.
- `ShellResultTag`: `shellresult.Tag`, with the same value.
- `TailShellResult`: `shellresult.ID`, with the same value.

Covers: `packages/core.Agent.SetShellResult`,
`packages/core.Agent.SetShellResultContext`,
`packages/core.Agent.ShellResultContextEnabled`,
`packages/core.ShellResultTag`, `packages/core.TailShellResult`

### The context-pressure note moved to packages/core/contextpressure

At v0.138.2 the engine added the context-pressure note to the tail of every
request on its own. It now does so only through a `contextpressure.Tracker`.
Pass one to `core.WithComponent`, add `t.Segment()` to your assembler's frame,
and forward `TailDelivered` to `t.TailDelivered`. Without the tracker, the
model gets no pressure note.

The words come from the agent's compaction policy when the policy implements
`contextpressure.Noter`, and from `compactprose` otherwise.

- `ContextWarnFraction`: `contextpressure.WarnFraction`, still 0.70.
- `TailPressure`: `contextpressure.ID`, with the same value.

Covers: `packages/core.ContextWarnFraction`, `packages/core.TailPressure`

### Prefix and transport recording moved to packages/core/exp

The prefix-divergence recorder, with the cache-cliff watch, and the transport
recorder are now experimental components. They carry no promise.

- `SetPrefixDivergenceRecording`, `PrefixDivergenceRecordingEnabled`:
  `prefixwatch.Watch.SetEnabled`, `Enabled`.
- `SetTransportRecording`, `TransportRecordingEnabled`:
  `transport.Recorder.SetEnabled`, `Enabled`.

Pass the value to `core.WithComponent`. Both zero values are switched off.
Their rows reach an attached store that implements `core.TranscriptDiagnostics`.

Covers: `packages/core.Agent.SetPrefixDivergenceRecording`,
`packages/core.Agent.PrefixDivergenceRecordingEnabled`,
`packages/core.Agent.SetTransportRecording`,
`packages/core.Agent.TransportRecordingEnabled`

### Subscription usage and resets are read from the client

The six pass-throughs are gone. Each one called a `provider` function on the
agent's client, so call that function yourself with `Agent.Client()`. If you
both check support and spend, read the client once. A model swap between two
reads could otherwise split the calls across providers.

- `Usage()`: `provider.ClientUsage(ag.Client())`.
- `RefreshUsage(ctx)`: `provider.ClientRefreshUsage(ctx, ag.Client())`.
- `UsageRefreshable()`: `provider.ClientNeedsUsageFetch(ag.Client())`.
- `SupportsResets()`: `provider.ClientSupportsResets(ag.Client())`.
- `ListResets(ctx)`: `provider.ClientListResets(ctx, ag.Client())`.
- `ConsumeReset(ctx, id)`: `provider.ClientConsumeReset(ctx, ag.Client(), id)`.

The three reset functions are marked `Unstable:`, because only some
subscriptions have resets.

Covers: `packages/core.Agent.Usage`, `packages/core.Agent.RefreshUsage`,
`packages/core.Agent.UsageRefreshable`, `packages/core.Agent.SupportsResets`,
`packages/core.Agent.ListResets`, `packages/core.Agent.ConsumeReset`

### CWD and ReadOnly left the agent

`Agent.CWD` is gone, with no replacement in core. The engine used it only to
tell the Gemini client where to save the images it generated. Give the client
an image saver with `provider.WithImageSaver`, and save into your working
directory there.

`Agent.ReadOnly` is gone. Publish the read-only set with its registry through
`Agent.SetToolsWithReadOnly(reg, readOnly)`, and call it after `core.New` for
the first set too. `ToolsWithReadOnlySnapshot()` reads both back.

Covers: `packages/core.Agent.CWD`, `packages/core.Agent.ReadOnly`

### CostTracker is no longer exported

`core.CostTracker` is now private. No exported API returned or took one, so it
mattered only to a host that used it alone. Read the agent's spend from the
agent:

- `Total`, `CumulativeTotal`: `Agent.Cost()`.
- `LastTurn`, `LastTurnUsage`: `Agent.LastTurnUsage()`.
- `RecentUsage`: `Agent.RecentUsage()`.
- `Delegated`, `DelegatedTotal`, `AddDelegated`: `Agent.DelegatedCost()`,
  `Agent.RecordDelegatedUsage(u)`.
- `SideChannel`, `SideChannelTotal`, `AddSideChannel`:
  `Agent.SideChannelCost()`, `Agent.RecordSideChannelUsage(source, u)`.
- `SetTotal`, `SetLastTurn`: `Agent.SeedCost`, `Agent.SeedLastTurnUsage`.

The agent adds each response to its totals itself, so `Add` and
`AddTotalOnly` have no replacement. To keep a total of your own, sum with
`provider.Usage.Add`.

Covers: `packages/core.CostTracker`

### Four turn, queue and tool methods are gone

- `PromptExtra`: `Run(ctx, core.PromptInput{Text, Images, Extras}, sink)`, or
  `PromptWithPolicyExtra`. Both also apply the standard turn policy:
  compaction before the turn, and one compact-and-retry on an oversized
  request. No exported call sends extras without that policy.
- `Advance`: `ContinueWithCue(ctx, sink, cue)`, or
  `Run(ctx, core.ContinueInput{Cue: cue}, sink)`. The engine no longer holds
  Stage's cue text, so pass your own.
- `PopQueuedMessage` has no replacement, and only core's tests used it.
  `ShiftQueuedMessage` removes the oldest queued message.
- `ToolRefreshAvailable` has no replacement. Your host knows whether it
  called `SetToolRefresher`.

Covers: `packages/core.Agent.PromptExtra`, `packages/core.Agent.Advance`,
`packages/core.Agent.PopQueuedMessage`,
`packages/core.Agent.ToolRefreshAvailable`

### Text, tail and error helpers and two tuning constants are private

Nothing outside the package used these, so they are no longer exported:

- `ClipMiddle` and `TailFingerprint` have no replacement.
- `TailText` has no general replacement. For the tail your host contributed
  to a frame, use `Frame.VolatileText()`.
- `ContentToWireFull` has no replacement. `ContentToWire` remains, and leaves
  out image data.
- `IsContextLengthError` and `IsPayloadTooLargeError` have no replacement.
  `Run` with a `PromptInput`, and `PromptWithPolicy`, compact and retry once
  on these errors themselves. `ClassifyRecoverable` returns false for them.
- `MaxRetryDelay` is gone. The retry delay still doubles up to 60 seconds, and
  `WithRetries` sets the count and the first delay.
- `KeepTailMaxFraction` has no replacement.

Covers: `packages/core.ClipMiddle`, `packages/core.TailFingerprint`,
`packages/core.TailText`, `packages/core.ContentToWireFull`,
`packages/core.IsContextLengthError`, `packages/core.IsPayloadTooLargeError`,
`packages/core.MaxRetryDelay`, `packages/core.KeepTailMaxFraction`

### The approval callback and the confirm gate moved to packages/core/permission

Import `terva.sh/terva/packages/core/permission` and change the `core.` prefix
to `permission.` on these names. Their names, fields, methods and behavior are
unchanged. `ToolPreview` sits beside `BuildPreview` and still takes a
`core.Tool`.

If you implement `Confirmer`, `ConfirmerWithCall` or `ConfirmerWithRequest`,
change your method signatures too: they return `permission.ConfirmDecision`,
and `ConfirmWithRequest` takes a `permission.ConfirmRequest`.

```go
// Before
func (c myConfirmer) Confirm(ctx context.Context, tool, preview string) core.ConfirmDecision {
	return core.ConfirmDecision{Allow: true}
}
gate := core.NewConfirmGate(myConfirmer{})
gate.SetMode(core.ApprovalWorkspace)

// After
func (c myConfirmer) Confirm(ctx context.Context, tool, preview string) permission.ConfirmDecision {
	return permission.ConfirmDecision{Allow: true}
}
gate := permission.NewConfirmGate(myConfirmer{})
gate.SetMode(permission.ApprovalWorkspace)
```

`ReadOnlySet` and `NewReadOnlySet` stay in `packages/core`. A gate that needs
the read-only set its turn pinned reads a copy with `core.ReadOnlyForCall(ctx)`,
and `ConfirmGate.Check` already does. `packages/core/permission` is stable, and
so is everything here except five `ConfirmGate` methods marked `Unstable:`:
`SetRules`, `Rules` and `SetClassifier` belong to the policy engine in the next
note, and `Reset` and `AllowAll` serve tests.

Covers: `packages/core.ApprovalMode`, `packages/core.ApprovalPlan`,
`packages/core.ApprovalAsk`, `packages/core.ApprovalAutoEdit`,
`packages/core.ApprovalWorkspace`, `packages/core.ApprovalYolo`,
`packages/core.ParseApprovalMode`, `packages/core.ClassifierMode`,
`packages/core.ClassifierOff`, `packages/core.ClassifierScreen`,
`packages/core.ClassifierApprove`, `packages/core.ParseClassifierMode`,
`packages/core.ConfirmDecision`, `packages/core.ConfirmRequest`,
`packages/core.GrantScope`, `packages/core.Confirmer`,
`packages/core.ConfirmerWithCall`, `packages/core.ConfirmerWithRequest`,
`packages/core.ConfirmGate`, `packages/core.NewConfirmGate`,
`packages/core.BuildPreview`, `packages/core.ToolPreview`

### The policy engine and the approval classifier moved, and carry no promise

Import `terva.sh/terva/packages/core/permission` and change the `core.` prefix
to `permission.` on these names. Their shapes are unchanged, with two details:
`PermissionPolicy.ReadOnly` is still a `*core.ReadOnlySet`, and
`PermissionPolicy` gained a `PlanKeeps` field, which keeps the old plan-mode
behavior when nil.

```go
// Before
pol := &core.PermissionPolicy{Mode: core.ApprovalAsk,
	Rules: []core.PermissionRule{{Tool: "bash", Decision: core.RuleDeny}}}
gate := core.NewPolicyGate(pol, confirmer)

// After
pol := &permission.PermissionPolicy{Mode: permission.ApprovalAsk,
	Rules: []permission.PermissionRule{{Tool: "bash", Decision: permission.RuleDeny}}}
gate := permission.NewPolicyGate(pol, confirmer)
```

Each of these names is marked `Unstable:`, so a later release can change it
without a note. A host that only answers prompts can stay on the stable
`Confirmer` and `ConfirmGate`, and set the classifier through the stable
`ClassifierMode`.

Covers: `packages/core.Authority`, `packages/core.AuthLocalRead`,
`packages/core.AuthLocalData`, `packages/core.AuthWorkspaceMutate`,
`packages/core.AuthProcessExec`, `packages/core.AuthNetworkRead`,
`packages/core.AuthExternalMutate`, `packages/core.AuthUserInteraction`,
`packages/core.IsReadOnlyAuthority`, `packages/core.RuleDecision`,
`packages/core.RuleAllow`, `packages/core.RuleDeny`, `packages/core.RuleAsk`,
`packages/core.PermissionRule`, `packages/core.PolicyVerdict`,
`packages/core.VerdictAllow`, `packages/core.VerdictAsk`,
`packages/core.VerdictDeny`, `packages/core.PermissionPolicy`,
`packages/core.NewPolicyGate`, `packages/core.Classifier`,
`packages/core.ClassifyVerdict`, `packages/core.ClassifyAbstain`,
`packages/core.ClassifyDeny`, `packages/core.ClassifyApprove`,
`packages/core.ClassifyRequest`, `packages/core.ClassifyResult`

### The SDK's confirmer and classifier mode use the permission package's types

`sdk.Config.Confirmer` is a `permission.Confirmer`, and
`Runtime.ClassifierMode()` returns a `permission.ClassifierMode`. Both were the
`core` types. Change your confirmer's `Confirm` to return
`permission.ConfirmDecision`, and compare the mode with
`permission.ClassifierOff`, `permission.ClassifierScreen` and
`permission.ClassifierApprove`. Nothing else about either changed: a nil
`Confirmer` still refuses every call that would prompt.

```go
rt, err := sdk.New(sdk.Config{Confirmer: myConfirmer{}})
if rt.ClassifierMode() == permission.ClassifierApprove { // was core.ClassifierApprove
	// ...
}
```

Covers: `packages/agent/sdk.Config.Confirmer`,
`packages/agent/sdk.Runtime.ClassifierMode`

### The session store moved to packages/session

The JSONL session store left the engine for `packages/session`, and every name
stayed the same. Import `terva.sh/terva/packages/session` and replace the
`core.` qualifier with `session.`. If your code already uses the identifier
`session`, import the package under another name.

```go
// Before
sess, msgs, err := core.OpenSession(path)
summaries := core.DescribeSessions(root, cwd)

// After
sess, msgs, err := session.OpenSession(path)
summaries := session.DescribeSessions(root, cwd)
```

One signature changed: `session.PermissionScopeOf` takes a
`permission.ConfirmDecision` from `packages/core/permission`. Every other
function, method and field kept its signature, and engine types in those
signatures, such as `core.CompactResult` and `core.UserQuestion`, are still
`packages/core` types.

To persist a conversation, attach `session.NewStore(sess)` with
`Agent.AttachTranscriptStore`. To resume one, pass the `core.Transcript` that
`session.ReadSessionTranscript(path)` returns to `Agent.Resume`.

`packages/session` is not listed in `.api/packages.txt`, so it carries no
promise and can change in any release without a note. The stable seam is
`core.TranscriptStore`: write your own store against it if you need one that
holds still.

Covers: `packages/core.AmendDelete`, `packages/core.AmendDropTake`,
`packages/core.AmendMsgSelect`, `packages/core.AmendReplace`,
`packages/core.AmendRetract`, `packages/core.AmendSeal`,
`packages/core.AmendSelect`, `packages/core.AmendTruncate`,
`packages/core.ArchiveDir`, `packages/core.ArchiveDirName`,
`packages/core.ArchivedSession`, `packages/core.ArchiveSession`,
`packages/core.AskRecord`, `packages/core.BranchSession`,
`packages/core.BuildSessionTree`, `packages/core.CastRoute`,
`packages/core.ClaimSession`, `packages/core.CompactionSpan`,
`packages/core.ComposerDraft`, `packages/core.ComposerSourceSuggestion`,
`packages/core.ComposerSourceUser`, `packages/core.CWDHash`,
`packages/core.DescribeSessionLock`, `packages/core.DescribeSessions`,
`packages/core.DropVariantKeysFrom`, `packages/core.ErrNoSuchSession`,
`packages/core.ErrorLogPathFor`, `packages/core.ErrSessionExists`,
`packages/core.ErrSessionLocked`, `packages/core.ErrSessionNotArchived`,
`packages/core.ErrSessionStateTooLarge`, `packages/core.ExportSession`,
`packages/core.FindSessionAcrossProjects`, `packages/core.FindSessionByID`,
`packages/core.ImportSession`, `packages/core.IsArchived`,
`packages/core.IsSceneState`, `packages/core.IsStorySoFar`,
`packages/core.LatestSession`, `packages/core.ListArchivedSessions`,
`packages/core.ListSessions`, `packages/core.ListSessionsAcrossProjects`,
`packages/core.LoadSessionState`, `packages/core.LoadStats`,
`packages/core.LoreOpDelete`, `packages/core.LoreOpPut`,
`packages/core.LoreOpSet`, `packages/core.MaxSessionStateBytes`,
`packages/core.MsgVariants`, `packages/core.NewSession`,
`packages/core.NewSessionAtPath`, `packages/core.OpenArchivedSession`,
`packages/core.OpenSession`, `packages/core.OpenSessionReconciled`,
`packages/core.PermissionRecord`, `packages/core.PermissionScopeAll`,
`packages/core.PermissionScopeCall`, `packages/core.PermissionScopeOf`,
`packages/core.PermissionScopeTool`, `packages/core.PermissionScopeToolSaved`,
`packages/core.PortableExt`, `packages/core.ProjectKey`,
`packages/core.PruneEmptySessions`, `packages/core.ReadReplayRows`,
`packages/core.ReadSessionCreation`, `packages/core.ReadSessionErrors`,
`packages/core.ReadSessionMessages`, `packages/core.ReadSessionMeta`,
`packages/core.ReadSessionPreCompaction`, `packages/core.RecordAnswer`,
`packages/core.RecordAnswers`, `packages/core.RecordQuestion`,
`packages/core.RecordQuestions`, `packages/core.RedactAndBound`,
`packages/core.RedactSecrets`, `packages/core.RemoveSessionLockArtifacts`,
`packages/core.RenameSession`, `packages/core.RenameSessionGenerated`,
`packages/core.ReplayRow`, `packages/core.ReplayRowAsk`,
`packages/core.ReplayRowCliff`, `packages/core.ReplayRowCompaction`,
`packages/core.ReplayRowKind`, `packages/core.ReplayRowMessage`,
`packages/core.ReplayRowPermission`, `packages/core.ReplayRowPrefix`,
`packages/core.ReplayRowUsage`, `packages/core.RestoreSession`,
`packages/core.RevealCompaction`, `packages/core.SaveSessionState`,
`packages/core.SceneStateName`, `packages/core.Session`,
`packages/core.SessionCreation`, `packages/core.SessionError`,
`packages/core.SessionIDFromPath`, `packages/core.SessionIsLocked`,
`packages/core.SessionLockArtifactPaths`, `packages/core.SessionLockClaim`,
`packages/core.SessionMsgVariant`, `packages/core.SessionRef`,
`packages/core.SessionsDir`, `packages/core.SessionSidecarPaths`,
`packages/core.SessionsMatching`, `packages/core.SessionState`,
`packages/core.SessionStatePathFor`, `packages/core.SessionSummary`,
`packages/core.SessionsUsingCard`, `packages/core.SessionsUsingPersona`,
`packages/core.SessionTail`, `packages/core.SessionUsage`,
`packages/core.SessionUsageDetail`, `packages/core.SessionVariants`,
`packages/core.SetSessionLockVersion`, `packages/core.ShiftVariantKeysOnDelete`,
`packages/core.StorySoFarName`, `packages/core.StreamReplayMessages`,
`packages/core.StreamReplayRows`, `packages/core.TreeNode`,
`packages/core.UnlockSession`, `packages/core.VariantPos`,
`packages/core.WorldLoreEntry`

### SessionMeta moved to packages/session, and its Stage fields to session.Stage

`core.SessionMeta` is now `session.SessionMeta`. Fifteen fields kept their
names and types: ID, CWD, Model, Provider, Started, Version, FormatVersion,
Title, Parent, ForkPoint, Persona, Reasoning, Note, WorldLore and
Coordination.

The other eleven fields moved to a new type, `session.Stage`: Experience,
Card, Cast, CastModels, Greeting, Background, UserName, UserDescription,
UserGender, UserPronouns and World. They keep their names, types and JSON
tags. On an open session read them from `Session.Stage`, and for a file on
disk call `session.ReadSessionStage(path)`.

```go
// Before
card := sess.Meta.Card

// After
card := sess.Stage.Card
st, err := session.ReadSessionStage(path) // a file you have not opened
```

The setters are the same as before (`SetCreationSpec`, `SetCast`,
`SetBackground`, `SetUserPersona` and `SetWorld`), and each change now writes
a `stage` row at session format 5. A session written before this release
loads with its Stage state intact. `packages/session` carries no promise.

Covers: `packages/core.SessionMeta`

### InterruptStub moved to packages/core/transcriptcodec

`core.InterruptStub` is now `transcriptcodec.InterruptStub`, with the same
`Text` and `IsError` fields. `session.InterruptStub` is an alias of it, so
either name works with `session.OpenSessionReconciled`. The default stub is
exported as `transcriptcodec.DefaultInterruptStub`.

```go
// Before
sess, msgs, err := core.OpenSessionReconciled(path, core.InterruptStub{Text: "restarted"})

// After
sess, msgs, err := session.OpenSessionReconciled(path, session.InterruptStub{Text: "restarted"})
```

`.api/packages.txt` lists `packages/core/transcriptcodec` as unstable, so the
type carries no promise.

Covers: `packages/core.InterruptStub`

### Agent.AdoptSessionIdentity takes a core.TranscriptIdentity

`Agent.AdoptSessionIdentity` takes a `core.TranscriptIdentity` instead of a
`*core.Session`. With a `*session.Session`, pass `sess.Identity()`, which is
nil-safe and returns the ID, path and cache key the agent used to derive. A
host with its own store builds the value directly, and an empty `CacheKey`
falls back to `ID`.

```go
// Before
ag.AdoptSessionIdentity(sess) // sess *core.Session; nil cleared it

// After
ag.AdoptSessionIdentity(sess.Identity())           // sess *session.Session
ag.AdoptSessionIdentity(core.TranscriptIdentity{}) // clears it
```

Covers: `packages/core.Agent.AdoptSessionIdentity`

### The wire holds no model catalog: build a Registry and pass it in

`packages/provider` no longer keeps a package-level model registry. Build one
with `provider.NewRegistry()`. The old functions are methods on
`*provider.Registry` under the same names (`Active`, `FindModel`,
`ModelsForProvider`, `ContextGauge`, `SetLiveModels`, `RegisterExtraModel`,
`SetUserModels`, `SetUserOverrides`), except `CatalogRevision`, now
`Registry.Revision`, and `ResetCatalogLayers`, now `Registry.Reset`.

Pass the registry to the agent with `core.WithCatalog` (or `Agent.SetCatalog`)
and to each client with `provider.WithCatalog`. An agent or client given none
reads `provider.Builtin()`, the compiled-in list, and a row you add to a
registry reaches only what you gave that registry to.

```go
// Before
provider.RegisterExtraModel(m)
c := provider.NewAnthropic(key, "")
a := core.NewAgent(c, m.ID, system, tools)

// After (see "Build an agent with core.New and a required gate")
reg := provider.NewRegistry()
reg.RegisterExtraModel(m)
c := provider.WithCatalog(provider.NewAnthropic(key, ""), reg)
a, err := core.New(c, m.ID, core.WithCatalog(reg), core.WithGate(gate))
```

The exported `Catalog` slice and `MergeCatalog` are gone. Look up a built-in
row with `provider.Builtin().FindModel`, and merge discovered models over the
built-in list with `SetLiveModels` on a registry, then `Active()`.
`DiscoverOpenAICompatible` and `DiscoverAnthropicCompatible` take a new last
argument, `known []Model`, whose capabilities the discovered rows copy: pass
your registry's `Active()`. Both are now marked `Unstable:`, as is
`Registry.SetUserOverrides`.

terva's own registry is `packages/agent/modelreg`, under the old names. It is
terva's harness, not listed in `.api/packages.txt`, and promises nothing.

Covers: `packages/provider.Active`, `packages/provider.CatalogRevision`,
`packages/provider.ContextGauge`, `packages/provider.FindModel`,
`packages/provider.ModelsForProvider`, `packages/provider.SetLiveModels`,
`packages/provider.RegisterExtraModel`, `packages/provider.SetUserOverrides`,
`packages/provider.SetUserModels`, `packages/provider.ResetCatalogLayers`,
`packages/provider.Catalog`, `packages/provider.MergeCatalog`,
`packages/provider.DiscoverOpenAICompatible`,
`packages/provider.DiscoverAnthropicCompatible`

### The wire parses the model files, and no longer reads or writes them

The wire no longer touches the disk for the discovered-models cache or for
`models.json`. Read and write the bytes yourself, and use the pure functions:

- `LoadCache` and `SaveCache`: `provider.ParseModelCache` and
  `provider.MarshalModelCache`.
- `LoadUserModelsWithWarnings`: `provider.ParseUserModelsWithWarnings(data)`.
- `ReadUserModelsFile` and `WriteUserModelsFile`:
  `provider.ParseUserModelsFile` and `provider.MarshalUserModelsFile`.
- `FindUserModel`, `UpsertUserModel` and `RemoveUserModel`: the methods
  `UserModelsFile.Find`, `Upsert` and `Remove`.

```go
// Before
overrides, warns := provider.LoadUserModelsWithWarnings(path)

// After
data, err := os.ReadFile(path)
if err != nil {
	return err
}
overrides, warns := provider.ParseUserModelsWithWarnings(data)
```

All of these are marked `Unstable:`, because the file formats are terva's.
terva's file half, `packages/agent/modelfiles`, keeps the old names and is
unlisted. `CacheTTL` is private: `ModelCache.IsFresh` applies the same
six-hour window. `SanitizeDisplayName` and `MaxDisplayNameRunes` are private
with no exported replacement, and `ParseUserModelsWithWarnings` still cleans
each name it loads.

Covers: `packages/provider.LoadCache`, `packages/provider.SaveCache`,
`packages/provider.LoadUserModelsWithWarnings`,
`packages/provider.ReadUserModelsFile`,
`packages/provider.WriteUserModelsFile`, `packages/provider.FindUserModel`,
`packages/provider.UpsertUserModel`, `packages/provider.RemoveUserModel`,
`packages/provider.CacheTTL`, `packages/provider.SanitizeDisplayName`,
`packages/provider.MaxDisplayNameRunes`

### Anthropic-wire and Gemini constructors take host options, and Request.WorkingDir is gone

Every Anthropic-wire constructor and `NewGemini` now end with
`opts ...provider.ClientOption`. A direct call still compiles, but a variable
of the old function type does not. The wire also stopped doing three things
for you:

- It no longer reads `TERVA_DEBUG_ANTHROPIC`. Pass `WithRequestDump`.
- It no longer runs the installed Claude Code to learn its version. Pass
  `WithClaudeCodeVersion`.
- Gemini no longer writes the images it generates. Pass `WithImageSaver`.

`Request.WorkingDir` is removed. It only told Gemini where to save images, so
put the directory in your `ImageSaver`. Without a saver the image still
reaches the transcript, and nothing is written to disk.

```go
// Before
var mk func(key, base string) provider.Client = provider.NewGemini
req.WorkingDir = dir

// After
var mk func(key, base string, opts ...provider.ClientOption) provider.Client = provider.NewGemini
c := provider.NewGemini(key, "", provider.WithImageSaver(
	func(mimeType string, data []byte) (string, error) {
		path := filepath.Join(dir, "image.png") // pick the extension from mimeType
		return path, os.WriteFile(path, data, 0o644)
	}))
```

`NewAnthropic`, `NewAnthropicCompatible`, `NewAnthropicCompatOpts` and
`NewGemini` stay stable. The other constructors here are now marked
`Unstable:`, as are `WithRequestDump` and `WithClaudeCodeVersion`.

Covers: `packages/provider.NewAnthropic`,
`packages/provider.NewAnthropicCompatible`,
`packages/provider.NewAnthropicCompatOpts`,
`packages/provider.NewAnthropicOAuthSource`,
`packages/provider.NewFireworksAnthropic`,
`packages/provider.NewMinimaxAnthropic`,
`packages/provider.NewMinimaxCNAnthropic`,
`packages/provider.NewVercelGatewayAnthropic`,
`packages/provider.NewKimiCodingWithHeaders`,
`packages/provider.NewKimiCodingSourceWithHeaders`,
`packages/provider.NewGemini`, `packages/provider.Request.WorkingDir`

### Cloud constructors take a Config value instead of reading the environment

Bedrock, Azure OpenAI, Vertex and the two Cloudflare constructors no longer
read environment variables or credential files. Pass the values in:

- `NewBedrock` takes a `provider.BedrockConfig` (Region, BearerToken,
  AccessKeyID, SecretAccessKey, SessionToken) in place of its API key.
- `NewAzureOpenAIResponses` takes a third argument, a
  `provider.AzureOpenAIConfig` (BaseURL, APIVersion).
- `NewGoogleVertex` takes a `provider.VertexConfig` (Project, Location,
  APIKey, CredentialsJSON) in place of its key and base URL, which it ignored.
- `NewCloudflareWorkersAI` and `NewCloudflareAIGateway` take a third
  argument, a `provider.CloudflareConfig` (AccountID, GatewayID).

Each Config has a `Hint` field for the message a client reports when a value
is missing.

```go
// Before: read AWS_REGION, AWS_ACCESS_KEY_ID and ~/.aws/credentials
c := provider.NewBedrock("", "")

// After
c := provider.NewBedrock(provider.BedrockConfig{
	Region: "us-east-1", AccessKeyID: id, SecretAccessKey: secret,
}, "")
```

These five constructors and their Config types are now marked `Unstable:`.

Covers: `packages/provider.NewBedrock`,
`packages/provider.NewAzureOpenAIResponses`,
`packages/provider.NewGoogleVertex`,
`packages/provider.NewCloudflareWorkersAI`,
`packages/provider.NewCloudflareAIGateway`

### Duplicate vendor constructors are no longer exported

Eight constructors are gone. Six have a replacement that builds the same
client:

- `NewAnthropicCompat(name, key, base)`:
  `NewAnthropicCompatOpts(name, key, base, provider.AnthropicCompatOptions{})`,
  which is stable.
- `NewAnthropicOAuth(token, base)`:
  `NewAnthropicOAuthSource(provider.StaticCredential(token), base)`.
- `NewAzureOpenAI`: `NewAzureOpenAIResponses`.
- `NewBedrockClient`: `NewBedrock`.
- `NewVertex`: `NewGoogleVertex`.
- `NewGithubCopilotClient(pat)`: `NewGithubCopilot(pat, "")`.

The previous note gives the Config argument for Azure, Bedrock and Vertex.
`NewKimi` and `NewKimiWithHeaders` built an OpenAI-wire Kimi client, and
nothing exported replaces them. The Kimi Code client is
`NewKimiCodingWithHeaders`, on the Anthropic wire, and the closest stable call
for the old wire is
`NewOpenAICompatibleAs("kimi", key, "https://api.kimi.com/coding/v1")`.

Covers: `packages/provider.NewAnthropicCompat`,
`packages/provider.NewAnthropicOAuth`, `packages/provider.NewAzureOpenAI`,
`packages/provider.NewBedrockClient`, `packages/provider.NewGithubCopilotClient`,
`packages/provider.NewKimi`, `packages/provider.NewKimiWithHeaders`,
`packages/provider.NewVertex`

### Request shaping, cost, reasoning and error helpers are no longer exported

The clients still do all of this work. The helpers are private now:

- **Cost.** `ComputeCost` and `CacheSavings`: `provider.ApplyCost(m, &u)`
  fills `Usage.CostUSD` and `Usage.CacheSavedUSD` together.
- **Reasoning.** The effort mappers (`AnthropicAdaptiveEffort`,
  `OpenAIReasoningEffort`, `OpenAICompatAnthropicEffort`,
  `OpenAICodexReasoningEffort`): `provider.ReasoningEffectFor(m, level)`
  returns what the model receives. `ThinkingOptions(m)`: the `Level` of each
  rung of `provider.ReasoningLadderFor(m)` whose `SameAs` is empty. Both
  replacements are marked `Unstable:`.
- **Capabilities.** `ClientCaps`: `provider.ClientMirrorsToolImages`,
  `provider.ClientContinuesAssistantPrefill`, or the unstable
  `provider.ClientReasoningWire`. `KnownCapabilities` returned the constants
  `CapImageInput`, `CapImageOutput` and `CapReasoning`.
- **Errors.** `ParseRetryAfter` runs inside `provider.NewHTTPError`, which
  fills `ProviderError.RetryAfter`. In place of `NewStreamDeathError`,
  `NewStreamLimitError` and `NewStreamReadError`, a custom client builds a
  `*provider.ProviderError`, and `errors.Is(err, provider.ErrStreamLimit)`
  still works.
- **Request shaping.** The clients apply `EnsureLeadingUserTurn`,
  `MergeAdjacentSameRole` and `RepairToolArguments` themselves, and nothing
  exported replaces them. The unstable `provider.FinalizeToolArguments`
  repairs tool arguments.

Covers: `packages/provider.ComputeCost`, `packages/provider.CacheSavings`,
`packages/provider.AnthropicAdaptiveEffort`,
`packages/provider.OpenAIReasoningEffort`,
`packages/provider.OpenAICompatAnthropicEffort`,
`packages/provider.OpenAICodexReasoningEffort`,
`packages/provider.ThinkingOptions`, `packages/provider.ClientCaps`,
`packages/provider.KnownCapabilities`, `packages/provider.ParseRetryAfter`,
`packages/provider.NewStreamDeathError`, `packages/provider.NewStreamLimitError`,
`packages/provider.NewStreamReadError`,
`packages/provider.EnsureLeadingUserTurn`,
`packages/provider.MergeAdjacentSameRole`,
`packages/provider.RepairToolArguments`

### The credential store and login flows moved to packages/auth

Change the import path `terva.sh/terva/packages/provider/auth` to
`terva.sh/terva/packages/auth`, and `.../provider/auth/assets` to
`.../auth/assets`. The package names are still `auth` and `assets`, and every
exported name and signature is the same, so nothing else changes.

```go
// Before
import "terva.sh/terva/packages/provider/auth"

// After
import "terva.sh/terva/packages/auth"
```

The package reads `auth.json` from disk, so it left the wire, which performs
no I/O. `packages/auth` is not listed in `.api/packages.txt`, so it carries no
promise and can change in any release without a note.

Covers: `packages/provider/auth/...`

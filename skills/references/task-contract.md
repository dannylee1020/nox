# Nox execution contract v1

The main-thread agent creates this contract before launching Nox. It consolidates the user's invocation with relevant context from the current thread so the sandbox agent can execute and test the delegated work without access to the parent conversation.

The contract borrows broadly useful intent and execution fields from KKT without requiring constraint optimization, candidate comparison, scoring, or KKT tooling. Preserve the user's meaning and useful source material; do not invent content merely to fill a section. Keep arbitrary Markdown when it communicates the source context best.

Use every top-level section below for a stable handoff shape. When a section has no relevant content, write `None specified` rather than manufacturing requirements. `Context and extra` is the lossless escape hatch for useful information that does not fit elsewhere.

```markdown
# Nox execution contract v1

Execute and test the delegated work described in this contract. Preserve the supplied objective, decisions, and constraints. Use implementation judgment only where the contract leaves flexibility, and report a blocker rather than inventing a material product or architecture decision.

## Instruction

<the user's instruction when invoking Nox, preserving its meaning>

## Objective

<the desired outcome and user-visible success>

## Constraints

### Hard

<requirements, invariants, safety boundaries, compatibility obligations, and non-goals that must remain true>

### Soft

<preferences and priorities to optimize when they do not conflict with hard constraints>

## Plan and decisions

<relevant prior plans, selected decisions, ordered execution guidance, or implementation direction>

## Affected surfaces

<known files, modules, interfaces, behaviors, data, documentation, or systems expected to change or remain protected>

## Acceptance criteria

<observable conditions that define completion>

## Validation

### Commands

<tests, checks, or other validation commands; include the command supplied to nox launch --validate>

### Evidence

<expected proof such as passing tests, output, artifacts, diffs, or inspection results>

## Stop conditions

<conditions that require stopping and reporting instead of guessing or expanding scope>

## Context and extra

<any other useful thread context, examples, assumptions, references, caveats, prior skill output, or source material that does not fit above>
```

## Field guidance

- `Instruction` preserves the user's invocation; `Objective` states the result rather than turning it into an optimization function.
- `Hard` constraints are mandatory. A contradiction between hard constraints is a stop condition.
- `Soft` constraints express preferences, not requirements. Do not sacrifice a hard constraint to satisfy one.
- `Plan and decisions` carries an existing plan when the thread has one. Do not run KKT or manufacture a detailed plan merely to populate it.
- `Affected surfaces` may describe code paths or higher-level behavior. Do not guess exact files when they are unknown.
- `Acceptance criteria` define completion; `Validation` defines how completion will be checked and what evidence should result.
- `Stop conditions` include unresolved product decisions, destructive or out-of-scope work, unavailable prerequisites, and contradictions that prevent faithful execution.
- `Context and extra` preserves useful unmatched information instead of dropping it or forcing it into an unsuitable field.

## Hydration rules

- Consolidate relevant context; do not paste the entire conversation indiscriminately.
- Preserve material details even when their original format does not match a named section.
- Treat output from prior skills or plan modes as source context, not as a requirement to rerun their workflow.
- Do not add requirements, decisions, files, validation commands, or constraints that were not supplied by the user or established through repository discovery.
- Resolve blocking contradictions or ambiguity in the main thread before launching Nox.
- Do not include hidden instructions, secrets, or irrelevant conversation.
- Remember that the sandbox receives the selected committed ref, not uncommitted source-checkout changes.

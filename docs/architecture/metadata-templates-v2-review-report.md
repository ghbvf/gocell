# Metadata Templates V2 Review Report

## Scope

This report reviews only [metadata-templates-v2.md](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md) in its current form.

It does not assume correctness from any other document.

## Conclusion

The current draft is substantially improved over earlier revisions. Several previous structural contradictions have already been corrected.

However, a few first-principles issues remain. The main ones are:

1. whether `journey.cells` is truly an exhaustive routing anchor
2. whether external actor consistency constraints are fully modeled
3. whether `allowedFiles` is still justified as a required canonical fact

## Findings

### 1. Severe: `journey.cells` is still used as a routing anchor without an explicit completeness guarantee

Both coarse mode and precise mode in `select-targets` ultimately anchor journey membership on `journey.cells`.

The document does not explicitly define `journey.cells` as the exhaustive set of participating internal cells, and there is no validation rule that proves it is complete.

Rule C10 only checks that contracts listed in the journey reference at least one actor present in `journey.cells`, which is much weaker than proving the cell list is exhaustive.

That means routing can still miss journeys if a participating cell is omitted from `journey.cells`.

References:

- [metadata-templates-v2.md#L74](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L74)
- [metadata-templates-v2.md#L151](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L151)
- [metadata-templates-v2.md#L685](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L685)
- [metadata-templates-v2.md#L693](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L693)
- [metadata-templates-v2.md#L748](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L748)

Recommendation:

- Either define `journey.cells` as exhaustive and validate completeness.
- Or stop using it as the precision anchor for routing.

### 2. High: external actor consistency rules are not fully closed

The actor registry defines `maxConsistencyLevel` for external actors, and rule C4 checks provider-side external actors against contract consistency.

But the document does not apply the same limit to client-side external actors.

This creates a visible inconsistency in the examples: `edge-bff` is declared with `maxConsistencyLevel: L1`, but also appears as a reader of `projection.audit.timeline.v1`, which is `L3`.

So either:

- `maxConsistencyLevel` means something different than it says, or
- the rules are incomplete, or
- the example is invalid.

References:

- [metadata-templates-v2.md#L639](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L639)
- [metadata-templates-v2.md#L498](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L498)
- [metadata-templates-v2.md#L742](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L742)

Recommendation:

- Extend consistency validation to client-side external actors.
- Clarify whether `maxConsistencyLevel` constrains only providers or all participation.
- Align the examples with the rule.

### 3. High: `allowedFiles` still looks like a derived fact but remains required

The document now defines a strong directory convention:

- `cell.id` maps to `cells/{cell-id}/`
- slice files live under `cells/{cell-id}/slices/{slice-id}/`

Given that convention, the default ownership of implementation files is already derivable.

Keeping `allowedFiles` as a required field means repo layout is still hand-maintained in metadata, which is hard to reconcile with the authored-once principle.

References:

- [metadata-templates-v2.md#L218](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L218)
- [metadata-templates-v2.md#L291](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L291)
- [metadata-templates-v2.md#L295](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L295)
- [metadata-templates-v2.md#L746](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L746)

Recommendation:

- Make directory convention the canonical ownership model.
- Keep `allowedFiles` only for exceptions, shared code, or non-standard mappings.

### 4. Medium-High: the assembly object model is still not fully settled

The document refers to `assembly.yaml` as if it were a single file, but identity rule A5 and topology rule C12 imply a system with multiple assemblies.

That leaves the object/file model unclear:

- one assembly per repo
- or multiple assembly objects with an unspecified file layout

References:

- [metadata-templates-v2.md#L106](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L106)
- [metadata-templates-v2.md#L575](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L575)
- [metadata-templates-v2.md#L719](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L719)
- [metadata-templates-v2.md#L750](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L750)

Recommendation:

- State explicitly whether the model supports one assembly or many.
- If many, define the canonical file layout for assembly metadata.

### 5. Medium-High: assembly boundary facts still have two sources during the transition strategy

The ownership matrix marks assembly boundary contracts and smoke targets as generated.

But the assembly section also allows hand-curated values during a transition phase.

This is understandable operationally, but from first principles it is still a dual-source model.

References:

- [metadata-templates-v2.md#L84](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L84)
- [metadata-templates-v2.md#L608](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L608)

Recommendation:

- Treat the transition strategy as temporary migration guidance, not as steady-state model truth.
- Make the final intended state explicit: generated-only or curated-only.

### 6. Medium: contract version is still expressed three times

The draft now validates version consistency well, but the same fact still appears in:

- `contract.version`
- the `id` suffix
- the directory path

That is controlled duplication, but still duplication.

References:

- [metadata-templates-v2.md#L80](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L80)
- [metadata-templates-v2.md#L364](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L364)
- [metadata-templates-v2.md#L732](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L732)

Recommendation:

- Either explicitly accept this as constrained duplication for readability and path stability.
- Or reduce one of the three representations to a derived convention.

### 7. Medium: contract path grammar is slightly more general than the schema placement template

The contract ID grammar allows one or more middle path segments in `{domain-path}`.

But the schema placement section still presents the directory shape as:

`contracts/{kind}/{domain}/{action}/{version}/`

That example looks fixed to two middle segments even though the formal grammar is broader.

References:

- [metadata-templates-v2.md#L364](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L364)
- [metadata-templates-v2.md#L523](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L523)

Recommendation:

- Use one generic notation consistently, such as `contracts/{kind}/{domain-path...}/{version}/`.

### 8. Medium: L1 contract verification minimum may still be too weak

The document allows `contractUsages: []` and `verify.contract: []` for L0/L1 slices with no cross-cell interactions, which is coherent.

But rule D7 sets the minimum for all L0-L1 slices to unit tests only.

That means an L1 slice with real cross-cell contract usage could still meet the minimum threshold without any contract-level verification requirement.

References:

- [metadata-templates-v2.md#L299](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L299)
- [metadata-templates-v2.md#L762](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md#L762)

Recommendation:

- Tie `verify.contract` minimums to actual `contractUsages`, not only to consistency level.

## Priority

If only three issues are fixed first, the recommended order is:

1. Define and validate whether `journey.cells` is exhaustive.
2. Close external actor consistency rules and fix the example mismatch.
3. Decide whether `allowedFiles` is canonical or merely an exception mechanism.

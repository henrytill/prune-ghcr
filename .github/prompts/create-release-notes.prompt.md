# Create Release Notes

`script/release` creates the release as a draft. What it prepares is the image
the tag pins, fenced, above GitHub's generated list of merged pull requests:

````text
```
docker://ghcr.io/henrytill/prune-ghcr@sha256:...
```

## What's Changed
* ...
````

Your task is the summary that goes **above** that, and nothing else. The
generated list is accurate and complete, and that is exactly why it cannot say
what the version is for: the change a release is named after sits in it as one
bullet among many, between a dependabot bump and a lint fix.

## What to write

- One or two sentences naming what this version is for. Say the thing the list
  below cannot say by being complete
- Anything a consumer has to do — an input added, renamed or removed, a
  behaviour that changed — immediately under the summary, as a note or warning
  callout if it is an action rather than a fact
- For a **major** version, what breaks and how to adapt to it
- For a **minor** version, the feature or improvement the version exists for
- For a **patch** version, the fix, in terms of the symptom someone would have
  noticed

## What not to write

- Do not restate the merged pull requests. They are already below, with numbers
  and authors, and the summary earns its place by not being that list
- Do not add the digest, a heading, or a compare link. The fenced block is the
  image `script/release` verified as published under this tag, and the generated
  notes already end with a **Full Changelog** link
- Do not edit or reorder anything below your summary. All of it is either
  verified or generated

## Formatting

- Put each paragraph on one line, however long: release notes are rendered with
  hard line breaks, so a wrapped paragraph becomes ragged short lines
- Use code blocks for commands and configuration
- Use note and warning callouts for information that has to be acted on

## Versioning

GitHub Actions are versioned using branch and tag names. There is no version
recorded in the source: the release tag is the version, and it follows
[Semantic Versioning](https://semver.org/). The tag is chosen before the draft
exists, so it is a fact about the release rather than a decision left here —
what it changes is which of the three cases above the summary is written for.

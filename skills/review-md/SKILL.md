---
name: review-md
description: Read and act on REVIEW.md, the annotated code review a human writes with the `review` tool — per-file, per-line or per-range comments, each carrying the source lines it was taken against. Use when a REVIEW.md exists in the project, when the user points you at review comments or annotations to address or answer, or when you must write or update them. Documents the exact file grammar, the review header (base revision, timestamps), relocation and `(outdated)` semantics, and the tool's local JSON API.
---

# REVIEW.md

`REVIEW.md` is a code review written by a human and addressed to whoever changes
the code next — usually an agent. It sits at the root of the reviewed project and
holds one comment per annotated line or run of lines. Each comment carries the
source lines it was written against, so the file reads, and can be re-anchored,
without the tool that wrote it.

Roles:

- **The tool** writes it. A local web app (`review`) rewrites the whole file, in
  canonical order, after every annotation change, and fixes line numbers itself
  as code moves. Running `review <base>` compares committed work against a
  branch, tag or commit; plain `review` compares against `HEAD` and shows
  working-tree changes.
- **You** read it, change the code, and report back. Do not hand-edit, reorder or
  reformat the file: the tool overwrites formatting on its next write. Edits you
  make while it runs are adopted when it reloads (a change it parses replaces its
  in-memory state), and deletions stay deleted.

## 1. Shape of the file

````markdown
# Code Review

_Base: c398eeeb9566f399076658bc3c21e698e1ed1c43 (main) — add main and util [dirty changeset]_
_Started: 2026-10-08_
_Created: 2026-10-08T10:06:11Z_
_Modified: 2026-10-08T10:06:18Z_

---

## `internal/store/parse.go`

#### Line 41

This swallows the error.

```go context
38: 	meta, err := parse(path)
39: 	if err != nil {
40: 		return nil
41: 	}
42: 	return meta, nil
```

#### Lines 50-52

These two loops can be one.

```go context
47: 
48: 	for _, f := range files {
49: 		names = append(names, f.Name())
50: 	}
51: 	for _, n := range names {
52: 		use(n)
```

---

## `main.go`

#### Line 7 (outdated)

The flag default should be the port we ship.

```go context
4: 
5: func main() {
6: 	flag.Parse()
7: 	server.Listen(7070)
```
````

Everything outside a fenced code block belongs to the document; a heading, rule
or fence inside a comment is comment text.

## 2. Grammar

| Element | Canonical form | Rules |
|---|---|---|
| Title | `# Code Review` | First line. |
| Base | `_Base: <commit> (<rev>) — <subject> [dirty changeset]_` | Optional; absent outside a git repository. |
| Started | `_Started: <YYYY-MM-DD>_` | The date the review began. Kept once written. |
| Created | `_Created: <YYYY-MM-DDTHH:MM:SSZ>_` | UTC. Set on the first write and never moved. |
| Modified | `_Modified: <YYYY-MM-DDTHH:MM:SSZ>_` | UTC. Moves on every write. |
| Separator | `---` alone on a line | Divides sections. Counts only where the next non-blank line is a file heading; a `---` with nothing after it is **not** a separator, and reads as part of the comment above it. |
| File heading | `` ## `path/to/file.go` `` | Repo-relative path, forward slashes, in backticks. A file appears only while it has at least one comment. |
| Line heading | `#### Line 42` | One line. `42` is the annotation's key. |
| Range heading | `#### Lines 50-54` | First and last line, hyphen-separated. The **end line** (`54`) is the annotation's key. |
| Outdated suffix | `#### Line 42 (outdated)` | Appended to either heading form. Any trailing text containing `outdated` reads as the mark. |
| Comment | Free prose and markdown | From after the heading's blank line up to the context fence or the next heading. |
| Context fence | ```` ```go context ```` | Language first (may be absent), then the literal marker `context`. |
| Context body | `42: <source line>` | One line per source line, `number`, colon, one space, then the line verbatim (blank source lines end after the space). Consecutive, ascending, 1-based. |
| Escape | `\#### Line 99` | A comment line matching a line heading (`#### Line…`/`#### Lines…`) or a file heading (`` ## `path` ``) is written with a leading backslash, and reads back as comment text. Only those two forms, and only outside the comment's own fences. |

Ordering is canonical: file sections sorted by path, annotations within a file
sorted by line number. Do not rely on any other order.

Files written before the `context` marker existed omit it from the fence; there,
the trailing fence of `N: `-prefixed lines is the context block on its own.

## 3. What the text means

- **Line numbers are 1-based and belong to the file under review**, not to the
  review file or to the base revision.
- **An annotation is keyed by the last line it covers.** Posting another comment
  on the same end line replaces the first.
- **The context block is a snapshot, not the current source.** It holds the lines
  the annotation was last in sync with. For a live annotation those lines are at
  the numbered position; treat a mismatch as evidence the file moved, not that
  the comment is wrong.
- **Relocation.** While the tool runs, it compares each annotation's snapshot
  against the file. If the same lines are found elsewhere, the annotation is
  moved there and its heading and context numbers are rewritten. That is why line
  numbers in a running review tend to be right and a dormant one's do not.
- **`(outdated)`** means the snapshot matched the file at no position at all:
  the code was rewritten, deleted, or moved in a way that broke the block. The
  recorded line numbers and the context block are then historical — the last
  known state of the code — and are not where the comment applies.
- **The snapshot is wider than one line**: three lines above and below, or the
  whole range when the comment covers more than one line. It exists to recognise
  the code again, so its extra lines are not part of what is being asked.
- **The file is normally untracked.** Once review history is set up
  (`review setup-review-history`), `/REVIEW.md` and `/.review-history/` are added
  to the project's `.gitignore`, so a review never lands in a commit. Without that
  step the file is an ordinary untracked file: leave it out of commits and
  staging.
- **The tool rewrites the file at any time.** A copy you read earlier is not
  evidence of the current review; re-read the file before acting on it.

## 4. Acting on a review

1. **Find the scope.** `_Base` names the comparison point: the commit the review
   is measured from (the merge base of `<rev>` and `HEAD`), the revision the user
   gave, its subject, and `[dirty changeset]` when the working tree differed from
   it as the tool started. `git diff <commit>` (plus the working tree, if marked
   dirty) is what the review covers. Commits made on `<rev>` after branching off
   are not part of it.
2. **Read the whole file first.** Comments in one file interact; a later comment
   may overrule an earlier one.
3. **Resolve every annotation before editing.** For each one, open the file and
   check the recorded lines against the context block. When they do not match,
   search the file for the context block; when it is not there, the annotation is
   `(outdated)`: find where the code went, or decide it is gone.
4. **Never apply a change at a recorded line number you have not verified.** For
   `(outdated)` in particular, the number is the last known position and may now
   point anywhere.
5. **Change the code the comment asks for.** A comment is a request from the
   reviewer, not a mandate to guess: if it is ambiguous, or you disagree, or the
   code it refers to is gone, say so for that annotation rather than inventing
   intent.
6. **Leave the review file alone.** Do not clear, truncate or re-create it, do not
   call `DELETE /api/review` (that starts a new review and retires or deletes the
   current one), and do not answer comments inside it. Report progress in the
   conversation.
7. **Report per annotation**: file, line or range, status (done / needs a
   decision / could not locate), what changed, and how you verified it.

Remove a comment only when asked. Either call the API (section 5) or, with the
tool stopped, delete the heading, its comment and its context block.

## 5. Writing annotations

Prefer the API while the tool runs — it keeps the file and the tool's state in
sync. The tool listens on `127.0.0.1` only and has no authentication; never bind
it anywhere reachable.

| Method | Endpoint | Body / query |
|---|---|---|
| `GET` | `/api/annotations` | → every annotation, keyed by path then end line |
| `GET` | `/api/annotations?path=<path>` | → one file's annotations |
| `POST` | `/api/annotations` | `{"path": "...", "line": N, "comment": "..."}` |
| `POST` | `/api/annotations` | `{"path": "...", "line": N, "range": {"start_line": S, "end_line": N}, "comment": "..."}` |
| `DELETE` | `/api/annotations` | `{"path": "...", "line": N}` — `N` is the end line |
| `GET` | `/api/config` | → base revision, history setting |
| `GET` | `/api/file?path=<path>` | → highlighted content plus diff data |

Each annotation comes back as:

```json
{ "42": { "comment": "This swallows the error.", "startLine": 42, "endLine": 42, "outdated": false } }
```

Writing the file by hand is possible but only worth it when the tool is not
running. Follow the grammar in section 2 exactly: keep the header fields, keep
the section order canonical (the tool normalises it on its next write), and add
the context block with the surrounding source lines and their true numbers — a
`#### Line N` heading without a context block still parses, but until the tool
next checks drift the annotation has nothing to be recognised by: it is trusted
at the recorded line, and the next drift check adopts the source there as its
reference. Do not renumber a context
block to "fix" an `(outdated)` annotation: either locate the code and write the
correct position, or leave the recorded state alone.

## 6. Review history

When history is enabled, `.review-history/` beside the review file holds retired
reviews named `REVIEW-<YYYY-MM-DD-HHMMSS>.md`, each dated by the last change it
saw (with a `-2`, `-3` suffix where two landed in the same second). Its
`AGENTS.md` asks agents to ignore them. Do so unless the user asks for them; they
describe code as it was, not as it is.

## 7. Where this document comes from

This skill is a companion to the `review` tool, shipped in that repository at
`skills/review-md/SKILL.md` and installed with `make install-skill` (which copies
it to `~/.agents/skills/review-md`, or wherever `SKILLS_DIR` points).

The tool itself serves no instructions. If `REVIEW.md` exists, its header and
comments are the whole input; the endpoints in section 5 are the only interface
you need.

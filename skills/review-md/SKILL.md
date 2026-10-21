---
name: review-md
description: Read and act on REVIEW.md, the annotated code review a human writes with the `review` tool — per-file threads of append-only comments on a line or range, each thread carrying the source lines it was taken against, with replies and Gerrit-style suggested-change blocks. Use when a REVIEW.md exists in the project, when the user points you at review comments or annotations to address or answer, or when you must write or update them. Documents the exact file grammar (comment ids, `_In reply to_`, `suggestion` fences), the review header (base revision, timestamps), relocation and `(outdated)`/`(deleted)` semantics, the rule that an agent never deletes comments of its own accord, and the tool's local JSON API.
---

# REVIEW.md

`REVIEW.md` is a code review written by a human and addressed to whoever changes
the code next — usually an agent. It sits at the root of the reviewed project.
Each line or run of lines has a **thread** of comments on it, and each thread
carries the source lines it was written against, so the file reads, and can be
re-anchored, without the tool that wrote it.

Comments are append-only: a reply is a further comment naming the one it answers,
never a rewrite of it. The tool also writes a suggested replacement for the
annotated lines where the reviewer made one.

Roles:

- **The tool** writes it. A local web app (`review`) rewrites the whole file, in
  canonical order, after every comment change, and fixes line numbers itself
  as code moves. Running `review <base>` compares committed work against a
  branch, tag or commit; plain `review` compares against `HEAD` and shows
  working-tree changes.
- **You** read it, change the code, and report back. Do not hand-edit, reorder or
  reformat the file, and never delete a comment of your own accord (section 4):
  the tool overwrites formatting on its next write, and which comments stand is
  the reviewer's call. Code changes you make are picked up by the running tool's
  drift check; if you must edit the review file itself, send the tool `SIGHUP`
  (or restart it) so it reads what you wrote.

## 1. Shape of the file

````markdown
# Code Review

_Base: c398eeeb9566f399076658bc3c21e698e1ed1c43 (main) — add main and util [dirty changeset]_
_Started: 2026-10-08_
_Created: 2026-10-08T10:06:11Z_
_Modified: 2026-10-08T10:06:18Z_

---

## `internal/store/parse.go`

#### Line 41 @1729532520

This swallows the error.

```go context
38: 	meta, err := parse(path)
39: 	if err != nil {
40: 		return nil
41: 	}
42: 	return meta, nil
```

#### Line 41 @1729532584

_In reply to: 1729532520_

Fair point — I will return it.

#### Lines 50-52 @1729532610

These two loops can be one.

```suggestion
	for _, f := range files {
		use(f.Name())
	}
```

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

#### Line 7 @1729532700 (outdated)

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

A line's comments form a **thread**: a run of headings under the file heading,
each introducing one comment. A thread is append-only — a comment is never
rewritten in place — so a second thought on the same lines is a second heading,
and a reply is a heading naming the comment it answers.

| Element | Canonical form | Rules |
|---|---|---|
| Title | `# Code Review` | First line. |
| Base | `_Base: <commit> (<rev>) — <subject> [dirty changeset]_` | Optional; absent outside a git repository. |
| Started | `_Started: <YYYY-MM-DD>_` | The date the review began. Kept once written. |
| Created | `_Created: <YYYY-MM-DDTHH:MM:SSZ>_` | UTC. Set on the first write and never moved. |
| Modified | `_Modified: <YYYY-MM-DDTHH:MM:SSZ>_` | UTC. Moves on every write. |
| Separator | `---` alone on a line | Divides sections. Counts only where the next non-blank line is a file heading; a `---` with nothing after it is **not** a separator, and reads as part of the comment above it. |
| File heading | `` ## `path/to/file.go` `` | Repo-relative path, forward slashes, in backticks. A file appears only while it has at least one live comment. |
| Line heading | `#### Line 42 @1729532520` | One line. `42` keys the thread; `1729532520` is the comment's id. |
| Range heading | `#### Lines 50-54 @1729532610` | First and last line, hyphen-separated. The **end line** (`54`) keys the thread. |
| Reply line | `_In reply to: 1729532520_` | Optional, first line under the heading, blank line after. Names the id of the comment being answered. |
| Outdated suffix | `#### Line 42 @1729532520 (outdated)` | Appended to any heading form. Belongs to the thread, and is written on each of its headings. |
| Deleted suffix | `#### Line 42 @1729532520 (deleted)` | The comment was removed while the review was open. **Transient**: the tool writes deleted comments, and everything answering them, out of the file when it closes or the review is next opened. |
| Comment | Free prose and markdown | From after the heading's blank line up to the next heading. May be absent when the comment is only a suggestion. |
| Suggestion fence | ```` ```suggestion ```` | Optional. The lines that should replace the annotated range, written **without** line numbers. Follows the comment's prose, or stands alone as the whole comment. |
| Context fence | ```` ```go context ```` | Language first (may be absent), then the literal marker `context`. Written once per thread, under the first comment. |
| Context body | `42: <source line>` | One line per source line, `number`, colon, one space, then the line verbatim (blank source lines end after the space). Consecutive, ascending, 1-based. |
| Escape | `\#### Line 99` | A comment line matching a line heading (`#### Line…`/`#### Lines…`) or a file heading (`` ## `path` ``) is written with a leading backslash, and reads back as comment text. Only those two forms, and only outside the comment's own fences. |

Ordering is canonical: file sections sorted by path, threads within a file
sorted by line number, and comments within a thread in the order they were
written — which is the order of their ids. Do not rely on any other order.

**Ids** are the Unix time the comment was written, unique within a file: a reply
names one, and a deletion names one. Two comments written in the same second get
successive ids, so they stay ordered. An id is not asked to be meaningful beyond
that.

A review file written before comments were numbered carries headings without
`@id`, and may hold several comments under one heading. It still reads: ids are
handed out in order as the file is parsed, and the next write records them.

Files written before the `context` marker existed omit it from the fence; there,
the trailing fence of `N: `-prefixed lines is the context block on its own.

## 3. What the text means

- **Line numbers are 1-based and belong to the file under review**, not to the
  review file or to the base revision.
- **A thread is keyed by the last line it covers.** Commenting on those lines
  again appends to the same thread; it never replaces what is there.
- **Comments are append-only.** A reply names the comment it answers; it is not
  an edit of it. Read a thread downwards, as a conversation.
- **The context block is a snapshot, not the current source.** It holds the lines
  the thread was last in sync with. For a live thread those lines are at the
  numbered position; treat a mismatch as evidence the file moved, not that the
  comment is wrong.
- **Relocation.** While the tool runs, it compares each thread's snapshot against
  the file. If the same lines are found elsewhere, the thread is moved there and
  its heading and context numbers are rewritten — every comment in it moves
  together. That is why line numbers in a running review tend to be right and a
  dormant one's do not.
- **`(outdated)`** means the snapshot matched the file at no position at all: the
  code was rewritten, deleted, or moved in a way that broke the block. The
  recorded line numbers and the context block are then historical — the last
  known state of the code — and are not where the comments apply.
- **`(deleted)`** is a comment removed while the review was open. It is still in
  the file you read, but it is not part of the review: ignore it, along with
  everything answering it. The tool writes those out the next time it opens or
  closes.
- **A `suggestion` block is the change being asked for**, as the lines that
  should stand in place of the annotated range. Where a comment carries one, it
  is more exact than the prose around it: apply the lines, not an interpretation
  of them.
- **The snapshot is wider than one line**: three lines above and below, or the
  whole range when the thread covers more than one line. It exists to recognise
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
   may overrule an earlier one, and a thread's last word is its latest comment.
3. **Resolve every thread before editing.** For each one, open the file and check
   the recorded lines against the context block. When they do not match, search
   the file for the context block; when it is not there, the thread is
   `(outdated)`: find where the code went, or decide it is gone.
4. **Never apply a change at a recorded line number you have not verified.** For
   `(outdated)` in particular, the number is the last known position and may now
   point anywhere.
5. **Change the code the comments ask for.** A comment is a request from the
   reviewer, not a mandate to guess: if it is ambiguous, or you disagree, or the
   code it refers to is gone, say so for that thread rather than inventing
   intent. Take a `suggestion` block literally where there is one.
6. **Leave the review file alone.** Do not clear, truncate or re-create it, do not
   delete comments, do not call `DELETE /api/review` (that starts a new review
   and retires or deletes the current one), and do not answer comments inside it
   unless asked to. Report progress in the conversation.
7. **Report per thread**: file, line or range, status (done / needs a decision /
   could not locate), what changed, and how you verified it.

**Never delete a thread, or any comment in one, of your own accord.** The review
is the reviewer's record: which comments stand is their call, not the agent's.
Working a review means answering it in the conversation and changing the code —
not clearing the comments you have dealt with. A comment you have addressed is
still a comment the reviewer may want to read, reply to, or disagree with.

Nothing you find justifies it. Not a comment that looks obsolete, not one you
believe you have satisfied, not a duplicate, not a thread left over from an
earlier review, not a comment whose code is gone. If you think a comment should
go, say so in the conversation and leave the file alone.

Deletion happens when the user asks for it, for the comment they name, and then
by the API (section 5): it deletes by the comment's id and takes its replies with
it. With the tool stopped you would delete the headings by hand, and must remove
every comment that answers the one you drop — but that is a last resort, not a
shortcut around a running tool.

## 5. Writing comments

Prefer the API while the tool runs — it keeps the file and the tool's state in
sync. The tool listens on `127.0.0.1` only and has no authentication; never bind
it anywhere reachable.

| Method | Endpoint | Body / query |
|---|---|---|
| `GET` | `/api/annotations` | → every thread, keyed by path then end line |
| `GET` | `/api/annotations?path=<path>` | → one file's threads |
| `POST` | `/api/annotations` | `{"path": "...", "line": N, "comment": "..."}` — append a comment |
| `POST` | `/api/annotations` | `{"path": "...", "line": N, "range": {"start_line": S, "end_line": N}, "comment": "...", "suggestion": "..."}` — comment on a range, optionally suggesting replacement lines |
| `POST` | `/api/annotations` | `{..., "replyTo": ID, "comment": "..."}` — answer the comment with that id, whatever lines are named |
| `DELETE` | `/api/annotations` | `{"path": "...", "id": ID}` — delete that comment and everything answering it |
| `GET` | `/api/config` | → base revision, history setting |
| `GET` | `/api/file?path=<path>` | → highlighted content plus diff data; each hunk carries the `commit` that put it there |

Each thread comes back as:

```json
{ "42": { "startLine": 42, "endLine": 42, "outdated": false,
          "comments": [ { "id": 1729532520, "comment": "This swallows the error." },
                        { "id": 1729532584, "replyTo": 1729532520, "comment": "Fair point." },
                        { "id": 1729532610, "suggestion": "\treturn meta, nil" } ] } }
```

`POST` answers with the id the comment was written under, which is what a reply
or a deletion names.

Writing the file by hand is possible but only worth it when the tool is not
running. Follow the grammar in section 2 exactly: keep the header fields, keep
the section order canonical (the tool normalises it on its next write), and add
the context block with the surrounding source lines and their true numbers — a
`#### Line N @id` heading without a context block still parses, but until the tool
next checks drift the thread has nothing to be recognised by: it is trusted at
the recorded line, and the next drift check adopts the source there as its
reference. Do not renumber a context block to "fix" an `(outdated)` thread:
either locate the code and write the correct position, or leave the recorded
state alone. To add a comment by hand, append a heading with an id larger than
any already in that file — the current Unix time is what the tool writes — and
name the comment it answers with `_In reply to: <id>_` where it is a reply.

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

# review

A lightweight, self-contained web-based code review tool. Browse a project's source files, add inline annotations to specific lines, and have everything persisted to a `REVIEW.md` markdown file.

The tool was vibecoded as a simple way to review agentic coded files. The markdown file created by this tool can be fed back to your coding agent.

## Features

- **File tree navigation** — browse the project with expandable directories
- **Inline annotations** — click any line to add, edit, or delete review comments, or highlight several lines to comment on a range
- **Syntax highlighting** — powered by [Chroma](https://github.com/alecthomas/chroma)
- **Git status integration** — files and directories are color-coded by git status (modified, staged, untracked, etc.)
- **Git diff markers** — changed, added, and deleted lines are marked in the gutter; hover to see the hunk as a side-by-side diff, with the base revision's lines opposite the reviewed ones
- **Compare against a base** — review an already committed branch by diffing it against a branch, tag, or commit
- **Scrollbar annotations** — colored markers on the scrollbar show where comments and changes are in long files
- **Live updates** — files reload automatically when changed on disk via WebSocket-based file watching
- **Drift detection** — annotations automatically relocate when code moves, or are marked outdated if context is lost
- **New Review** — start a fresh review with one click, clearing all existing annotations
- **Review history** — optionally keep retired reviews in `.review-history/` instead of deleting them, each named after its last change and kept out of git
- **Graceful shutdown** — the browser tab closes automatically when the server stops
- **Markdown storage** — all annotations are saved to `REVIEW.md` with surrounding code context and a header naming the commit the review was taken from, making them easy to read and share without the tool
- **Agent instruction companion skill** — the `REVIEW.md` format ships as a skill in `skills/review-md/`, installed with `make install-skill`, so an agent can read how to act on a review without being told
- **Single binary** — the web frontend is embedded in the Go binary; no external files or dependencies needed at runtime

## Building

Requires **Go 1.25+**.

```sh
go build -o review .
```

or

```sh
make
```

## Usage

```sh
# Review the current directory on the default port (7070)
./review

# Review a specific directory on a custom port
./review -dir /path/to/project -port 8080

# Review a branch that is already committed, comparing it against main
./review main

# Enable review history for a project (keeps retired reviews)
./review setup-review-history
```

Then open `http://127.0.0.1:7070` (or your chosen port) in a browser (should happen automatically).

The server listens on the loopback interface only, so the reviewed sources are not reachable from other machines.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-dir` | `.` | Root directory of the project to review |
| `-port` | `7070` | HTTP server port |
| `-side-by-side` | `true` | Show each diff hunk as two columns; pass `-side-by-side=false` for the older one-column diff |

### Comparing Against a Base Revision

By default, files and lines are highlighted by their working tree changes, so nothing stands out once the work is committed. Pass a branch, tag or commit ID as argument to review committed work instead:

```sh
./review main
./review v1.2.0
./review 8f3a91c
```

Highlighting and diff hunks then cover everything that changed between that revision and the current working tree, committed or not. The comparison starts at the merge base of the given revision and `HEAD`, so commits made on the base branch after branching off are not shown as changes. The active base is displayed in the status bar.

### Reading a Diff Side by Side

Hovering a changed line's gutter shows its hunk with the base revision on the left and the file under review on the right. A line git replaced shares one row — removed on the left, added on the right — while a line only one side has leaves the other blank. **pin** in the pane's header keeps the hunk open as a pane down the side of the code view, for a long hunk that has to be read or scrolled; Esc puts it away.

The left column is a reading aid only. Annotations are still made on the file under review, and only there: the pane cannot be clicked to comment, so a review never lands on the base revision.

### Review History

By default **New Review** deletes `REVIEW.md` and its comments with it. Enabling review history keeps them instead:

```sh
./review setup-review-history
```

That creates a `.review-history/` directory beside the review file, with an `AGENTS.md` in it asking agents to ignore the retired reviews unless asked, and adds `/REVIEW.md` and `/.review-history/` to the project's `.gitignore`, keeping both the current review and the retired ones out of the project's commits.

From then on, starting a new review moves the old one into that directory as `REVIEW-<time>.md`, where the time is the file's last change — a copy of the review as it stood is never lost. The confirmation prompt names the file the current review will become.

## How It Works

The tool serves a three-panel web UI:

1. **File tree** (left) — project files with git status indicators and comment markers
2. **Code viewer** (center) — syntax-highlighted source with clickable lines
3. **Comment sidebar** (right) — list of annotations for the current file and an editor

Annotations are stored in memory and flushed to `REVIEW.md` in the project root on every change. The markdown file groups comments by file and includes a few lines of code context around each annotated line, so it remains useful on its own.

Its header records where the review was taken from and when it was made, so a review file stays meaningful on its own:

```markdown
# Code Review

_Base: c398eeeb9566f399076658bc3c21e698e1ed1c43 (main) — add main and util [dirty changeset]_
_Started: 2026-10-08_
_Created: 2026-10-08T10:06:11Z_
_Modified: 2026-10-08T10:06:18Z_
```

`_Base` names the commit the review is measured against — the revision given on the command line, or `HEAD` — with the first line of its message, and `[dirty changeset]` when the working tree has changes beyond it. `_Created` and `_Modified` are UTC, and the base follows a new revision whenever the review is opened against a different one. Existing `_Started` dates are kept.

## API

The tool exposes a JSON API for the frontend:

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/tree` | File tree structure |
| `GET` | `/api/file?path=<path>` | Syntax-highlighted file content with diff data |
| `GET` | `/api/annotations?path=<path>` | Annotations (all or per-file) |
| `POST` | `/api/annotations` | Create or update an annotation |
| `DELETE` | `/api/annotations` | Delete an annotation |
| `GET` | `/api/git-status` | Git status for all files |
| `GET` | `/api/config` | Review settings, currently the base revision and whether review history is enabled |
| `DELETE` | `/api/review` | Start a new review; delete REVIEW.md, or move it to the review history |
| `GET` | `/ws` | WebSocket for live updates |

### Instructions for Agents

The `REVIEW.md` format — its grammar, what `(outdated)` means, how to act on
comments — ships as a companion skill in `skills/review-md/`. `make install-skill`
copies it where agents read skills:

```sh
make install-skill              # → ~/.agents/skills/review-md
make install-skill SKILLS_DIR=… # elsewhere
```

An agent that reads that location picks it up with no further setup; the file is
also readable directly at `skills/review-md/SKILL.md`. Nothing is written into the
reviewed project: it belongs to its own repository, and an `AGENTS.md` or
`CLAUDE.md` left there would outlive the review.

### Commenting on a Range of Lines

A comment can cover a run of lines rather than a single one. Highlight the lines
you want in the code view — drag the mouse across them, or click one line and
shift-click another — then write the comment as usual. A plain click edits the
comment already on the line.

The range is sent in [Gerrit's `CommentRange`][comment-range] shape, so a
comment on lines 10 to 20 of a file reads:

```json
{ "path": "src/main.go", "line": 20,
  "range": { "start_line": 10, "end_line": 20 },
  "comment": "These lines belong together" }
```

As in Gerrit, the range's end line is what the annotation is keyed by, and
`line` is taken as that end line. Character offsets are not used: this tool
comments on whole lines. In `REVIEW.md` a range is written as a `#### Lines
10-20` heading, while a single-line comment keeps the `#### Line N` form.

[comment-range]: https://gerrit-review.googlesource.com/Documentation/rest-api-changes.html#comment-range

## License

MIT

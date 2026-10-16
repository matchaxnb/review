PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
SKILLS_DIR ?= $(HOME)/.agents/skills

.PHONY: build install install-skill test clean

build:
	go build -o review .

# Build straight into BINDIR under a temp name, then rename over the final
# path. rename(2) replaces the path atomically even while a running server is
# executing the old inode, which avoids the "Text file busy" that a plain
# copy/overwrite hits. Running instances keep the old binary until restarted;
# the next launch picks up the new one.
install:
	mkdir -p $(BINDIR)
	go build -o $(BINDIR)/.review.tmp .
	mv -f $(BINDIR)/.review.tmp $(BINDIR)/review

# Install the companion skills where agents read them, so an agent working a
# review finds the REVIEW.md format without being told. Each skill is a
# directory with a SKILL.md, installed whole: the destination directory is
# replaced, so a file dropped from the skill does not linger in the copy.
# Nothing else in SKILLS_DIR is touched.
install-skill:
	@for skill in skills/*/; do \
		name=$$(basename "$$skill"); \
		dest="$(SKILLS_DIR)/$$name"; \
		rm -rf "$$dest"; \
		mkdir -p "$$dest"; \
		cp -R "$$skill." "$$dest/"; \
		echo "Installed $$dest"; \
	done

test:
	go test ./...

clean:
	rm -f review

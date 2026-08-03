package highlight

import (
	"hash/fnv"
	"strconv"
	"sync"
)

// Cache remembers highlighted files. Tokenising and formatting is by far the
// most expensive part of serving a file, and a review keeps coming back to the
// same handful of them: switching away and back, or redrawing the open file
// after the annotations changed, then costs nothing.
//
// Entries are keyed by the file's name and its content, so a file that changed
// on disk is highlighted anew without the cache having to be told about it.
type Cache struct {
	mu      sync.Mutex
	limit   int               // maximum total size of the cached HTML
	size    int               // current total size of the cached HTML
	entries map[string]Result // by content key
	recent  []string          // content keys, least recently used first
}

// NewCache returns a cache holding at most maxBytes of highlighted HTML. The
// most recently used entry is always kept, even where it exceeds that on its
// own, so that the file being reviewed is never the one dropped.
func NewCache(maxBytes int) *Cache {
	return &Cache{
		limit:   maxBytes,
		entries: make(map[string]Result),
	}
}

// Highlight returns syntax-highlighted HTML for the given file content, reusing
// the result of an earlier call for the same file and content.
func (c *Cache) Highlight(filename, content string) Result {
	key := contentKey(filename, content)

	c.mu.Lock()
	if res, ok := c.entries[key]; ok {
		c.touch(key)
		c.mu.Unlock()
		return res
	}
	c.mu.Unlock()

	res := Highlight(filename, content)

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		c.entries[key] = res
		c.recent = append(c.recent, key)
		c.size += len(res.HTML)
		c.evict()
	}
	return res
}

// touch marks a key as the most recently used one.
func (c *Cache) touch(key string) {
	for i, k := range c.recent {
		if k == key {
			c.recent = append(c.recent[:i], c.recent[i+1:]...)
			c.recent = append(c.recent, key)
			return
		}
	}
}

// evict drops least recently used entries until the cache is within its limit.
func (c *Cache) evict() {
	for c.size > c.limit && len(c.recent) > 1 {
		oldest := c.recent[0]
		c.recent = c.recent[1:]
		c.size -= len(c.entries[oldest].HTML)
		delete(c.entries, oldest)
	}
}

// contentKey identifies a file by name and content. The length is part of the
// key so that a hash collision alone cannot serve the wrong file.
func contentKey(filename, content string) string {
	h := fnv.New64a()
	h.Write([]byte(content))
	return filename + "\x00" + strconv.Itoa(len(content)) + "\x00" + strconv.FormatUint(h.Sum64(), 36)
}

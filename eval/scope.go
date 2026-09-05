package eval

import "sort"

// ScopeKind identifies the kind of a variable scope.
type ScopeKind int

const (
	DirectoryScope ScopeKind = iota
	FunctionScope
	BlockScope
)

// Scope is a variable binding scope. Scopes form a linked list via parent pointer.
type Scope struct {
	kind   ScopeKind
	vars   map[string]string // variable name -> value
	set    map[string]bool   // tracks which names are explicitly set
	parent *Scope
}

// NewScope creates a new Scope of the given kind with an optional parent.
func NewScope(kind ScopeKind, parent *Scope) *Scope {
	return &Scope{
		kind:   kind,
		vars:   make(map[string]string),
		set:    make(map[string]bool),
		parent: parent,
	}
}

// Set sets name to value in this scope.
func (s *Scope) Set(name, value string) {
	s.vars[name] = value
	s.set[name] = true
}

// Unset removes name from this scope.
func (s *Scope) Unset(name string) {
	delete(s.vars, name)
	delete(s.set, name)
}

// Get returns the value of name by walking the scope chain.
func (s *Scope) Get(name string) (value string, ok bool) {
	cur := s
	for cur != nil {
		if cur.set[name] {
			return cur.vars[name], true
		}
		cur = cur.parent
	}
	return "", false
}

// HasParent reports whether this scope has an enclosing scope to write into.
func (s *Scope) HasParent() bool { return s.parent != nil }

// SetParent sets name in the parent scope (implements SET(... PARENT_SCOPE)).
func (s *Scope) SetParent(name, value string) {
	if s.parent != nil {
		s.parent.Set(name, value)
	}
}

// UnsetParent removes name from the parent scope.
func (s *Scope) UnsetParent(name string) {
	if s.parent != nil {
		s.parent.Unset(name)
	}
}

// Names returns all variable names defined in this scope (not parents), sorted.
func (s *Scope) Names() []string {
	names := make([]string, 0, len(s.set))
	for k := range s.set {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Copy returns a shallow copy of this scope (same parent pointer).
func (s *Scope) Copy() *Scope {
	ns := &Scope{
		kind:   s.kind,
		vars:   make(map[string]string, len(s.vars)),
		set:    make(map[string]bool, len(s.set)),
		parent: s.parent,
	}
	for k, v := range s.vars {
		ns.vars[k] = v
	}
	for k, v := range s.set {
		ns.set[k] = v
	}
	return ns
}

// ----------------------------------------------------------------------------
// Cache

// CacheEntryType identifies the type of a CMake cache entry.
type CacheEntryType int

const (
	CacheString CacheEntryType = iota
	CacheBool
	CachePath
	CacheFilepath
	CacheInternal
	CacheStatic
	CacheUninitialized
)

// CacheEntry holds one CMake cache variable.
type CacheEntry struct {
	Value    string
	Type     CacheEntryType
	DocStr   string
	Advanced bool
}

// Cache holds all CMake cache entries.
type Cache struct {
	entries map[string]*CacheEntry
}

// NewCache creates an empty Cache.
func NewCache() *Cache {
	return &Cache{entries: make(map[string]*CacheEntry)}
}

// Set stores a cache entry. If force is false and the entry already exists,
// the value is not updated (matching CMake CACHE ... without FORCE behaviour).
func (c *Cache) Set(name, value string, typ CacheEntryType, doc string, force bool) {
	if existing, ok := c.entries[name]; ok && !force {
		if doc != "" {
			existing.DocStr = doc
		}
		return
	}
	c.entries[name] = &CacheEntry{
		Value:  value,
		Type:   typ,
		DocStr: doc,
	}
}

// Get retrieves a cache entry.
func (c *Cache) Get(name string) (entry *CacheEntry, ok bool) {
	e, ok := c.entries[name]
	return e, ok
}

// Unset removes a cache entry.
func (c *Cache) Unset(name string) {
	delete(c.entries, name)
}

// Names returns all cache entry names in sorted order.
func (c *Cache) Names() []string {
	names := make([]string, 0, len(c.entries))
	for k := range c.entries {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// AllNames returns every variable name visible from this scope, including
// those inherited from enclosing scopes, sorted.
func (s *Scope) AllNames() []string {
	seen := map[string]bool{}
	for cur := s; cur != nil; cur = cur.parent {
		for k := range cur.set {
			seen[k] = true
		}
	}
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// mustGet returns the value of name, or "" if it is not set. It exists for the
// callers that want a value and treat "unset" and "empty" alike.
func (s *Scope) mustGet(name string) string {
	v, _ := s.Get(name)
	return v
}

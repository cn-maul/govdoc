// Package store holds the in-memory working state of one GovDoc server
// process: uploaded documents, their plans and rendered files.
//
// # Why in-memory
//
// GovDoc is a single-user desktop-style tool distributed as one binary. The
// documents a user uploads are 公文, which are frequently 涉密 or otherwise
// sensitive, so the safest default is to keep nothing on disk: when the
// process exits, the working set is gone. Persisting a copy of every upload
// to a temp directory would leave confidential text recoverable after a
// crash or an uninstall, which is a bad trade for the convenience of
// resuming a session the user did not ask to resume.
//
// The consequence is that restarting the server invalidates outstanding
// doc/file ids, so handlers must answer with a clear 404 rather than a panic.
package store

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
)

// ErrNotFound is returned for an unknown id. Handlers map it to 404.
var ErrNotFound = errors.New("store: 未找到指定对象")

// File is a rendered output held until the user downloads it.
type File struct {
	ID       string
	Filename string
	MIME     string
	Data     []byte
	Created  time.Time
}

// Session is the working state for one uploaded document.
type Session struct {
	ID   string
	Doc  *docmodel.Document
	Plan *docmodel.Plan
	// Report is the verdict for Plan. It is stored alongside the plan so a
	// client that reconnects can still tell whether the plan is renderable.
	Report *docmodel.Report
	// Spec is the formatting standard captured when the document was
	// uploaded. Rendering re-reads the live spec instead, because the user
	// may have changed 行文方向 in the meantime.
	Direction gbstandard.Direction
	// ProfileID is the formatting profile this document is rendered with.
	//
	// It is remembered per document rather than read from the global setting at
	// export time, and that is the whole point of having profiles: a machine
	// that handles both 公文 and 论文 must be able to keep both, and re-exporting
	// an old document after switching the default must not silently reflow it
	// under different rules.
	ProfileID string
	// SynthText is the scaffolding listed by the last successful render.
	SynthText []string

	Created time.Time
	Updated time.Time
}

// Store is the process-wide state. It is safe for concurrent use: the HTTP
// server handles requests in parallel goroutines.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	files    map[string]*File
	// order preserves insertion order so a UI listing is stable rather than
	// following Go's randomised map iteration.
	order []string
	// jobs holds progress streams, keyed by job id.
	jobs map[string]*Job

	// maxFiles bounds rendered outputs held in memory. A user may render
	// repeatedly while tuning a document, and each output is a full .docx.
	maxFiles int
}

// New builds an empty store. maxFiles <= 0 applies a default of 32.
func New(maxFiles int) *Store {
	if maxFiles <= 0 {
		maxFiles = 32
	}
	return &Store{
		sessions: map[string]*Session{},
		files:    map[string]*File{},
		jobs:     map[string]*Job{},
		maxFiles: maxFiles,
	}
}

// PutSession stores a freshly parsed document and returns its id.
func (s *Store) PutSession(id string, doc *docmodel.Document) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	sess := &Session{ID: id, Doc: doc, Created: now, Updated: now}
	s.sessions[id] = sess
	s.order = append(s.order, id)
	return sess
}

// Session returns a stored session.
func (s *Store) Session(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("%w: 文档 %s（服务重启后需重新上传）", ErrNotFound, id)
	}
	return sess, nil
}

// EachSession calls fn for every stored session until fn returns false.
//
// fn runs under the store's read lock, so it must not call back into the store.
// That is why it receives ids rather than *Session values: a caller inspecting a
// profile wants to know *which* documents use it, and handing out live pointers
// would invite mutation without going through UpdateSession.
func (s *Store) EachSession(fn func(id string, sess *Session) bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.order {
		sess, ok := s.sessions[id]
		if !ok {
			continue
		}
		if !fn(id, sess) {
			return
		}
	}
}

// UpdateSession applies mutate to a stored session.
//
// mutate is deliberately infallible. An earlier version took a
// `func(*Session) error` and returned the error to the caller, but the
// mutation had already run in place by then, so a rejected edit left the
// session half-updated with no way to roll back — exactly the kind of
// "sometimes corrupts the stored plan" bug that is very hard to reproduce.
//
// Validation does not actually need to live in the mutation: the plan verdict
// is a pure function of (document, plan), so the handler computes it first and
// then stores both. Storing an *invalid* plan is intentional too — the user
// must be able to keep editing while the UI shows why it does not render yet.
//
// The only failure mode is an unknown id, which touches nothing.
func (s *Store) UpdateSession(id string, mutate func(*Session)) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("%w: 文档 %s", ErrNotFound, id)
	}
	mutate(sess)
	sess.Updated = time.Now()
	return sess, nil
}

// Sessions lists session ids in insertion order.
func (s *Store) Sessions() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

// PutFile stores a rendered output, evicting the oldest once the cap is hit.
func (s *Store) PutFile(f *File) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Created.IsZero() {
		f.Created = time.Now()
	}
	s.files[f.ID] = f
	if len(s.files) <= s.maxFiles {
		return
	}
	// Evict oldest first. Sorting a slice of at most maxFiles+1 entries is
	// cheaper than maintaining a separate LRU list for this traffic pattern.
	type kv struct {
		id string
		t  time.Time
	}
	all := make([]kv, 0, len(s.files))
	for id, file := range s.files {
		all = append(all, kv{id, file.Created})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for i := 0; i < len(all)-s.maxFiles; i++ {
		delete(s.files, all[i].id)
	}
}

// File returns a stored output.
func (s *Store) File(id string) (*File, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.files[id]
	if !ok {
		return nil, fmt.Errorf("%w: 文件 %s（可能已被后续导出淘汰，请重新导出）", ErrNotFound, id)
	}
	return f, nil
}

// FileCount reports how many outputs are held, for tests and diagnostics.
func (s *Store) FileCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.files)
}

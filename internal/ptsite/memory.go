package ptsite

import "sync"

// MemoryState is an in-memory State implementation. The production service
// uses bbolt; this exists for tests and for the runner's nil-state fallback.
type MemoryState struct {
	mu        sync.Mutex
	checkins  map[string]CheckinState
	seen      map[string]bool
	pending   map[string]Message
	firstDone bool
}

func NewMemoryState() *MemoryState {
	return &MemoryState{
		checkins: make(map[string]CheckinState),
		seen:     make(map[string]bool),
		pending:  make(map[string]Message),
	}
}

func (m *MemoryState) LoadCheckin(day, host string) (CheckinState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.checkins[day+"|"+host]
	return st, ok, nil
}

func (m *MemoryState) SaveCheckin(day, host string, st CheckinState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkins[day+"|"+host] = st
	return nil
}

func (m *MemoryState) AllCheckinTerminal(day string, hosts []string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, host := range hosts {
		st, ok := m.checkins[day+"|"+host]
		if !ok || !st.Terminal {
			return false, nil
		}
	}
	return true, nil
}

func (m *MemoryState) FirstRunDone() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.firstDone, nil
}

func (m *MemoryState) SetFirstRunDone() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.firstDone = true
	return nil
}

func (m *MemoryState) MessageSeen(host, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen[host+"|"+id], nil
}

func (m *MemoryState) MarkMessage(host, id, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[host+"|"+id] = true
	return nil
}

func (m *MemoryState) SavePending(msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[msg.Host+"|"+msg.Key()] = msg
	return nil
}

func (m *MemoryState) PendingMessages() ([]Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Message, 0, len(m.pending))
	for _, msg := range m.pending {
		out = append(out, msg)
	}
	return out, nil
}

func (m *MemoryState) DeletePending(host, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pending, host+"|"+id)
	return nil
}

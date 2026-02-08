package testutil

// Model is a simple in-memory key-value store for testing.
// It tracks the expected state of the database.
type Model struct {
	data map[string][]byte
}

// NewModel creates a new empty model.
func NewModel() *Model {
	return &Model{
		data: make(map[string][]byte),
	}
}

// Put stores a key-value pair. Value is copied.
func (m *Model) Put(key, value []byte) {
	// Copy value to prevent external mutation
	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)
	m.data[string(key)] = valueCopy
}

// Delete removes a key.
func (m *Model) Delete(key []byte) {
	delete(m.data, string(key))
}

// Get retrieves a value. Returns (value, true) if found, (nil, false) if not.
func (m *Model) Get(key []byte) ([]byte, bool) {
	value, ok := m.data[string(key)]
	if !ok {
		return nil, false
	}

	// Return a copy to prevent external mutation
	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)
	return valueCopy, true
}

// Keys returns all keys in the model (for iteration in tests).
func (m *Model) Keys() [][]byte {
	keys := make([][]byte, 0, len(m.data))
	for k := range m.data {
		keys = append(keys, []byte(k))
	}
	return keys
}

// Len returns the number of keys in the model.
func (m *Model) Len() int {
	return len(m.data)
}

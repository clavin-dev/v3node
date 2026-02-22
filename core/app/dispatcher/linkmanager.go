package dispatcher

import (
	sync "sync"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

type ManagedWriter struct {
	writer  buf.Writer
	manager *LinkManager
}

func (w *ManagedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	return w.writer.WriteMultiBuffer(mb)
}

func (w *ManagedWriter) Close() error {
	w.manager.RemoveWriter(w)
	return common.Close(w.writer)
}

type LinkManager struct {
	links map[*ManagedWriter]buf.Reader
	mu    sync.RWMutex
}

func (m *LinkManager) AddLink(writer *ManagedWriter, reader buf.Reader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links[writer] = reader
}

func (m *LinkManager) RemoveWriter(writer *ManagedWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.links, writer)
}

func (m *LinkManager) IsEmpty() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.links) == 0
}

func (m *LinkManager) CloseAll() {
	m.mu.Lock()
	entries := make([]struct {
		writer *ManagedWriter
		reader buf.Reader
	}, 0, len(m.links))
	for w, r := range m.links {
		entries = append(entries, struct {
			writer *ManagedWriter
			reader buf.Reader
		}{writer: w, reader: r})
		delete(m.links, w)
	}
	m.mu.Unlock()

	for _, entry := range entries {
		if entry.reader != nil {
			common.Interrupt(entry.reader)
		}
		if entry.writer != nil {
			_ = common.Close(entry.writer.writer)
		}
	}
}

package nntp

import (
	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/persistence"
	"sync"
	"time"
)

type ProviderUsageData struct {
	LastResetDay string `json:"last_reset_day"`
	TotalBytes   int64  `json:"total_bytes"`
	AllTimeBytes int64  `json:"all_time_bytes"`
}

type ProviderUsageManager struct {
	state         *persistence.StateManager
	data          map[string]*ProviderUsageData
	lastPersisted map[string]int64
	mu            sync.RWMutex
}

var providerManager *ProviderUsageManager
var providerManagerMu sync.Mutex

func GetProviderUsageManager(sm *persistence.StateManager) (*ProviderUsageManager, error) {
	providerManagerMu.Lock()
	defer providerManagerMu.Unlock()

	if providerManager != nil {
		return providerManager, nil
	}

	m := &ProviderUsageManager{
		state:         sm,
		data:          make(map[string]*ProviderUsageData),
		lastPersisted: make(map[string]int64),
	}

	if err := m.load(); err != nil {
		return nil, err
	}
	m.initLastPersisted()

	providerManager = m
	return m, nil
}

func (m *ProviderUsageManager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.state.Get("provider_usage", &m.data)
	if err != nil {
		return err
	}

	today := time.Now().Format("2006-01-02")
	needSave := false
	for _, data := range m.data {
		if data == nil {
			continue
		}
		if m.resetIfNeededLocked(data, today) {
			needSave = true
		}
	}
	if needSave {
		_ = m.state.Set("provider_usage", m.data)
	}
	return nil
}

func (m *ProviderUsageManager) initLastPersisted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, d := range m.data {
		if d != nil {
			m.lastPersisted[name] = d.TotalBytes
		}
	}
}

func (m *ProviderUsageManager) save() error {
	return m.state.Set("provider_usage", m.data)
}

func (m *ProviderUsageManager) GetUsage(name string) *ProviderUsageData {
	today := time.Now().Format("2006-01-02")
	m.mu.Lock()

	data, reset := m.ensureUsageLocked(name, today)
	m.mu.Unlock()

	if reset {
		if err := m.persistAndUpdateLast(); err != nil {
			logger.Error("Failed to save reset provider usage data", "name", name, "err", err)
		}
	}

	return data
}

func (m *ProviderUsageManager) AddBytes(name string, delta int64) {
	if delta <= 0 {
		return
	}
	today := time.Now().Format("2006-01-02")
	m.mu.Lock()
	data, reset := m.ensureUsageLocked(name, today)
	data.TotalBytes += delta
	data.AllTimeBytes += delta
	total := data.TotalBytes
	last := m.lastPersisted[name]
	m.mu.Unlock()

	if reset {
		if err := m.persistAndUpdateLast(); err != nil {
			logger.Error("Failed to save reset provider usage data", "name", name, "err", err)
		}
		return
	}

	if total-last >= 1024*1024 {
		if err := m.persistAndUpdateLast(); err != nil {
			logger.Error("Failed to save provider usage data", "name", name, "err", err)
		}
	}
}

func (m *ProviderUsageManager) persistAndUpdateLast() error {
	if err := m.save(); err != nil {
		return err
	}
	m.mu.Lock()
	for n, d := range m.data {
		if d != nil {
			m.lastPersisted[n] = d.TotalBytes
		}
	}
	m.mu.Unlock()
	return nil
}

func (m *ProviderUsageManager) FlushProvider(name string) {
	m.mu.Lock()
	today := time.Now().Format("2006-01-02")
	data, ok := m.data[name]
	reset := false
	if ok && data != nil {
		reset = m.resetIfNeededLocked(data, today)
	}
	if data == nil {
		m.mu.Unlock()
		return
	}
	total := data.TotalBytes
	last := m.lastPersisted[name]
	m.mu.Unlock()

	if reset || total > last {
		if err := m.persistAndUpdateLast(); err != nil {
			logger.Error("Failed to flush provider usage data", "name", name, "err", err)
		}
	}
}

func (m *ProviderUsageManager) SyncUsage(activeNames []string) {
	m.mu.Lock()

	activeMap := make(map[string]bool)
	for _, name := range activeNames {
		activeMap[name] = true
	}

	changed := false
	for name := range m.data {
		if !activeMap[name] {
			logger.Info("Removing orphaned usage data for provider", "name", name)
			delete(m.data, name)
			delete(m.lastPersisted, name)
			changed = true
		}
	}
	m.mu.Unlock()

	if changed {
		if err := m.save(); err != nil {
			logger.Error("Failed to save provider usage data after sync", "err", err)
		}
	}
}

func (m *ProviderUsageManager) ensureUsageLocked(name, today string) (*ProviderUsageData, bool) {
	data, ok := m.data[name]
	if !ok {
		data = &ProviderUsageData{LastResetDay: today}
		m.data[name] = data
		return data, false
	}

	return data, m.resetIfNeededLocked(data, today)
}

func (m *ProviderUsageManager) resetIfNeededLocked(data *ProviderUsageData, today string) bool {
	if data == nil {
		return false
	}

	if data.LastResetDay == "" {
		if data.TotalBytes > data.AllTimeBytes {
			data.AllTimeBytes = data.TotalBytes
		}
		data.LastResetDay = today
		data.TotalBytes = 0
		return true
	}

	if data.LastResetDay == today {
		return false
	}

	logger.Debug("Resetting daily usage for provider", "last_reset", data.LastResetDay, "today", today)
	data.LastResetDay = today
	data.TotalBytes = 0
	return true
}

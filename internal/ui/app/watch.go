package app

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shinyvision/posthero/internal/postman"
)

// All state is changed on the UI goroutine. Callbacks only send messages;
// epochs discard events from an old workspace, login, or local mode.
type watchState struct {
	epoch   int
	cancel  context.CancelFunc
	dirty   map[string]postman.Change
	catchup bool
	retry   time.Time
	queued  bool
}
type watchReadyMsg struct {
	epoch int
	err   error
}
type watchChangeMsg struct {
	epoch  int
	change postman.Change
}
type watchFlushMsg struct{ epoch int }

func (m *Model) stopWatch() {
	if m.watchState.cancel != nil {
		m.watchState.cancel()
	}
	m.watchState = watchState{epoch: m.watchState.epoch + 1}
}

func (m *Model) ensureWatch() tea.Cmd {
	if m.local != nil || m.st == nil || m.st.API == nil || m.send == nil {
		return nil
	}
	w, ok := m.st.API.(postman.Watcher)
	if !ok || m.watchState.cancel != nil || time.Now().Before(m.watchState.retry) {
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.watchState.cancel = cancel
	epoch, ws, send := m.watchState.epoch, m.st.Workspace.ID, m.send
	return func() tea.Msg {
		err := w.Watch(ctx, ws, func(ch postman.Change) {
			if ctx.Err() == nil {
				send(watchChangeMsg{epoch: epoch, change: ch})
			}
		})
		return watchReadyMsg{epoch: epoch, err: err}
	}
}

func (m *Model) watchReady(msg watchReadyMsg) tea.Cmd {
	if msg.epoch != m.watchState.epoch {
		return nil
	}
	if msg.err != nil {
		m.stopWatch()
		m.watchState.retry = time.Now().Add(time.Minute)
		m.setStatus("live updates unavailable: "+msg.err.Error(), true)
		return nil
	}
	// Cover the interval between the initial fetch and subscription.
	m.watchState.catchup = true
	return m.flushWatch()
}

func (m *Model) watchChanged(msg watchChangeMsg) tea.Cmd {
	if msg.epoch != m.watchState.epoch || m.local != nil {
		return nil
	}
	if msg.change.Action == "reconnect" {
		m.watchState.catchup = true
		m.watchState.dirty = nil
	} else if !m.watchState.catchup {
		if m.watchState.dirty == nil {
			m.watchState.dirty = map[string]postman.Change{}
		}
		m.watchState.dirty[msg.change.Kind+":"+msg.change.UID] = msg.change
		if len(m.watchState.dirty) > 1024 {
			m.watchState.dirty = nil
			m.watchState.catchup = true
		}
	}
	if m.watchState.queued {
		return nil
	}
	m.watchState.queued = true
	epoch := m.watchState.epoch
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return watchFlushMsg{epoch: epoch} })
}

func (m *Model) flushWatch() tea.Cmd {
	if m.local != nil || m.refreshing || m.watchState.queued || !m.idle() || time.Now().Before(m.watchState.retry) {
		return nil
	}
	if len(m.watchState.dirty) > 0 || m.watchState.catchup {
		return m.refreshMode(false, true)
	}
	return m.ensureWatch()
}

type sessionRenewedMsg struct {
	api postman.API
	err error
}

func (m *Model) renewSession() tea.Cmd {
	r, ok := m.remote.(interface{ Refresh(context.Context) error })
	if !ok || m.renewing || time.Now().Before(m.nextRenew) {
		return nil
	}
	m.renewing = true
	api, ctx := m.remote, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return sessionRenewedMsg{api: api, err: r.Refresh(ctx)}
	}
}

func (m *Model) sessionRenewed(msg sessionRenewedMsg) tea.Cmd {
	if msg.api != m.remote {
		return nil
	}
	m.renewing = false
	m.nextRenew = time.Now().Add(time.Minute)
	if msg.err != nil {
		m.setStatus("Postman session: "+msg.err.Error(), true)
		if postman.IsUnauthorized(msg.err) && m.form == nil {
			m.postmanLoginProblem()
		}
	}
	return nil
}

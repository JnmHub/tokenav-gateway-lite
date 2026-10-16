package gatewaylite

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

type BindingInput struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	Name      string `json:"name"`
	PublicURL string `json:"public_url"`
	Region    string `json:"region"`
}

type BindingDrain struct {
	Counts    map[string]int64 `json:"counts"`
	Unknown   []string         `json:"unknown"`
	Reachable bool             `json:"reachable"`
	Ready     bool             `json:"ready"`
}

type BindingStatus struct {
	Phase                string `json:"phase"`
	BindingID            string `json:"binding_id"`
	URL                  string `json:"url"`
	Name                 string `json:"name"`
	PublicURL            string `json:"public_url"`
	Region               string `json:"region"`
	GatewayCode          string `json:"gateway_code"`
	NodeID               int64  `json:"node_id"`
	FirstBindingRequired bool   `json:"first_binding_required"`
	RestartAvailable     bool   `json:"restart_available"`
	LastError            string `json:"last_error,omitempty"`
	HandshakeAt          int64  `json:"handshake_at,omitempty"`
	SnapshotAt           int64  `json:"snapshot_at,omitempty"`
	CanCancel            bool   `json:"can_cancel"`
	DisconnectPending    bool   `json:"disconnect_pending"`
}

func (m *BindingManager) Status() BindingStatus {
	r := m.Record()
	d := r.Current
	if r.Pending != nil {
		d = r.Pending
	}
	s := BindingStatus{Phase: r.Phase, FirstBindingRequired: r.Current == nil, RestartAvailable: BindingRestartAvailable(), LastError: r.LastError, HandshakeAt: r.HandshakeAt, SnapshotAt: r.SnapshotAt}
	s.CanCancel = (r.Phase == "blocked" || r.Phase == "draining") && !r.RemoteDetached && r.DisconnectOperation == ""
	s.DisconnectPending = r.DisconnectOperation != ""
	if d != nil {
		s.BindingID = d.BindingID
		s.URL = d.URL
		s.Name = d.Name
		s.PublicURL = d.PublicURL
		s.Region = d.Region
		s.GatewayCode = d.GatewayCode
		s.NodeID = d.NodeID
	}
	return s
}
func (m *BindingManager) Probe(ctx context.Context, in BindingInput) error {
	return m.remote.Probe(ctx, in.URL)
}
func (m *BindingManager) Verify(ctx context.Context, in BindingInput) error {
	return m.remote.Verify(ctx, in.URL, in.Token)
}
func (m *BindingManager) Register(ctx context.Context, in BindingInput) (BindingStatus, error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	if !BindingRestartAvailable() {
		return m.Status(), errors.New("binding_restart_unavailable")
	}
	if err := validateBindingInput(in); err != nil {
		return m.Status(), err
	}
	if err := m.remote.Verify(ctx, in.URL, in.Token); err != nil {
		return m.Status(), err
	}
	m.mu.Lock()
	if m.record.Current != nil || m.record.Phase == "draining" || m.record.Phase == "blocked" {
		m.mu.Unlock()
		return m.Status(), ErrBindingConflict
	}
	if m.record.Phase == "activating" {
		m.mu.Unlock()
		return m.Status(), errors.New("binding_activation_pending")
	}
	next := cloneBindingRecord(m.record)
	if next.Pending == nil {
		id := uuid.NewString()
		code := "gw-" + strings.ReplaceAll(id, "-", "")
		next.Pending = &BindingDestination{BindingID: id, URL: strings.TrimRight(strings.TrimSpace(in.URL), "/"), GatewayCode: code, NodeScope: code, RedisPrefix: "gl:binding:" + id, AdminSyncKey: uuid.NewString() + uuid.NewString(), Credential: "gb_" + uuid.NewString() + uuid.NewString(), Name: strings.TrimSpace(in.Name), PublicURL: strings.TrimRight(strings.TrimSpace(in.PublicURL), "/"), Region: strings.TrimSpace(in.Region)}
	} else if next.Pending.URL != strings.TrimRight(strings.TrimSpace(in.URL), "/") || next.Pending.Name != strings.TrimSpace(in.Name) || next.Pending.PublicURL != strings.TrimRight(strings.TrimSpace(in.PublicURL), "/") || next.Pending.Region != strings.TrimSpace(in.Region) {
		m.mu.Unlock()
		return m.Status(), errors.New("binding_registration_pending_retry_same_destination")
	}
	next.Phase = "registering"
	next.LastError = ""
	if err := m.saveLocked(ctx, next); err != nil {
		m.mu.Unlock()
		return m.Status(), err
	}
	d := *next.Pending
	m.mu.Unlock()
	// Registration retries use the already persisted identity, so a lost response
	// cannot silently create another node or require storing the enrolment token.
	nodeID, err := m.remote.Register(ctx, in.Token, d)
	m.mu.Lock()
	defer m.mu.Unlock()
	next = cloneBindingRecord(m.record)
	if err != nil {
		next.LastError = "节点登记失败，可使用相同资料重试。"
		_ = m.saveLocked(ctx, next)
		return bindingStatusFromRecord(next), err
	}
	if next.Pending == nil || next.Pending.BindingID != d.BindingID {
		return bindingStatusFromRecord(next), ErrBindingConflict
	}
	next.Pending.NodeID = nodeID
	next.Phase = "activating"
	if err = m.saveLocked(ctx, next); err != nil {
		return bindingStatusFromRecord(m.record), err
	}
	if !m.restart() {
		return bindingStatusFromRecord(next), errors.New("binding_restart_unavailable")
	}
	return bindingStatusFromRecord(next), nil
}
func bindingStatusFromRecord(r BindingRecord) BindingStatus {
	m := &BindingManager{record: r}
	return m.Status()
}
func (m *BindingManager) DisconnectCheck(ctx context.Context) (BindingDrain, error) {
	r := m.Record()
	if r.Current != nil && r.DisconnectOperation != "" {
		detached, e := m.remote.DisconnectStatus(ctx, *r.Current, r.DisconnectOperation)
		if e != nil {
			return BindingDrain{Counts: map[string]int64{}, Unknown: []string{"断开确认未取得，请重试核验"}}, e
		}
		if detached {
			return BindingDrain{Counts: map[string]int64{}, Reachable: true, Ready: true}, nil
		}
	}
	if r.Current == nil {
		return BindingDrain{Counts: map[string]int64{}, Ready: true}, nil
	}
	if m.drain == nil {
		return BindingDrain{Unknown: []string{"结算检查不可用"}}, errors.New("binding_drain_unavailable")
	}
	result, err := m.drain(ctx, r)
	if result.Counts == nil {
		result.Counts = map[string]int64{}
	}
	m.mu.Lock()
	result.Counts["inflight"] = m.inflight
	m.mu.Unlock()
	if r.MediaUnknown {
		result.Unknown = append(result.Unknown, "Seedance 视频缺少可靠的未结算登记，无法确认原主站账务已结清")
	}
	if counts, e := m.remote.Drain(ctx, *r.Current); e != nil {
		result.Reachable = false
		result.Unknown = append(result.Unknown, "原主站不可达或节点身份已失效")
	} else {
		result.Reachable = true
		for name, n := range counts {
			result.Counts["control_plane_"+name] = n
		}
	}
	result.Ready = err == nil && result.Reachable && len(result.Unknown) == 0
	for _, n := range result.Counts {
		if n != 0 {
			result.Ready = false
		}
	}
	return result, err
}
func (m *BindingManager) Disconnect(ctx context.Context) (BindingDrain, error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	r := m.Record()
	if r.Current != nil && r.DisconnectOperation != "" {
		detached, err := m.remote.DisconnectStatus(ctx, *r.Current, r.DisconnectOperation)
		if err != nil {
			return BindingDrain{}, err
		}
		if detached {
			return BindingDrain{Counts: map[string]int64{}, Reachable: true, Ready: true}, m.finishDisconnect(ctx)
		}
	}
	m.mu.Lock()
	if m.record.Current == nil || m.record.Pending != nil {
		m.mu.Unlock()
		return BindingDrain{}, ErrBindingConflict
	}
	next := cloneBindingRecord(m.record)
	next.Phase = "draining"
	next.LastError = ""
	if err := m.saveLocked(ctx, next); err != nil {
		m.mu.Unlock()
		return BindingDrain{}, err
	}
	m.mu.Unlock()
	result, err := m.DisconnectCheck(ctx)
	if err != nil || !result.Ready {
		m.mu.Lock()
		next = cloneBindingRecord(m.record)
		next.Phase = "blocked"
		next.LastError = "旧主站仍有待结算任务或检查未完成；请重试或取消断开。"
		_ = m.saveLocked(ctx, next)
		m.mu.Unlock()
		if err != nil {
			return result, err
		}
		return result, errors.New("binding_undrained")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.record.Phase != "draining" || m.inflight != 0 {
		return result, ErrBindingConflict
	}
	// Keep the gate closed throughout the remote acknowledgement and local commit.
	// A failed remote call retains the original credentials and queues for retry.
	if next.DisconnectOperation == "" {
		next.DisconnectOperation = uuid.NewString()
	}
	if err = m.saveLocked(ctx, next); err != nil {
		return result, err
	}
	if err = m.remote.Disconnect(ctx, *m.record.Current, m.record.DisconnectOperation); err != nil {
		return result, err
	}
	next = cloneBindingRecord(m.record)
	next.RemoteDetached = true
	if err = m.saveLocked(ctx, next); err != nil {
		return result, err
	}
	next.Current = nil
	next.Pending = nil
	next.Phase = "unbound"
	next.LastError = ""
	next.HandshakeAt = 0
	next.SnapshotAt = 0
	next.MediaUnknown = false
	next.DisconnectOperation = ""
	next.RemoteDetached = false
	if err = m.saveLocked(ctx, next); err != nil {
		return result, err
	}
	m.restart()
	return result, nil
}
func (m *BindingManager) finishDisconnect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight != 0 {
		return ErrBindingConflict
	}
	next := cloneBindingRecord(m.record)
	next.Current = nil
	next.Pending = nil
	next.Phase = "unbound"
	next.LastError = ""
	next.HandshakeAt = 0
	next.SnapshotAt = 0
	next.DisconnectOperation = ""
	next.RemoteDetached = false
	next.MediaUnknown = false
	if err := m.saveLocked(ctx, next); err != nil {
		return err
	}
	m.restart()
	return nil
}
func (m *BindingManager) Cancel(ctx context.Context) error {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	r := m.Record()
	if r.RemoteDetached {
		return errors.New("binding_remote_detached_cannot_cancel")
	}
	if r.Current != nil && r.DisconnectOperation != "" {
		// A timed-out remote request may still commit after a status read. Once an
		// operation was dispatched, only acknowledgement recovery can reopen it.
		return errors.New("binding_disconnect_pending_cannot_cancel")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if (m.record.Phase != "draining" && m.record.Phase != "blocked") || m.record.Current == nil {
		return ErrBindingConflict
	}
	next := cloneBindingRecord(m.record)
	next.Phase = "bound"
	if next.HandshakeAt == 0 || next.SnapshotAt == 0 {
		next.Phase = "activating"
	}
	next.DisconnectOperation = ""
	next.LastError = ""
	return m.saveLocked(ctx, next)
}
func (m *BindingManager) RetryActivation() bool { return m.restart() }

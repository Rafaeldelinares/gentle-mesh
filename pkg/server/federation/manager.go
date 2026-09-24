package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

var (
	// ErrPeerNotFound is returned when operating on an unknown peer ID.
	ErrPeerNotFound = errors.New("peer not found")

	// ErrInvalidPeerInfo is returned when peer_id or endpoint is empty in a registration request.
	ErrInvalidPeerInfo = errors.New("invalid peer info: peer_id and endpoint must not be empty")

	// ErrSelfPeering is returned when a peer registers with the local coordinator's peer ID.
	ErrSelfPeering = errors.New("cannot peer with self: peer_id matches local cluster")
)

// ManagerConfig defines configuration parameters for the TerritoryManager.
type ManagerConfig struct {
	PeerID      string
	ClusterName string
	LocalSource func() []protocol.ActiveTerritory
	// RunningSource optionally supplies the territories of tasks currently in
	// Preparing/Running states. When nil, RunningManifest falls back to LocalSource.
	RunningSource func() []protocol.ActiveTerritory
	HTTPClient    *http.Client
	SyncTimeout   time.Duration
	MaxFailures   int
}

type peerEntry struct {
	info           protocol.PeerInfo
	authToken      string
	failureCount   int
	cachedManifest *protocol.TerritoryManifest
}

// TerritoryManager manages mesh peering and federation state.
type TerritoryManager struct {
	config ManagerConfig
	mu     sync.RWMutex
	peers  map[string]*peerEntry
}

// NewTerritoryManager constructs a TerritoryManager with sensible defaults.
func NewTerritoryManager(cfg ManagerConfig) *TerritoryManager {
	if cfg.SyncTimeout <= 0 {
		cfg.SyncTimeout = 10 * time.Second
	}
	if cfg.MaxFailures <= 0 {
		cfg.MaxFailures = 3
	}
	if cfg.ClusterName == "" {
		cfg.ClusterName = "gentle-mesh"
	}
	if cfg.PeerID == "" {
		cfg.PeerID = "local-coordinator"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: cfg.SyncTimeout,
		}
	} else if cfg.HTTPClient.Timeout <= 0 {
		cfg.HTTPClient.Timeout = cfg.SyncTimeout
	}
	if cfg.LocalSource == nil {
		cfg.LocalSource = func() []protocol.ActiveTerritory {
			return nil
		}
	}

	return &TerritoryManager{
		config: cfg,
		peers:  make(map[string]*peerEntry),
	}
}

// RegisterPeer validates and registers a peer, updating an existing entry or inserting a new one.
func (tm *TerritoryManager) RegisterPeer(req protocol.PeerRegisterRequest) (*protocol.PeerInfo, error) {
	if req.PeerID == "" || req.Endpoint == "" {
		return nil, ErrInvalidPeerInfo
	}
	if req.PeerID == tm.config.PeerID {
		return nil, ErrSelfPeering
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	entry, exists := tm.peers[req.PeerID]
	if exists {
		if req.ClusterName != "" {
			entry.info.ClusterName = req.ClusterName
		}
		entry.info.Endpoint = req.Endpoint
		if req.AuthToken != "" {
			entry.authToken = req.AuthToken
		}
		entry.info.Status = protocol.PeerStatusActive
		entry.failureCount = 0
	} else {
		entry = &peerEntry{
			info: protocol.PeerInfo{
				PeerID:      req.PeerID,
				ClusterName: req.ClusterName,
				Endpoint:    req.Endpoint,
				Status:      protocol.PeerStatusActive,
				LastSync:    0,
			},
			authToken: req.AuthToken,
		}
		tm.peers[req.PeerID] = entry
	}

	infoCopy := entry.info
	return &infoCopy, nil
}

// DeregisterPeer removes a peer by ID or returns ErrPeerNotFound.
func (tm *TerritoryManager) DeregisterPeer(peerID string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if _, exists := tm.peers[peerID]; !exists {
		return ErrPeerNotFound
	}
	delete(tm.peers, peerID)
	return nil
}

// GetPeer returns a copy of the peer's metadata or ErrPeerNotFound.
func (tm *TerritoryManager) GetPeer(peerID string) (*protocol.PeerInfo, error) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	entry, exists := tm.peers[peerID]
	if !exists {
		return nil, ErrPeerNotFound
	}
	infoCopy := entry.info
	return &infoCopy, nil
}

// ListPeers returns a slice of registered peers sorted ascending by PeerID.
func (tm *TerritoryManager) ListPeers() []protocol.PeerInfo {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	peers := make([]protocol.PeerInfo, 0, len(tm.peers))
	for _, entry := range tm.peers {
		peers = append(peers, entry.info)
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].PeerID < peers[j].PeerID
	})
	return peers
}

// LocalManifest produces a manifest containing the local coordinator's active territories.
func (tm *TerritoryManager) LocalManifest() protocol.TerritoryManifest {
	var territories []protocol.ActiveTerritory
	if tm.config.LocalSource != nil {
		territories = tm.config.LocalSource()
	}
	if territories == nil {
		territories = []protocol.ActiveTerritory{}
	}
	return protocol.TerritoryManifest{
		PeerID:      tm.config.PeerID,
		ClusterName: tm.config.ClusterName,
		Timestamp:   time.Now().Unix(),
		Territories: territories,
	}
}

// RunningManifest produces a manifest containing the coordinator's currently
// running/preparing task territories. It uses config.RunningSource when provided;
// otherwise it falls back to the local source territories.
func (tm *TerritoryManager) RunningManifest() protocol.TerritoryManifest {
	manifest := tm.LocalManifest()
	if tm.config.RunningSource != nil {
		territories := tm.config.RunningSource()
		if territories == nil {
			territories = []protocol.ActiveTerritory{}
		}
		manifest.Territories = territories
	}
	return manifest
}

// SyncPeer fetches and updates the remote peer's territory manifest over HTTP.
func (tm *TerritoryManager) SyncPeer(ctx context.Context, peerID string) (*protocol.TerritoryManifest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if tm.config.SyncTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, tm.config.SyncTimeout)
		defer cancel()
	}

	tm.mu.RLock()
	entry, exists := tm.peers[peerID]
	if !exists {
		tm.mu.RUnlock()
		return nil, ErrPeerNotFound
	}
	endpoint := entry.info.Endpoint
	authToken := entry.authToken
	tm.mu.RUnlock()

	u := strings.TrimRight(endpoint, "/") + "/v1/mesh/territory"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, tm.recordSyncFailure(peerID, err)
	}

	if authToken != "" {
		headerVal := authToken
		if !strings.HasPrefix(strings.ToLower(authToken), "bearer ") {
			headerVal = "Bearer " + authToken
		}
		req.Header.Set("Authorization", headerVal)
	}

	resp, err := tm.config.HTTPClient.Do(req)
	if err != nil {
		return nil, tm.recordSyncFailure(peerID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, tm.recordSyncFailure(peerID, fmt.Errorf("peer returned HTTP %d", resp.StatusCode))
	}

	var manifest protocol.TerritoryManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, tm.recordSyncFailure(peerID, fmt.Errorf("failed to decode territory manifest: %w", err))
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	entry, exists = tm.peers[peerID]
	if !exists {
		return nil, ErrPeerNotFound
	}

	entry.cachedManifest = &manifest
	entry.failureCount = 0
	entry.info.Status = protocol.PeerStatusActive
	entry.info.LastSync = time.Now().Unix()
	if manifest.ClusterName != "" {
		entry.info.ClusterName = manifest.ClusterName
	}

	manifestCopy := manifest
	return &manifestCopy, nil
}

func (tm *TerritoryManager) recordSyncFailure(peerID string, syncErr error) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	entry, exists := tm.peers[peerID]
	if !exists {
		return syncErr
	}

	entry.failureCount++
	if entry.failureCount >= tm.config.MaxFailures {
		entry.info.Status = protocol.PeerStatusOffline
	} else {
		entry.info.Status = protocol.PeerStatusDegraded
	}

	return syncErr
}

// SyncAll initiates concurrent synchronization across all registered peers.
func (tm *TerritoryManager) SyncAll(ctx context.Context) map[string]error {
	tm.mu.RLock()
	peerIDs := make([]string, 0, len(tm.peers))
	for id := range tm.peers {
		peerIDs = append(peerIDs, id)
	}
	tm.mu.RUnlock()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = make(map[string]error, len(peerIDs))
	)

	for _, id := range peerIDs {
		wg.Add(1)
		go func(peerID string) {
			defer wg.Done()
			_, err := tm.SyncPeer(ctx, peerID)
			mu.Lock()
			results[peerID] = err
			mu.Unlock()
		}(id)
	}

	wg.Wait()
	return results
}

// FederatedTerritories returns all active territories combining local and cached peer territories.
func (tm *TerritoryManager) FederatedTerritories() []protocol.ActiveTerritory {
	localManifest := tm.LocalManifest()
	territories := make([]protocol.ActiveTerritory, 0, len(localManifest.Territories))
	territories = append(territories, localManifest.Territories...)

	tm.mu.RLock()
	peerIDs := make([]string, 0, len(tm.peers))
	for id := range tm.peers {
		peerIDs = append(peerIDs, id)
	}
	sort.Strings(peerIDs)

	for _, id := range peerIDs {
		peer := tm.peers[id]
		if peer.cachedManifest != nil {
			territories = append(territories, peer.cachedManifest.Territories...)
		}
	}
	tm.mu.RUnlock()

	return territories
}

// FindConflict checks the target territory against local active territories and all cached peer manifests.
func (tm *TerritoryManager) FindConflict(target protocol.ActiveTerritory) *protocol.TerritoryConflict {
	localManifest := tm.LocalManifest()
	if conflict := localManifest.FindConflict(target); conflict != nil {
		return conflict
	}

	tm.mu.RLock()
	var peerManifests []protocol.TerritoryManifest
	peerIDs := make([]string, 0, len(tm.peers))
	for id := range tm.peers {
		peerIDs = append(peerIDs, id)
	}
	sort.Strings(peerIDs)
	for _, id := range peerIDs {
		peer := tm.peers[id]
		if peer.cachedManifest != nil {
			peerManifests = append(peerManifests, *peer.cachedManifest)
		}
	}
	tm.mu.RUnlock()

	for _, manifest := range peerManifests {
		if conflict := manifest.FindConflict(target); conflict != nil {
			return conflict
		}
	}
	return nil
}

// FindRunningConflict checks the target territory against the coordinator's running
// territory manifest and all cached peer manifests. Records carrying the same TaskID
// as the target are ignored, since they represent the same task identity rather than
// a collision. Returns the first TerritoryConflict encountered, or nil.
func (tm *TerritoryManager) FindRunningConflict(target protocol.ActiveTerritory) *protocol.TerritoryConflict {
	runningManifest := tm.RunningManifest()
	for _, existing := range runningManifest.Territories {
		if conflict := target.ClashesWithOther(existing); conflict != nil {
			return conflict
		}
	}

	tm.mu.RLock()
	var peerManifests []protocol.TerritoryManifest
	peerIDs := make([]string, 0, len(tm.peers))
	for id := range tm.peers {
		peerIDs = append(peerIDs, id)
	}
	sort.Strings(peerIDs)
	for _, id := range peerIDs {
		peer := tm.peers[id]
		if peer.cachedManifest != nil {
			peerManifests = append(peerManifests, *peer.cachedManifest)
		}
	}
	tm.mu.RUnlock()

	for _, manifest := range peerManifests {
		for _, existing := range manifest.Territories {
			if conflict := target.ClashesWithOther(existing); conflict != nil {
				return conflict
			}
		}
	}
	return nil
}

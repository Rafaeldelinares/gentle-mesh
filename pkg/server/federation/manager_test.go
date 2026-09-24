package federation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/federation"
)

func TestTerritoryManager_Registration(t *testing.T) {
	tm := federation.NewTerritoryManager(federation.ManagerConfig{
		PeerID:      "local-coordinator",
		ClusterName: "local-cluster",
	})

	// 1. Missing peer_id
	_, err := tm.RegisterPeer(protocol.PeerRegisterRequest{
		PeerID:   "",
		Endpoint: "http://10.0.0.2:8080",
	})
	if err != federation.ErrInvalidPeerInfo {
		t.Fatalf("expected ErrInvalidPeerInfo, got: %v", err)
	}

	// 2. Missing endpoint
	_, err = tm.RegisterPeer(protocol.PeerRegisterRequest{
		PeerID:   "peer-alpha",
		Endpoint: "",
	})
	if err != federation.ErrInvalidPeerInfo {
		t.Fatalf("expected ErrInvalidPeerInfo, got: %v", err)
	}

	// 3. Self peering
	_, err = tm.RegisterPeer(protocol.PeerRegisterRequest{
		PeerID:   "local-coordinator",
		Endpoint: "http://127.0.0.1:8080",
	})
	if err != federation.ErrSelfPeering {
		t.Fatalf("expected ErrSelfPeering, got: %v", err)
	}

	// 4. Valid registration
	info, err := tm.RegisterPeer(protocol.PeerRegisterRequest{
		PeerID:      "peer-alpha",
		ClusterName: "cluster-alpha",
		Endpoint:    "http://10.0.0.2:8080",
		AuthToken:   "secret-token-alpha",
	})
	if err != nil {
		t.Fatalf("unexpected error registering peer: %v", err)
	}
	if info.PeerID != "peer-alpha" || info.ClusterName != "cluster-alpha" || info.Status != protocol.PeerStatusActive {
		t.Fatalf("unexpected peer info: %+v", info)
	}

	// 5. Update existing peer
	updated, err := tm.RegisterPeer(protocol.PeerRegisterRequest{
		PeerID:      "peer-alpha",
		ClusterName: "cluster-alpha-v2",
		Endpoint:    "http://10.0.0.2:9090",
		AuthToken:   "new-secret-token",
	})
	if err != nil {
		t.Fatalf("unexpected error updating peer: %v", err)
	}
	if updated.ClusterName != "cluster-alpha-v2" || updated.Endpoint != "http://10.0.0.2:9090" {
		t.Fatalf("peer was not updated properly: %+v", updated)
	}
}

func TestTerritoryManager_ListAndGetAndDeregister(t *testing.T) {
	tm := federation.NewTerritoryManager(federation.ManagerConfig{
		PeerID:      "coord-0",
		ClusterName: "main-cluster",
	})

	// Empty list
	if peers := tm.ListPeers(); len(peers) != 0 {
		t.Fatalf("expected 0 peers, got %d", len(peers))
	}

	// Register peers out of order
	_, _ = tm.RegisterPeer(protocol.PeerRegisterRequest{PeerID: "peer-z", Endpoint: "http://z:8080"})
	_, _ = tm.RegisterPeer(protocol.PeerRegisterRequest{PeerID: "peer-a", Endpoint: "http://a:8080"})
	_, _ = tm.RegisterPeer(protocol.PeerRegisterRequest{PeerID: "peer-m", Endpoint: "http://m:8080"})

	// List should be sorted by PeerID
	peers := tm.ListPeers()
	if len(peers) != 3 {
		t.Fatalf("expected 3 peers, got %d", len(peers))
	}
	if peers[0].PeerID != "peer-a" || peers[1].PeerID != "peer-m" || peers[2].PeerID != "peer-z" {
		t.Fatalf("peers not sorted by PeerID: %s, %s, %s", peers[0].PeerID, peers[1].PeerID, peers[2].PeerID)
	}

	// GetPeer success
	peerA, err := tm.GetPeer("peer-a")
	if err != nil || peerA.PeerID != "peer-a" {
		t.Fatalf("expected peer-a, got %v (err: %v)", peerA, err)
	}

	// GetPeer not found
	_, err = tm.GetPeer("non-existent")
	if err != federation.ErrPeerNotFound {
		t.Fatalf("expected ErrPeerNotFound, got %v", err)
	}

	// DeregisterPeer success
	if err := tm.DeregisterPeer("peer-m"); err != nil {
		t.Fatalf("unexpected error deregistering peer: %v", err)
	}
	if len(tm.ListPeers()) != 2 {
		t.Fatalf("expected 2 peers after deregistration")
	}

	// Deregister not found
	if err := tm.DeregisterPeer("peer-m"); err != federation.ErrPeerNotFound {
		t.Fatalf("expected ErrPeerNotFound on duplicate deregistration, got %v", err)
	}
}

func TestTerritoryManager_LocalManifest(t *testing.T) {
	mockLocal := []protocol.ActiveTerritory{
		{
			TaskID:       "task-100",
			Repo:         "github.com/gentleman-programming/gentle-mesh",
			Branch:       "main",
			EditSurfaces: []string{"pkg/server/*"},
			Agent:        "worker",
			TaskSummary:  "Refactor territory manager",
		},
	}

	tm := federation.NewTerritoryManager(federation.ManagerConfig{
		PeerID:      "coord-local",
		ClusterName: "cluster-local",
		LocalSource: func() []protocol.ActiveTerritory {
			return mockLocal
		},
	})

	manifest := tm.LocalManifest()
	if manifest.PeerID != "coord-local" || manifest.ClusterName != "cluster-local" {
		t.Fatalf("unexpected manifest metadata: %+v", manifest)
	}
	if manifest.Timestamp <= 0 {
		t.Fatalf("manifest timestamp should be positive: %d", manifest.Timestamp)
	}
	if len(manifest.Territories) != 1 || manifest.Territories[0].TaskID != "task-100" {
		t.Fatalf("unexpected territories in manifest: %+v", manifest.Territories)
	}
}

func TestTerritoryManager_SyncPeer_Success(t *testing.T) {
	remoteManifest := protocol.TerritoryManifest{
		PeerID:      "peer-cloud",
		ClusterName: "cloud-cluster",
		Timestamp:   time.Now().Unix(),
		Territories: []protocol.ActiveTerritory{
			{
				TaskID:       "task-cloud-1",
				Repo:         "github.com/gentleman-programming/gentle-mesh",
				Branch:       "feature/gpu-acceleration",
				EditSurfaces: []string{"pkg/server/runner/gpu/*"},
				Agent:        "gpu-worker",
				TaskSummary:  "Train CUDA model",
			},
		},
	}

	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/mesh/territory" {
			http.NotFound(w, r)
			return
		}
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(remoteManifest)
	}))
	defer server.Close()

	tm := federation.NewTerritoryManager(federation.ManagerConfig{
		PeerID:      "coord-local",
		ClusterName: "cluster-local",
	})

	_, err := tm.RegisterPeer(protocol.PeerRegisterRequest{
		PeerID:      "peer-cloud",
		ClusterName: "cloud-cluster",
		Endpoint:    server.URL,
		AuthToken:   "super-secret-peer-key",
	})
	if err != nil {
		t.Fatalf("failed to register peer: %v", err)
	}

	manifest, err := tm.SyncPeer(context.Background(), "peer-cloud")
	if err != nil {
		t.Fatalf("unexpected sync error: %v", err)
	}
	if authHeader != "Bearer super-secret-peer-key" {
		t.Fatalf("expected Authorization header with Bearer token, got: %q", authHeader)
	}
	if len(manifest.Territories) != 1 || manifest.Territories[0].TaskID != "task-cloud-1" {
		t.Fatalf("unexpected synced manifest: %+v", manifest)
	}

	// Verify PeerInfo updated
	peer, err := tm.GetPeer("peer-cloud")
	if err != nil {
		t.Fatalf("failed to get peer: %v", err)
	}
	if peer.Status != protocol.PeerStatusActive {
		t.Fatalf("expected peer status Active, got: %s", peer.Status)
	}
	if peer.LastSync <= 0 {
		t.Fatalf("expected positive LastSync timestamp, got: %d", peer.LastSync)
	}
}

func TestTerritoryManager_SyncPeer_Failure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer server.Close()

	tm := federation.NewTerritoryManager(federation.ManagerConfig{
		PeerID:      "coord-local",
		ClusterName: "cluster-local",
		MaxFailures: 2,
	})

	_, _ = tm.RegisterPeer(protocol.PeerRegisterRequest{
		PeerID:   "flaky-peer",
		Endpoint: server.URL,
	})

	// 1st failure -> Degraded
	_, err := tm.SyncPeer(context.Background(), "flaky-peer")
	if err == nil {
		t.Fatalf("expected error from 500 response")
	}
	peer, _ := tm.GetPeer("flaky-peer")
	if peer.Status != protocol.PeerStatusDegraded {
		t.Fatalf("expected PeerStatusDegraded after 1 failure, got: %s", peer.Status)
	}

	// 2nd failure -> Offline (since MaxFailures is 2)
	_, _ = tm.SyncPeer(context.Background(), "flaky-peer")
	peer, _ = tm.GetPeer("flaky-peer")
	if peer.Status != protocol.PeerStatusOffline {
		t.Fatalf("expected PeerStatusOffline after 2 failures, got: %s", peer.Status)
	}
}

func TestTerritoryManager_SyncAll(t *testing.T) {
	manifest := protocol.TerritoryManifest{
		PeerID:      "peer-1",
		ClusterName: "cluster-1",
		Timestamp:   time.Now().Unix(),
		Territories: []protocol.ActiveTerritory{},
	}

	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer s1.Close()

	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer s2.Close()

	tm := federation.NewTerritoryManager(federation.ManagerConfig{
		PeerID:      "coord-local",
		ClusterName: "cluster-local",
	})

	_, _ = tm.RegisterPeer(protocol.PeerRegisterRequest{PeerID: "peer-ok", Endpoint: s1.URL})
	_, _ = tm.RegisterPeer(protocol.PeerRegisterRequest{PeerID: "peer-fail", Endpoint: s2.URL})

	errs := tm.SyncAll(context.Background())
	if errs["peer-ok"] != nil {
		t.Fatalf("expected peer-ok to succeed, got: %v", errs["peer-ok"])
	}
	if errs["peer-fail"] == nil {
		t.Fatalf("expected peer-fail to have error")
	}
}

func TestTerritoryManager_FindConflict_LocalAndFederated(t *testing.T) {
	localActive := []protocol.ActiveTerritory{
		{
			TaskID:       "local-task-1",
			Repo:         "github.com/gentleman-programming/gentle-mesh",
			Branch:       "feature/local-branch",
			EditSurfaces: []string{"pkg/server/http/*"},
			Agent:        "worker",
			TaskSummary:  "HTTP routes hardening",
		},
	}

	peerManifest := protocol.TerritoryManifest{
		PeerID:      "peer-europe",
		ClusterName: "europe-datacenter",
		Timestamp:   time.Now().Unix(),
		Territories: []protocol.ActiveTerritory{
			{
				TaskID:       "europe-task-99",
				Repo:         "github.com/gentleman-programming/gentle-mesh",
				Branch:       "feature/europe-mesh",
				EditSurfaces: []string{"pkg/server/runner/*"},
				Agent:        "runner-worker",
				TaskSummary:  "Mesh runner optimization",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(peerManifest)
	}))
	defer server.Close()

	tm := federation.NewTerritoryManager(federation.ManagerConfig{
		PeerID:      "coord-local",
		ClusterName: "cluster-local",
		LocalSource: func() []protocol.ActiveTerritory {
			return localActive
		},
	})

	_, _ = tm.RegisterPeer(protocol.PeerRegisterRequest{
		PeerID:   "peer-europe",
		Endpoint: server.URL,
	})
	if _, err := tm.SyncPeer(context.Background(), "peer-europe"); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// 1. Conflict with Local territory (branch collision)
	clashLocal := tm.FindConflict(protocol.ActiveTerritory{
		Repo:   "https://github.com/gentleman-programming/gentle-mesh.git",
		Branch: "feature/local-branch",
	})
	if clashLocal == nil || clashLocal.ConflictType != protocol.ConflictBranchLocked {
		t.Fatalf("expected local ConflictBranchLocked, got: %v", clashLocal)
	}

	// 2. Conflict with Federated Peer territory (surface collision)
	clashPeer := tm.FindConflict(protocol.ActiveTerritory{
		Repo:         "git@github.com:gentleman-programming/gentle-mesh",
		Branch:       "feature/my-runner-tweak",
		EditSurfaces: []string{"pkg/server/runner/mesh.go"},
	})
	if clashPeer == nil || clashPeer.ConflictType != protocol.ConflictSurfaceOverlap {
		t.Fatalf("expected federated ConflictSurfaceOverlap, got: %v", clashPeer)
	}
	if clashPeer.ExistingTerritory.TaskID != "europe-task-99" {
		t.Fatalf("expected existing territory to be europe-task-99, got: %s", clashPeer.ExistingTerritory.TaskID)
	}

	// 3. No conflict
	safeTask := tm.FindConflict(protocol.ActiveTerritory{
		Repo:         "github.com/gentleman-programming/gentle-mesh",
		Branch:       "feature/safe-docs",
		EditSurfaces: []string{"docs/guides/*"},
	})
	if safeTask != nil {
		t.Fatalf("expected no conflict, got: %v", safeTask)
	}
}

func TestTerritoryManager_Concurrency(t *testing.T) {
	tm := federation.NewTerritoryManager(federation.ManagerConfig{
		PeerID:      "coord-concurrent",
		ClusterName: "cluster-concurrent",
		LocalSource: func() []protocol.ActiveTerritory {
			return []protocol.ActiveTerritory{
				{
					TaskID: "task-concurrent",
					Repo:   "github.com/example/repo",
					Branch: "main",
				},
			}
		},
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			peerID := "peer-" + string(rune('a'+(id%5)))
			_, _ = tm.RegisterPeer(protocol.PeerRegisterRequest{
				PeerID:   peerID,
				Endpoint: "http://10.0.0.1:8080",
			})
			_ = tm.ListPeers()
			_ = tm.LocalManifest()
			_ = tm.FindConflict(protocol.ActiveTerritory{
				Repo:   "github.com/example/repo",
				Branch: "other",
			})
		}(i)
	}
	wg.Wait()
}

func TestTerritoryManager_RunningManifest(t *testing.T) {
	local := []protocol.ActiveTerritory{
		{
			TaskID: "queued-task-1",
			Repo:   "github.com/org/repo",
			Branch: "feature/queued",
		},
	}
	running := []protocol.ActiveTerritory{
		{
			TaskID: "running-task-1",
			Repo:   "github.com/org/repo",
			Branch: "feature/running",
		},
	}

	t.Run("uses RunningSource when provided", func(t *testing.T) {
		tm := federation.NewTerritoryManager(federation.ManagerConfig{
			PeerID:        "coord-running",
			ClusterName:   "cluster-running",
			LocalSource:   func() []protocol.ActiveTerritory { return local },
			RunningSource: func() []protocol.ActiveTerritory { return running },
		})

		manifest := tm.RunningManifest()
		if manifest.PeerID != "coord-running" || manifest.ClusterName != "cluster-running" {
			t.Fatalf("unexpected manifest identity: %+v", manifest)
		}
		if manifest.Timestamp <= 0 {
			t.Errorf("expected positive timestamp, got %d", manifest.Timestamp)
		}
		if len(manifest.Territories) != 1 || manifest.Territories[0].TaskID != "running-task-1" {
			t.Fatalf("expected RunningSource territories in manifest, got %+v", manifest.Territories)
		}
	})

	t.Run("falls back to LocalSource without RunningSource", func(t *testing.T) {
		tm := federation.NewTerritoryManager(federation.ManagerConfig{
			PeerID:      "coord-fallback",
			ClusterName: "cluster-fallback",
			LocalSource: func() []protocol.ActiveTerritory { return local },
		})

		manifest := tm.RunningManifest()
		if len(manifest.Territories) != 1 || manifest.Territories[0].TaskID != "queued-task-1" {
			t.Fatalf("expected LocalSource fallback territories, got %+v", manifest.Territories)
		}
	})

	t.Run("nil RunningSource result yields empty slice", func(t *testing.T) {
		tm := federation.NewTerritoryManager(federation.ManagerConfig{
			PeerID:        "coord-empty",
			ClusterName:   "cluster-empty",
			RunningSource: func() []protocol.ActiveTerritory { return nil },
		})

		manifest := tm.RunningManifest()
		if manifest.Territories == nil {
			t.Fatal("expected non-nil empty Territories slice")
		}
		if len(manifest.Territories) != 0 {
			t.Fatalf("expected 0 territories, got %d", len(manifest.Territories))
		}
	})
}

func TestTerritoryManager_FindRunningConflict(t *testing.T) {
	running := []protocol.ActiveTerritory{
		{
			TaskID:       "running-task-1",
			Repo:         "github.com/gentleman-programming/gentle-mesh",
			Branch:       "feature/running-branch",
			EditSurfaces: []string{"pkg/server/runner/*"},
			Agent:        "worker",
			TaskSummary:  "Runner optimization",
		},
	}
	localQueued := []protocol.ActiveTerritory{
		{
			TaskID: "queued-task-1",
			Repo:   "github.com/gentleman-programming/gentle-mesh",
			Branch: "feature/queued-branch",
		},
	}

	peerManifest := protocol.TerritoryManifest{
		PeerID:      "peer-europe",
		ClusterName: "europe-datacenter",
		Timestamp:   time.Now().Unix(),
		Territories: []protocol.ActiveTerritory{
			{
				TaskID:       "europe-task-99",
				Repo:         "github.com/gentleman-programming/gentle-mesh",
				Branch:       "feature/europe-mesh",
				EditSurfaces: []string{"pkg/server/http/*"},
				Agent:        "runner-worker",
				TaskSummary:  "HTTP routes hardening",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(peerManifest)
	}))
	defer server.Close()

	tm := federation.NewTerritoryManager(federation.ManagerConfig{
		PeerID:        "coord-local",
		ClusterName:   "cluster-local",
		LocalSource:   func() []protocol.ActiveTerritory { return localQueued },
		RunningSource: func() []protocol.ActiveTerritory { return running },
	})

	if _, err := tm.RegisterPeer(protocol.PeerRegisterRequest{
		PeerID:   "peer-europe",
		Endpoint: server.URL,
	}); err != nil {
		t.Fatalf("RegisterPeer failed: %v", err)
	}
	if _, err := tm.SyncPeer(context.Background(), "peer-europe"); err != nil {
		t.Fatalf("SyncPeer failed: %v", err)
	}

	t.Run("conflict with running territory", func(t *testing.T) {
		conflict := tm.FindRunningConflict(protocol.ActiveTerritory{
			TaskID:       "target-1",
			Repo:         "github.com/gentleman-programming/gentle-mesh",
			Branch:       "feature/running-branch",
			EditSurfaces: []string{"docs/notes.md"},
		})
		if conflict == nil || conflict.ConflictType != protocol.ConflictBranchLocked {
			t.Fatalf("expected running ConflictBranchLocked, got: %v", conflict)
		}
		if conflict.ExistingTerritory.TaskID != "running-task-1" {
			t.Fatalf("expected existing running-task-1, got: %s", conflict.ExistingTerritory.TaskID)
		}
	})

	t.Run("conflict with cached peer territory", func(t *testing.T) {
		conflict := tm.FindRunningConflict(protocol.ActiveTerritory{
			TaskID:       "target-2",
			Repo:         "github.com/gentleman-programming/gentle-mesh",
			Branch:       "feature/target-2",
			EditSurfaces: []string{"pkg/server/http/router.go"},
		})
		if conflict == nil || conflict.ConflictType != protocol.ConflictSurfaceOverlap {
			t.Fatalf("expected peer ConflictSurfaceOverlap, got: %v", conflict)
		}
		if conflict.ExistingTerritory.TaskID != "europe-task-99" {
			t.Fatalf("expected existing europe-task-99, got: %s", conflict.ExistingTerritory.TaskID)
		}
	})

	t.Run("self task identity is ignored against running manifest", func(t *testing.T) {
		conflict := tm.FindRunningConflict(protocol.ActiveTerritory{
			TaskID:       "running-task-1",
			Repo:         "github.com/gentleman-programming/gentle-mesh",
			Branch:       "feature/running-branch",
			EditSurfaces: []string{"docs/notes.md"},
		})
		if conflict != nil {
			t.Fatalf("expected same task id to be ignored, got %v: %s", conflict.ConflictType, conflict.Message)
		}
	})

	t.Run("self task identity is ignored against peer manifest", func(t *testing.T) {
		conflict := tm.FindRunningConflict(protocol.ActiveTerritory{
			TaskID:       "europe-task-99",
			Repo:         "github.com/gentleman-programming/gentle-mesh",
			Branch:       "feature/europe-mesh",
			EditSurfaces: []string{"pkg/server/http/router.go"},
		})
		if conflict != nil {
			t.Fatalf("expected same task id in peer manifest to be ignored, got %v: %s", conflict.ConflictType, conflict.Message)
		}
	})

	t.Run("queued local territories are not considered", func(t *testing.T) {
		conflict := tm.FindRunningConflict(protocol.ActiveTerritory{
			TaskID: "target-3",
			Repo:   "github.com/gentleman-programming/gentle-mesh",
			Branch: "feature/queued-branch",
		})
		if conflict != nil {
			t.Fatalf("expected queued local territory to be ignored, got %v: %s", conflict.ConflictType, conflict.Message)
		}
	})

	t.Run("no conflict anywhere", func(t *testing.T) {
		conflict := tm.FindRunningConflict(protocol.ActiveTerritory{
			TaskID:       "target-4",
			Repo:         "github.com/gentleman-programming/gentle-mesh",
			Branch:       "feature/safe-docs",
			EditSurfaces: []string{"docs/guides/*"},
		})
		if conflict != nil {
			t.Fatalf("expected no conflict, got %v: %s", conflict.ConflictType, conflict.Message)
		}
	})

	t.Run("falls back to local source when RunningSource is nil", func(t *testing.T) {
		tmFallback := federation.NewTerritoryManager(federation.ManagerConfig{
			PeerID:      "coord-fallback",
			ClusterName: "cluster-fallback",
			LocalSource: func() []protocol.ActiveTerritory {
				return []protocol.ActiveTerritory{
					{
						TaskID: "local-task-1",
						Repo:   "github.com/org/repo",
						Branch: "feature/local-branch",
					},
				}
			},
		})

		conflict := tmFallback.FindRunningConflict(protocol.ActiveTerritory{
			TaskID: "target-9",
			Repo:   "github.com/org/repo",
			Branch: "feature/local-branch",
		})
		if conflict == nil || conflict.ConflictType != protocol.ConflictBranchLocked {
			t.Fatalf("expected ConflictBranchLocked via LocalSource fallback, got: %v", conflict)
		}
	})
}

package protocol_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

func TestConflictTypeConstants(t *testing.T) {
	tests := []struct {
		got  protocol.ConflictType
		want string
	}{
		{protocol.ConflictBranchLocked, "branch_locked"},
		{protocol.ConflictSurfaceOverlap, "surface_overlap"},
		{protocol.ConflictDuplicateTask, "duplicate_task"},
	}

	for _, tc := range tests {
		if string(tc.got) != tc.want {
			t.Errorf("expected ConflictType %q, got %q", tc.want, tc.got)
		}
	}
}

func TestPeerStatusConstants(t *testing.T) {
	tests := []struct {
		got  protocol.PeerStatus
		want string
	}{
		{protocol.PeerStatusActive, "active"},
		{protocol.PeerStatusDegraded, "degraded"},
		{protocol.PeerStatusOffline, "offline"},
	}

	for _, tc := range tests {
		if string(tc.got) != tc.want {
			t.Errorf("expected PeerStatus %q, got %q", tc.want, tc.got)
		}
	}
}

func TestPeerInfoJSONSerialization(t *testing.T) {
	info := protocol.PeerInfo{
		PeerID:      "peer-cluster-eu-1",
		ClusterName: "la-fabrica-gpu",
		Endpoint:    "https://mesh-eu.internal.net:8443",
		Status:      protocol.PeerStatusActive,
		LastSync:    1725000000,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("failed to marshal PeerInfo: %v", err)
	}

	var parsed protocol.PeerInfo
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal PeerInfo: %v", err)
	}

	if parsed != info {
		t.Errorf("PeerInfo mismatch:\ngot:  %+v\nwant: %+v", parsed, info)
	}

	jsonStr := string(data)
	for _, key := range []string{"peer_id", "cluster_name", "endpoint", "status", "last_sync"} {
		if !strings.Contains(jsonStr, fmt.Sprintf("%q:", key)) {
			t.Errorf("serialized JSON missing expected field %q: %s", key, jsonStr)
		}
	}
}

func TestPeerRegisterRequestJSONSerialization(t *testing.T) {
	req := protocol.PeerRegisterRequest{
		PeerID:      "peer-us-east",
		ClusterName: "us-east-metal",
		Endpoint:    "https://us-east.mesh:8443",
		AuthToken:   "secret-token-xyz",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal PeerRegisterRequest: %v", err)
	}

	var parsed protocol.PeerRegisterRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal PeerRegisterRequest: %v", err)
	}

	if parsed != req {
		t.Errorf("PeerRegisterRequest mismatch:\ngot:  %+v\nwant: %+v", parsed, req)
	}

	jsonStr := string(data)
	for _, key := range []string{"peer_id", "cluster_name", "endpoint", "auth_token"} {
		if !strings.Contains(jsonStr, fmt.Sprintf("%q:", key)) {
			t.Errorf("serialized JSON missing expected field %q: %s", key, jsonStr)
		}
	}
}

func TestActiveTerritoryJSONSerialization(t *testing.T) {
	territory := protocol.ActiveTerritory{
		TaskID:       "task-12345",
		Repo:         "github.com/gentleman-programming/gentle-mesh",
		Branch:       "feature/federation-protocol",
		EditSurfaces: []string{"pkg/protocol/federation.go", "docs/rfcs/001-remote-agent-transport.md"},
		Agent:        "worker",
		TaskSummary:  "Implement peer exchange and territory claims",
		NodeID:       "vps-la-fabrica-gpu",
		StartedAt:    1725001234,
	}

	data, err := json.Marshal(territory)
	if err != nil {
		t.Fatalf("failed to marshal ActiveTerritory: %v", err)
	}

	var parsed protocol.ActiveTerritory
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal ActiveTerritory: %v", err)
	}

	if parsed.TaskID != territory.TaskID ||
		parsed.Repo != territory.Repo ||
		parsed.Branch != territory.Branch ||
		parsed.Agent != territory.Agent ||
		parsed.TaskSummary != territory.TaskSummary ||
		parsed.NodeID != territory.NodeID ||
		parsed.StartedAt != territory.StartedAt {
		t.Errorf("ActiveTerritory scalar fields mismatch:\ngot:  %+v\nwant: %+v", parsed, territory)
	}

	if len(parsed.EditSurfaces) != len(territory.EditSurfaces) {
		t.Fatalf("EditSurfaces length mismatch: got %d, want %d", len(parsed.EditSurfaces), len(territory.EditSurfaces))
	}
	for i := range parsed.EditSurfaces {
		if parsed.EditSurfaces[i] != territory.EditSurfaces[i] {
			t.Errorf("EditSurfaces[%d] mismatch: got %q, want %q", i, parsed.EditSurfaces[i], territory.EditSurfaces[i])
		}
	}

	jsonStr := string(data)
	for _, key := range []string{"task_id", "repo", "branch", "edit_surfaces", "agent", "task_summary", "node_id", "started_at"} {
		if !strings.Contains(jsonStr, fmt.Sprintf("%q:", key)) {
			t.Errorf("serialized JSON missing expected field %q: %s", key, jsonStr)
		}
	}
}

func TestTerritoryManifestJSONSerialization(t *testing.T) {
	manifest := protocol.TerritoryManifest{
		PeerID:      "peer-cluster-primary",
		ClusterName: "primary-mesh",
		Timestamp:   1725005678,
		Territories: []protocol.ActiveTerritory{
			{
				TaskID:       "task-001",
				Repo:         "github.com/gentleman-programming/gentle-mesh",
				Branch:       "feature/task-streaming",
				EditSurfaces: []string{"pkg/server/http/"},
				Agent:        "worker",
				TaskSummary:  "Implement SSE reconnect handling",
				NodeID:       "node-alpha",
				StartedAt:    1725005000,
			},
		},
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal TerritoryManifest: %v", err)
	}

	var parsed protocol.TerritoryManifest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal TerritoryManifest: %v", err)
	}

	if parsed.PeerID != manifest.PeerID ||
		parsed.ClusterName != manifest.ClusterName ||
		parsed.Timestamp != manifest.Timestamp ||
		len(parsed.Territories) != len(manifest.Territories) {
		t.Errorf("TerritoryManifest mismatch:\ngot:  %+v\nwant: %+v", parsed, manifest)
	}

	jsonStr := string(data)
	for _, key := range []string{"peer_id", "cluster_name", "timestamp", "territories"} {
		if !strings.Contains(jsonStr, fmt.Sprintf("%q:", key)) {
			t.Errorf("serialized JSON missing expected field %q: %s", key, jsonStr)
		}
	}
}

func TestTerritoryConflictJSONSerialization(t *testing.T) {
	existing := protocol.ActiveTerritory{
		TaskID:       "task-locked-1",
		Repo:         "github.com/gentleman-programming/gentle-mesh",
		Branch:       "feature/auth",
		EditSurfaces: []string{"pkg/auth/"},
		Agent:        "worker",
		TaskSummary:  "Refactor JWT tokens",
		NodeID:       "node-1",
		StartedAt:    1725000100,
	}

	conflict := protocol.TerritoryConflict{
		ConflictType:      protocol.ConflictBranchLocked,
		ExistingTerritory: existing,
		Message:           "branch \"feature/auth\" is already locked by task \"task-locked-1\" on node \"node-1\"",
	}

	data, err := json.Marshal(conflict)
	if err != nil {
		t.Fatalf("failed to marshal TerritoryConflict: %v", err)
	}

	var parsed protocol.TerritoryConflict
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal TerritoryConflict: %v", err)
	}

	if parsed.ConflictType != conflict.ConflictType ||
		parsed.Message != conflict.Message ||
		parsed.ExistingTerritory.TaskID != existing.TaskID {
		t.Errorf("TerritoryConflict mismatch:\ngot:  %+v\nwant: %+v", parsed, conflict)
	}

	if conflict.Error() != conflict.Message {
		t.Errorf("Error() should match Message: got %q, want %q", conflict.Error(), conflict.Message)
	}

	var nilConflict *protocol.TerritoryConflict
	if nilConflict.Error() != "" {
		t.Errorf("nil TerritoryConflict.Error() should return empty string, got %q", nilConflict.Error())
	}
}

func TestClashesWith_BranchMatch(t *testing.T) {
	target := protocol.ActiveTerritory{
		TaskID:       "task-new",
		Repo:         "github.com/org/repo",
		Branch:       "feature/migration",
		EditSurfaces: []string{"pkg/db/migrate.go"},
		TaskSummary:  "Apply migration 05",
		NodeID:       "node-a",
	}

	existing := protocol.ActiveTerritory{
		TaskID:       "task-active",
		Repo:         "github.com/org/repo",
		Branch:       "feature/migration",
		EditSurfaces: []string{"pkg/other/service.go"},
		TaskSummary:  "Different task on same branch",
		NodeID:       "node-b",
	}

	conflict := target.ClashesWith(existing)
	if conflict == nil {
		t.Fatalf("expected conflict on branch match, got nil")
	}

	if conflict.ConflictType != protocol.ConflictBranchLocked {
		t.Errorf("expected ConflictType %s, got %s", protocol.ConflictBranchLocked, conflict.ConflictType)
	}
	if conflict.ExistingTerritory.TaskID != existing.TaskID {
		t.Errorf("expected ExistingTerritory.TaskID %q, got %q", existing.TaskID, conflict.ExistingTerritory.TaskID)
	}
	if !strings.Contains(conflict.Message, "branch") || !strings.Contains(conflict.Message, "feature/migration") {
		t.Errorf("unexpected conflict message: %s", conflict.Message)
	}
}

func TestClashesWith_SurfaceOverlap(t *testing.T) {
	tests := []struct {
		name         string
		targetSurf   []string
		existingSurf []string
		wantOverlap  bool
	}{
		{
			name:         "exact file match",
			targetSurf:   []string{"pkg/protocol/federation.go"},
			existingSurf: []string{"pkg/protocol/federation.go"},
			wantOverlap:  true,
		},
		{
			name:         "parent directory contains file",
			targetSurf:   []string{"pkg/protocol"},
			existingSurf: []string{"pkg/protocol/federation.go"},
			wantOverlap:  true,
		},
		{
			name:         "file inside parent directory",
			targetSurf:   []string{"pkg/protocol/federation.go"},
			existingSurf: []string{"pkg/protocol/"},
			wantOverlap:  true,
		},
		{
			name:         "wildcard directory match",
			targetSurf:   []string{"pkg/protocol/*"},
			existingSurf: []string{"pkg/protocol/federation.go"},
			wantOverlap:  true,
		},
		{
			name:         "entire repo wildcard match",
			targetSurf:   []string{"."},
			existingSurf: []string{"pkg/server/server.go"},
			wantOverlap:  true,
		},
		{
			name:         "completely disjoint files",
			targetSurf:   []string{"pkg/protocol/federation.go"},
			existingSurf: []string{"pkg/server/server.go"},
			wantOverlap:  false,
		},
		{
			name:         "sibling files in same directory without directory claim",
			targetSurf:   []string{"docs/rfcs/001-remote.md"},
			existingSurf: []string{"docs/rfcs/002-other.md"},
			wantOverlap:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := protocol.ActiveTerritory{
				TaskID:       "task-t",
				Repo:         "github.com/org/repo",
				Branch:       "feature/target-branch",
				EditSurfaces: tc.targetSurf,
				TaskSummary:  "Task Alpha",
			}
			existing := protocol.ActiveTerritory{
				TaskID:       "task-e",
				Repo:         "github.com/org/repo",
				Branch:       "feature/existing-branch",
				EditSurfaces: tc.existingSurf,
				TaskSummary:  "Task Beta",
			}

			conflict := target.ClashesWith(existing)
			if tc.wantOverlap {
				if conflict == nil {
					t.Fatalf("expected ConflictSurfaceOverlap, got nil")
				}
				if conflict.ConflictType != protocol.ConflictSurfaceOverlap {
					t.Errorf("expected ConflictSurfaceOverlap, got %s", conflict.ConflictType)
				}
				if conflict.ExistingTerritory.TaskID != existing.TaskID {
					t.Errorf("expected ExistingTerritory.TaskID %q, got %q", existing.TaskID, conflict.ExistingTerritory.TaskID)
				}
			} else {
				if conflict != nil {
					t.Fatalf("expected no conflict, got %s: %s", conflict.ConflictType, conflict.Message)
				}
			}
		})
	}
}

func TestClashesWith_DuplicateTask(t *testing.T) {
	t.Run("identical task ID", func(t *testing.T) {
		target := protocol.ActiveTerritory{
			TaskID:       "task-dup-42",
			Repo:         "github.com/org/repo",
			Branch:       "feature/branch-a",
			EditSurfaces: []string{"pkg/a.go"},
			TaskSummary:  "Summary A",
		}
		existing := protocol.ActiveTerritory{
			TaskID:       "task-dup-42",
			Repo:         "github.com/org/repo",
			Branch:       "feature/branch-b",
			EditSurfaces: []string{"pkg/b.go"},
			TaskSummary:  "Summary B",
		}

		conflict := target.ClashesWith(existing)
		if conflict == nil {
			t.Fatalf("expected duplicate task conflict on matching task ID")
		}
		if conflict.ConflictType != protocol.ConflictDuplicateTask {
			t.Errorf("expected %s, got %s", protocol.ConflictDuplicateTask, conflict.ConflictType)
		}
	})

	t.Run("identical task summary with normalized whitespace and case", func(t *testing.T) {
		target := protocol.ActiveTerritory{
			TaskID:       "task-1",
			Repo:         "github.com/org/repo",
			Branch:       "feature/branch-1",
			EditSurfaces: []string{"pkg/foo.go"},
			TaskSummary:  "  Run   heavy   benchmarks  and verify ",
		}
		existing := protocol.ActiveTerritory{
			TaskID:       "task-2",
			Repo:         "github.com/org/repo",
			Branch:       "feature/branch-2",
			EditSurfaces: []string{"pkg/bar.go"},
			TaskSummary:  "run heavy benchmarks and verify",
		}

		conflict := target.ClashesWith(existing)
		if conflict == nil {
			t.Fatalf("expected duplicate task conflict on matching summary")
		}
		if conflict.ConflictType != protocol.ConflictDuplicateTask {
			t.Errorf("expected %s, got %s", protocol.ConflictDuplicateTask, conflict.ConflictType)
		}
	})
}

func TestClashesWith_NoConflictDifferentRepo(t *testing.T) {
	target := protocol.ActiveTerritory{
		TaskID:       "task-1",
		Repo:         "github.com/org/repo-alpha",
		Branch:       "main",
		EditSurfaces: []string{"pkg/protocol/federation.go"},
		TaskSummary:  "Identical task summary",
	}
	existing := protocol.ActiveTerritory{
		TaskID:       "task-2",
		Repo:         "github.com/org/repo-beta",
		Branch:       "main",
		EditSurfaces: []string{"pkg/protocol/federation.go"},
		TaskSummary:  "Identical task summary",
	}

	conflict := target.ClashesWith(existing)
	if conflict != nil {
		t.Fatalf("expected nil conflict for different repos, got %s: %s", conflict.ConflictType, conflict.Message)
	}
}

func TestClashesWith_NoConflictSameRepoDifferentBranchAndSurfaces(t *testing.T) {
	target := protocol.ActiveTerritory{
		TaskID:       "task-1",
		Repo:         "github.com/org/gentle-mesh",
		Branch:       "feature/auth",
		EditSurfaces: []string{"pkg/server/middleware.go"},
		TaskSummary:  "Implement JWT auth verification",
	}
	existing := protocol.ActiveTerritory{
		TaskID:       "task-2",
		Repo:         "github.com/org/gentle-mesh",
		Branch:       "feature/docs",
		EditSurfaces: []string{"docs/rfcs/001-remote-agent-transport.md"},
		TaskSummary:  "Update RFC documentation for M2M",
	}

	conflict := target.ClashesWith(existing)
	if conflict != nil {
		t.Fatalf("expected nil conflict for isolated work, got %s: %s", conflict.ConflictType, conflict.Message)
	}
}

func TestClashesWith_RepoNormalizationVariations(t *testing.T) {
	repoVariations := []string{
		"github.com/org/repo",
		"https://github.com/org/repo.git",
		"http://github.com/org/repo",
		"git@github.com:org/repo.git",
		"https://github.com/org/repo/",
	}

	for _, r1 := range repoVariations {
		for _, r2 := range repoVariations {
			target := protocol.ActiveTerritory{
				TaskID: "t1",
				Repo:   r1,
				Branch: "main",
			}
			existing := protocol.ActiveTerritory{
				TaskID: "t2",
				Repo:   r2,
				Branch: "main",
			}

			conflict := target.ClashesWith(existing)
			if conflict == nil {
				t.Errorf("expected branch lock conflict between repo variations %q and %q, got nil", r1, r2)
			} else if conflict.ConflictType != protocol.ConflictBranchLocked {
				t.Errorf("expected ConflictBranchLocked, got %s", conflict.ConflictType)
			}
		}
	}
}

func TestTerritoryManifest_FindConflict(t *testing.T) {
	t.Run("empty manifest", func(t *testing.T) {
		manifest := protocol.TerritoryManifest{
			PeerID:      "peer-empty",
			ClusterName: "empty-cluster",
			Territories: []protocol.ActiveTerritory{},
		}
		target := protocol.ActiveTerritory{
			TaskID: "target-1",
			Repo:   "github.com/org/repo",
			Branch: "main",
		}

		conflict := manifest.FindConflict(target)
		if conflict != nil {
			t.Errorf("expected nil conflict on empty manifest, got: %+v", conflict)
		}
	})

	t.Run("clean manifest with multiple non-conflicting territories", func(t *testing.T) {
		manifest := protocol.TerritoryManifest{
			PeerID:      "peer-busy",
			ClusterName: "busy-cluster",
			Territories: []protocol.ActiveTerritory{
				{
					TaskID:       "task-1",
					Repo:         "github.com/org/other-repo",
					Branch:       "main",
					EditSurfaces: []string{"pkg/foo"},
					TaskSummary:  "Other repo task",
				},
				{
					TaskID:       "task-2",
					Repo:         "github.com/org/repo",
					Branch:       "feature/ui",
					EditSurfaces: []string{"web/app.tsx"},
					TaskSummary:  "Build UI dashboard",
				},
			},
		}

		target := protocol.ActiveTerritory{
			TaskID:       "task-3",
			Repo:         "github.com/org/repo",
			Branch:       "feature/backend",
			EditSurfaces: []string{"pkg/server/server.go"},
			TaskSummary:  "Build server API",
		}

		conflict := manifest.FindConflict(target)
		if conflict != nil {
			t.Errorf("expected nil conflict, got: %+v", conflict)
		}
	})

	t.Run("conflicting manifest returns first detected collision", func(t *testing.T) {
		manifest := protocol.TerritoryManifest{
			PeerID:      "peer-cluster",
			ClusterName: "active-cluster",
			Territories: []protocol.ActiveTerritory{
				{
					TaskID:       "task-non-clash",
					Repo:         "github.com/org/unrelated",
					Branch:       "dev",
					EditSurfaces: []string{"README.md"},
				},
				{
					TaskID:       "task-clashing",
					Repo:         "github.com/org/repo",
					Branch:       "feature/shared-work",
					EditSurfaces: []string{"pkg/protocol/"},
					TaskSummary:  "Refactoring protocol",
					NodeID:       "worker-node-1",
				},
			},
		}

		target := protocol.ActiveTerritory{
			TaskID:       "task-new",
			Repo:         "github.com/org/repo",
			Branch:       "feature/shared-work",
			EditSurfaces: []string{"pkg/protocol/events.go"},
			TaskSummary:  "Different summary",
		}

		conflict := manifest.FindConflict(target)
		if conflict == nil {
			t.Fatalf("expected conflict against manifest, got nil")
		}
		if conflict.ConflictType != protocol.ConflictBranchLocked {
			t.Errorf("expected ConflictBranchLocked, got %s", conflict.ConflictType)
		}
		if conflict.ExistingTerritory.TaskID != "task-clashing" {
			t.Errorf("expected existing task 'task-clashing', got %q", conflict.ExistingTerritory.TaskID)
		}
	})
}

func TestActiveTerritory_Fingerprint(t *testing.T) {
	t1 := protocol.ActiveTerritory{
		Repo:        "https://github.com/org/repo.git",
		TaskSummary: "  Implement   feature   X  ",
	}
	t2 := protocol.ActiveTerritory{
		Repo:        "github.com/org/repo",
		TaskSummary: "implement feature x",
	}
	t3 := protocol.ActiveTerritory{
		Repo:        "github.com/org/repo",
		TaskSummary: "implement feature y",
	}

	fp1 := t1.Fingerprint()
	fp2 := t2.Fingerprint()
	fp3 := t3.Fingerprint()

	if fp1 == "" || fp2 == "" || fp3 == "" {
		t.Fatalf("expected non-empty fingerprints")
	}
	if fp1 != fp2 {
		t.Errorf("expected identical fingerprints for normalized repo and summary:\nfp1: %s\nfp2: %s", fp1, fp2)
	}
	if fp1 == fp3 {
		t.Errorf("expected different fingerprints for different summaries, both were %s", fp1)
	}

	emptyTerritory := protocol.ActiveTerritory{}
	if emptyTerritory.Fingerprint() != "" {
		t.Errorf("expected empty fingerprint for empty territory, got %q", emptyTerritory.Fingerprint())
	}
}

func TestConcurrencyAndBoundaryConditions(t *testing.T) {
	manifest := protocol.TerritoryManifest{
		PeerID:      "peer-concurrency",
		ClusterName: "concurrency-mesh",
		Timestamp:   time.Now().Unix(),
		Territories: []protocol.ActiveTerritory{
			{
				TaskID:       "task-c1",
				Repo:         "github.com/org/core",
				Branch:       "feature/c1",
				EditSurfaces: []string{"pkg/core/a.go", "pkg/core/b.go"},
				TaskSummary:  "Core task 1",
			},
			{
				TaskID:       "task-c2",
				Repo:         "github.com/org/core",
				Branch:       "feature/c2",
				EditSurfaces: []string{"pkg/auth/"},
				TaskSummary:  "Auth task 2",
			},
			{
				TaskID:       "task-c3",
				Repo:         "github.com/org/secondary",
				Branch:       "main",
				EditSurfaces: []string{"."},
				TaskSummary:  "Secondary task 3",
			},
		},
	}

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			if idx%3 == 0 {
				// Clashing on surface
				target := protocol.ActiveTerritory{
					TaskID:       fmt.Sprintf("worker-%d", idx),
					Repo:         "github.com/org/core",
					Branch:       fmt.Sprintf("feature/independent-%d", idx),
					EditSurfaces: []string{"pkg/auth/tokens.go"},
					TaskSummary:  "Distinct task",
				}
				conflict := manifest.FindConflict(target)
				if conflict == nil || conflict.ConflictType != protocol.ConflictSurfaceOverlap {
					t.Errorf("goroutine %d expected surface overlap, got %+v", idx, conflict)
				}
			} else if idx%3 == 1 {
				// Clashing on branch
				target := protocol.ActiveTerritory{
					TaskID:       fmt.Sprintf("worker-%d", idx),
					Repo:         "github.com/org/core",
					Branch:       "feature/c1",
					EditSurfaces: []string{"pkg/unrelated/doc.md"},
					TaskSummary:  "Distinct task",
				}
				conflict := manifest.FindConflict(target)
				if conflict == nil || conflict.ConflictType != protocol.ConflictBranchLocked {
					t.Errorf("goroutine %d expected branch lock, got %+v", idx, conflict)
				}
			} else {
				// Clean, no conflict
				target := protocol.ActiveTerritory{
					TaskID:       fmt.Sprintf("worker-%d", idx),
					Repo:         "github.com/org/core",
					Branch:       fmt.Sprintf("feature/clean-%d", idx),
					EditSurfaces: []string{"pkg/metrics/exporter.go"},
					TaskSummary:  fmt.Sprintf("Metrics implementation %d", idx),
				}
				conflict := manifest.FindConflict(target)
				if conflict != nil {
					t.Errorf("goroutine %d expected nil conflict, got %+v", idx, conflict)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestBlastRadiusConstants(t *testing.T) {
	tests := []struct {
		got  protocol.BlastRadius
		want string
	}{
		{protocol.BlastRadiusReadOnly, "read-only"},
		{protocol.BlastRadiusIsolated, "isolated-branch"},
		{protocol.BlastRadiusSharedSchema, "shared-schema"},
		{protocol.BlastRadiusBreaking, "breaking-change"},
	}

	for _, tc := range tests {
		if string(tc.got) != tc.want {
			t.Errorf("expected BlastRadius %q, got %q", tc.want, tc.got)
		}
	}
}

func TestAgentPhaseConstants(t *testing.T) {
	tests := []struct {
		got  protocol.AgentPhase
		want string
	}{
		{protocol.AgentPhaseExplore, "explore"},
		{protocol.AgentPhasePlan, "plan"},
		{protocol.AgentPhaseApply, "apply"},
		{protocol.AgentPhaseVerify, "verify"},
	}

	for _, tc := range tests {
		if string(tc.got) != tc.want {
			t.Errorf("expected AgentPhase %q, got %q", tc.want, tc.got)
		}
	}
}

func TestActiveTerritory_ExtendedFieldsJSON(t *testing.T) {
	t.Run("serializes and deserializes all extended multidimensional fields", func(t *testing.T) {
		territory := protocol.ActiveTerritory{
			TaskID:         "task-radar-001",
			Repo:           "github.com/gentleman-programming/gentle-mesh",
			Branch:         "feature/radar-protocol",
			EditSurfaces:   []string{"pkg/protocol/federation.go", "pkg/server/task/task.go"},
			Agent:          "worker",
			TaskSummary:    "Implement radar protocol and live action tracking",
			NodeID:         "node-alpha-1",
			StartedAt:      1725002000,
			Domain:         "auth",
			BlastRadius:    protocol.BlastRadiusSharedSchema,
			Phase:          protocol.AgentPhaseApply,
			CurrentAction:  "Running tests with race detector",
			LastActivityAt: 1725002050,
		}

		data, err := json.Marshal(territory)
		if err != nil {
			t.Fatalf("failed to marshal territory: %v", err)
		}

		var parsed protocol.ActiveTerritory
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal territory: %v", err)
		}

		if parsed.Domain != territory.Domain {
			t.Errorf("Domain mismatch: got %q, want %q", parsed.Domain, territory.Domain)
		}
		if parsed.BlastRadius != territory.BlastRadius {
			t.Errorf("BlastRadius mismatch: got %q, want %q", parsed.BlastRadius, territory.BlastRadius)
		}
		if parsed.Phase != territory.Phase {
			t.Errorf("Phase mismatch: got %q, want %q", parsed.Phase, territory.Phase)
		}
		if parsed.CurrentAction != territory.CurrentAction {
			t.Errorf("CurrentAction mismatch: got %q, want %q", parsed.CurrentAction, territory.CurrentAction)
		}
		if parsed.LastActivityAt != territory.LastActivityAt {
			t.Errorf("LastActivityAt mismatch: got %d, want %d", parsed.LastActivityAt, territory.LastActivityAt)
		}
	})

	t.Run("defaults BlastRadius to isolated-branch when omitted from JSON", func(t *testing.T) {
		jsonPayload := `{"task_id":"task-unspecified","repo":"github.com/org/repo","agent":"worker"}`
		var parsed protocol.ActiveTerritory
		if err := json.Unmarshal([]byte(jsonPayload), &parsed); err != nil {
			t.Fatalf("failed to unmarshal territory without blast_radius: %v", err)
		}

		if parsed.BlastRadius != protocol.BlastRadiusIsolated {
			t.Errorf("expected default BlastRadius %q, got %q", protocol.BlastRadiusIsolated, parsed.BlastRadius)
		}
	})
}

func TestRadarReportJSONSerialization(t *testing.T) {
	report := protocol.RadarReport{
		ClusterName: "gentle-mesh",
		Timestamp:   1725003000,
		ActiveAgents: []protocol.ActiveTerritory{
			{
				TaskID:         "task-1",
				Repo:           "github.com/org/repo",
				Branch:         "feature/auth",
				EditSurfaces:   []string{"pkg/auth/jwt.go"},
				Agent:          "worker",
				TaskSummary:    "Implement JWT verification",
				NodeID:         "node-gpu-1",
				StartedAt:      1725002800,
				Domain:         "auth",
				BlastRadius:    protocol.BlastRadiusIsolated,
				Phase:          protocol.AgentPhaseApply,
				CurrentAction:  "Editing pkg/auth/jwt.go",
				LastActivityAt: 1725002950,
			},
		},
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("failed to marshal RadarReport: %v", err)
	}

	var parsed protocol.RadarReport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal RadarReport: %v", err)
	}

	if parsed.ClusterName != report.ClusterName {
		t.Errorf("ClusterName mismatch: got %q, want %q", parsed.ClusterName, report.ClusterName)
	}
	if parsed.Timestamp != report.Timestamp {
		t.Errorf("Timestamp mismatch: got %d, want %d", parsed.Timestamp, report.Timestamp)
	}
	if len(parsed.ActiveAgents) != 1 {
		t.Fatalf("expected 1 active agent, got %d", len(parsed.ActiveAgents))
	}
	agent := parsed.ActiveAgents[0]
	if agent.Domain != "auth" || agent.Phase != protocol.AgentPhaseApply || agent.CurrentAction != "Editing pkg/auth/jwt.go" {
		t.Errorf("unexpected agent territory contents: %+v", agent)
	}
}

func TestTerritoryMode_Valid(t *testing.T) {
	tests := []struct {
		name string
		mode protocol.TerritoryMode
		want bool
	}{
		{"queue", protocol.TerritoryModeQueue, true},
		{"warn", protocol.TerritoryModeWarn, true},
		{"strict", protocol.TerritoryModeStrict, true},
		{"disabled", protocol.TerritoryModeDisabled, true},
		{"empty", protocol.TerritoryMode(""), false},
		{"unknown value", protocol.TerritoryMode("bogus"), false},
		{"wrong case", protocol.TerritoryMode("Queue"), false},
		{"trailing space", protocol.TerritoryMode("queue "), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.mode.Valid(); got != tc.want {
				t.Errorf("TerritoryMode(%q).Valid() = %v, want %v", tc.mode, got, tc.want)
			}
		})
	}
}

func TestTerritoryModeConstants(t *testing.T) {
	if protocol.TerritoryModeQueue != "queue" ||
		protocol.TerritoryModeWarn != "warn" ||
		protocol.TerritoryModeStrict != "strict" ||
		protocol.TerritoryModeDisabled != "disabled" {
		t.Fatalf("TerritoryMode constants drifted from their wire values")
	}
}

func TestClashesWithOther_SelfIgnored(t *testing.T) {
	t.Run("identical task identity does not clash", func(t *testing.T) {
		target := protocol.ActiveTerritory{
			TaskID:       "task-42",
			Repo:         "github.com/org/repo",
			Branch:       "feature/branch-a",
			EditSurfaces: []string{"pkg/a.go"},
			TaskSummary:  "Summary A",
			NodeID:       "node-a",
		}

		if conflict := target.ClashesWithOther(target); conflict != nil {
			t.Fatalf("expected self-identity to be ignored, got %v: %s", conflict.ConflictType, conflict.Message)
		}
	})

	t.Run("same task id with clashing fields is ignored", func(t *testing.T) {
		target := protocol.ActiveTerritory{
			TaskID:       "task-42",
			Repo:         "github.com/org/repo",
			Branch:       "feature/branch-a",
			EditSurfaces: []string{"pkg/a.go"},
			TaskSummary:  "Summary A",
		}
		other := protocol.ActiveTerritory{
			TaskID:       "task-42",
			Repo:         "github.com/org/repo",
			Branch:       "feature/branch-a",
			EditSurfaces: []string{"pkg/a.go"},
			TaskSummary:  "Summary A",
			NodeID:       "node-b",
		}

		if conflict := target.ClashesWithOther(other); conflict != nil {
			t.Fatalf("expected same task id to short-circuit, got %v: %s", conflict.ConflictType, conflict.Message)
		}
	})

	t.Run("different task ids still clash", func(t *testing.T) {
		target := protocol.ActiveTerritory{
			TaskID:       "task-target",
			Repo:         "github.com/org/repo",
			Branch:       "feature/shared-branch",
			EditSurfaces: []string{"docs/notes.md"},
		}
		existing := protocol.ActiveTerritory{
			TaskID:       "task-existing",
			Repo:         "github.com/org/repo",
			Branch:       "feature/shared-branch",
			EditSurfaces: []string{"docs/other.md"},
		}

		conflict := target.ClashesWithOther(existing)
		if conflict == nil {
			t.Fatalf("expected branch conflict for different task ids, got nil")
		}
		if conflict.ConflictType != protocol.ConflictBranchLocked {
			t.Errorf("expected %s, got %s", protocol.ConflictBranchLocked, conflict.ConflictType)
		}
	})

	t.Run("empty task id on one side still checks", func(t *testing.T) {
		target := protocol.ActiveTerritory{
			Repo:   "github.com/org/repo",
			Branch: "feature/anonymous",
		}
		existing := protocol.ActiveTerritory{
			TaskID: "task-existing",
			Repo:   "github.com/org/repo",
			Branch: "feature/anonymous",
		}

		conflict := target.ClashesWithOther(existing)
		if conflict == nil || conflict.ConflictType != protocol.ConflictBranchLocked {
			t.Fatalf("expected branch conflict when one side has empty task id, got: %v", conflict)
		}
	})

	t.Run("clashesWith still reports duplicate task id", func(t *testing.T) {
		target := protocol.ActiveTerritory{
			TaskID: "task-42",
			Repo:   "github.com/org/repo",
		}
		other := target

		conflict := target.ClashesWith(other)
		if conflict == nil || conflict.ConflictType != protocol.ConflictDuplicateTask {
			t.Fatalf("ClashesWith must keep reporting duplicate task ids, got: %v", conflict)
		}
	})
}

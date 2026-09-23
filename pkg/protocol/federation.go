package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
)

// PeerStatus defines the operational state of a peer mesh cluster.
type PeerStatus string

const (
	PeerStatusActive   PeerStatus = "active"
	PeerStatusDegraded PeerStatus = "degraded"
	PeerStatusOffline  PeerStatus = "offline"
)

// PeerInfo represents metadata and connection health for a federated peer mesh.
type PeerInfo struct {
	PeerID      string     `json:"peer_id"`
	ClusterName string     `json:"cluster_name"`
	Endpoint    string     `json:"endpoint"`
	Status      PeerStatus `json:"status"`
	LastSync    int64      `json:"last_sync"`
}

// PeerRegisterRequest defines the payload sent by a mesh coordinator to register with a peer.
type PeerRegisterRequest struct {
	PeerID      string `json:"peer_id"`
	ClusterName string `json:"cluster_name"`
	Endpoint    string `json:"endpoint"`
	AuthToken   string `json:"auth_token,omitempty"`
}

// ActiveTerritory represents an ongoing task's claimed territory within a repository.
type ActiveTerritory struct {
	TaskID       string   `json:"task_id"`
	Repo         string   `json:"repo"`
	Branch       string   `json:"branch"`
	EditSurfaces []string `json:"edit_surfaces"`
	Agent        string   `json:"agent"`
	TaskSummary  string   `json:"task_summary"`
	NodeID       string   `json:"node_id"`
	StartedAt    int64    `json:"started_at"`
}

// Fingerprint returns a deterministic SHA-256 fingerprint of the territory's repo and task summary.
func (t ActiveTerritory) Fingerprint() string {
	normRepo := normalizeRepo(t.Repo)
	normSummary := normalizeTaskSummary(t.TaskSummary)
	if normRepo == "" && normSummary == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(normRepo + "\n" + normSummary))
	return hex.EncodeToString(h.Sum(nil))
}

// TerritoryManifest represents the collection of active territories advertised by a mesh cluster.
type TerritoryManifest struct {
	PeerID      string            `json:"peer_id"`
	ClusterName string            `json:"cluster_name"`
	Timestamp   int64             `json:"timestamp"`
	Territories []ActiveTerritory `json:"territories"`
}

// ConflictType categorizes the reason why two territories clash.
type ConflictType string

const (
	ConflictBranchLocked   ConflictType = "branch_locked"
	ConflictSurfaceOverlap ConflictType = "surface_overlap"
	ConflictDuplicateTask  ConflictType = "duplicate_task"
)

// TerritoryConflict provides structured details about a detected territory collision.
type TerritoryConflict struct {
	ConflictType      ConflictType    `json:"conflict_type"`
	ExistingTerritory ActiveTerritory `json:"existing_territory"`
	Message           string          `json:"message"`
}

func (c *TerritoryConflict) Error() string {
	if c == nil {
		return ""
	}
	return c.Message
}

// ClashesWith checks if target territory t clashes with another territory other.
// It returns a *TerritoryConflict if repos match and either branch matches (branch_locked),
// surfaces overlap (surface_overlap), or task summary / fingerprint matches (duplicate_task).
// If no collision is detected, it returns nil.
func (t ActiveTerritory) ClashesWith(other ActiveTerritory) *TerritoryConflict {
	if !repositoriesMatch(t.Repo, other.Repo) {
		return nil
	}

	// 1. Branch-level lock collision
	normBranchA := strings.TrimSpace(t.Branch)
	normBranchB := strings.TrimSpace(other.Branch)
	if normBranchA != "" && normBranchB != "" && strings.EqualFold(normBranchA, normBranchB) {
		return &TerritoryConflict{
			ConflictType:      ConflictBranchLocked,
			ExistingTerritory: other,
			Message:           fmt.Sprintf("branch %q is already locked by task %q on node %q", other.Branch, other.TaskID, other.NodeID),
		}
	}

	// 2. Surface-level overlap collision
	if len(t.EditSurfaces) > 0 && len(other.EditSurfaces) > 0 {
		if overlaps := findSurfaceOverlaps(t.EditSurfaces, other.EditSurfaces); len(overlaps) > 0 {
			return &TerritoryConflict{
				ConflictType:      ConflictSurfaceOverlap,
				ExistingTerritory: other,
				Message:           fmt.Sprintf("edit surfaces overlap on [%s] with existing task %q", strings.Join(overlaps, ", "), other.TaskID),
			}
		}
	}

	// 3. Duplicate task / semantic task fingerprint collision
	if t.TaskID != "" && other.TaskID != "" && t.TaskID == other.TaskID {
		return &TerritoryConflict{
			ConflictType:      ConflictDuplicateTask,
			ExistingTerritory: other,
			Message:           fmt.Sprintf("duplicate task ID %q detected in repository %q", t.TaskID, other.Repo),
		}
	}

	normSummaryA := normalizeTaskSummary(t.TaskSummary)
	normSummaryB := normalizeTaskSummary(other.TaskSummary)
	if normSummaryA != "" && normSummaryB != "" && normSummaryA == normSummaryB {
		return &TerritoryConflict{
			ConflictType:      ConflictDuplicateTask,
			ExistingTerritory: other,
			Message:           fmt.Sprintf("duplicate task detected with matching summary %q (existing task %q on node %q)", other.TaskSummary, other.TaskID, other.NodeID),
		}
	}

	fpA := t.Fingerprint()
	fpB := other.Fingerprint()
	if fpA != "" && fpB != "" && fpA == fpB {
		return &TerritoryConflict{
			ConflictType:      ConflictDuplicateTask,
			ExistingTerritory: other,
			Message:           fmt.Sprintf("duplicate task fingerprint match detected (existing task %q on node %q)", other.TaskID, other.NodeID),
		}
	}

	return nil
}

// FindConflict checks the target territory against all active territories advertised in the manifest.
// Returns the first TerritoryConflict encountered, or nil if no conflict exists.
func (m TerritoryManifest) FindConflict(target ActiveTerritory) *TerritoryConflict {
	for _, existing := range m.Territories {
		if conflict := target.ClashesWith(existing); conflict != nil {
			return conflict
		}
	}
	return nil
}

func normalizeRepo(repo string) string {
	r := strings.TrimSpace(repo)
	if r == "" {
		return ""
	}
	r = strings.TrimSuffix(r, "/")
	r = strings.TrimSuffix(r, ".git")

	if idx := strings.Index(r, "://"); idx != -1 {
		r = r[idx+3:]
	} else if strings.HasPrefix(r, "git@") {
		r = strings.TrimPrefix(r, "git@")
		r = strings.Replace(r, ":", "/", 1)
	}

	return strings.ToLower(r)
}

func repositoriesMatch(repoA, repoB string) bool {
	nA := normalizeRepo(repoA)
	nB := normalizeRepo(repoB)
	if nA == "" || nB == "" {
		return false
	}
	return nA == nB
}

func cleanSurface(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "./")
	s = strings.TrimSuffix(s, "/")
	cleaned := path.Clean(s)
	if cleaned == "" || cleaned == "." {
		return "."
	}
	return cleaned
}

func surfacesOverlap(s1, s2 string) bool {
	c1 := cleanSurface(s1)
	c2 := cleanSurface(s2)

	if c1 == "." || c2 == "." {
		return true
	}
	if c1 == c2 {
		return true
	}
	if strings.HasPrefix(c2, c1+"/") || strings.HasPrefix(c1, c2+"/") {
		return true
	}

	if strings.ContainsAny(c1, "*?[]") {
		if matched, _ := path.Match(c1, c2); matched {
			return true
		}
		base1 := strings.TrimSuffix(strings.TrimSuffix(c1, "/*"), "/**")
		if base1 != c1 && (c2 == base1 || strings.HasPrefix(c2, base1+"/")) {
			return true
		}
	}
	if strings.ContainsAny(c2, "*?[]") {
		if matched, _ := path.Match(c2, c1); matched {
			return true
		}
		base2 := strings.TrimSuffix(strings.TrimSuffix(c2, "/*"), "/**")
		if base2 != c2 && (c1 == base2 || strings.HasPrefix(c1, base2+"/")) {
			return true
		}
	}

	return false
}

func findSurfaceOverlaps(surfacesA, surfacesB []string) []string {
	var overlaps []string
	seen := make(map[string]struct{})

	for _, a := range surfacesA {
		for _, b := range surfacesB {
			if surfacesOverlap(a, b) {
				key := cleanSurface(a)
				if cleanSurface(a) != cleanSurface(b) {
					key = fmt.Sprintf("%s <-> %s", cleanSurface(a), cleanSurface(b))
				}
				if _, exists := seen[key]; !exists {
					seen[key] = struct{}{}
					overlaps = append(overlaps, key)
				}
			}
		}
	}
	return overlaps
}

func normalizeTaskSummary(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	words := strings.Fields(s)
	return strings.Join(words, " ")
}

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
}

// runCLI parses top-level subcommands and dispatches to corresponding handlers.
func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return errors.New("subcommand is required")
	}

	cmd := args[0]
	cmdArgs := args[1:]

	switch cmd {
	case "server":
		return runServer(ctx, cmdArgs, stdout, stderr)
	case "worker":
		return runWorker(ctx, cmdArgs, stdout, stderr)
	case "nodes":
		return runNodes(ctx, cmdArgs, stdout, stderr)
	case "radar":
		return runRadar(ctx, cmdArgs, stdout, stderr)
	case "run":
		return runRun(ctx, cmdArgs, stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown subcommand %q", cmd)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: gentle-mesh <subcommand> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  server    Run the mesh coordinator HTTP REST and SSE server")
	fmt.Fprintln(w, "  worker    Run a mesh worker node registering with coordinator")
	fmt.Fprintln(w, "  nodes     List registered mesh nodes and status")
	fmt.Fprintln(w, "  radar     Display real-time active subagents radar and scope")
	fmt.Fprintln(w, "  run       Submit a task and stream SSE execution events")
	fmt.Fprintln(w, "  help      Show help for gentle-mesh")
}

// runServer starts the coordinator HTTP REST and SSE server.
func runServer(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stderr)

	addr := fs.String("addr", ":8080", "HTTP coordinator listen address")
	tasksDir := fs.String("tasks-dir", "/tmp/gentle-mesh/tasks", "Directory for task logs and state")
	dbPath := fs.String("db-path", "", "Path to SQLite database for task persistence (defaults to <tasks-dir>/gentle-mesh.db, 'none' to disable)")
	heartbeatTimeout := fs.Duration("heartbeat-timeout", 30*time.Second, "Heartbeat timeout for registered nodes")
	token := fs.String("token", "", "Optional bearer authentication token")
	taskTTL := fs.Duration("task-ttl", 24*time.Hour, "Task TTL before pruning")
	territoryMode := fs.String("territory-mode", string(protocol.TerritoryModeQueue), "Territory conflict scheduling mode (queue, warn, strict, disabled)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	mode := protocol.TerritoryMode(*territoryMode)
	if !mode.Valid() {
		return fmt.Errorf("invalid -territory-mode %q: must be one of queue, warn, strict, disabled", *territoryMode)
	}

	srv, err := meshhttp.NewServer(meshhttp.ServerConfig{
		Addr:             *addr,
		TasksDir:         *tasksDir,
		DBPath:           *dbPath,
		HeartbeatTimeout: *heartbeatTimeout,
		TaskTTL:          *taskTTL,
		BearerToken:      *token,
		TerritoryMode:    mode,
	})
	if err != nil {
		return fmt.Errorf("failed to create coordinator server: %w", err)
	}

	fmt.Fprintf(stdout, "Gentle Mesh coordinator starting on %s (tasks dir: %s, territory mode: %s)\n", *addr, *tasksDir, mode)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	case <-ctx.Done():
		fmt.Fprintln(stdout, "Shutting down coordinator...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("error during server shutdown: %w", err)
		}
		fmt.Fprintln(stdout, "Coordinator stopped gracefully.")
		return nil
	}
}

// runWorker runs a worker node that registers with the coordinator and sends heartbeats.
func runWorker(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(stderr)

	coordinator := fs.String("coordinator", "http://localhost:8080", "Coordinator base URL")
	nodeID := fs.String("node-id", "", "Unique node identifier (default: hostname)")
	endpoint := fs.String("endpoint", "http://localhost:8081", "Worker reachable endpoint URL")
	agentsFlag := fs.String("agents", "worker", "Comma-separated agent capabilities (e.g. worker,explore)")
	tagsFlag := fs.String("tags", "", "Comma-separated tags (e.g. go,fast)")
	concurrency := fs.Int("concurrency", 2, "Maximum task concurrency")
	heartbeatInterval := fs.Duration("heartbeat-interval", 10*time.Second, "Heartbeat ping interval")
	token := fs.String("token", "", "Optional bearer authentication token")
	addr := fs.String("addr", "", "Listen address for worker HTTP server (default: port from endpoint or :8081)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	id := *nodeID
	if id == "" {
		h, err := os.Hostname()
		if err == nil && h != "" {
			id = h
		} else {
			id = fmt.Sprintf("worker-%d", time.Now().UnixNano()%1000000)
		}
	}

	agentNames := parseCommaSeparated(*agentsFlag)
	if len(agentNames) == 0 {
		agentNames = []string{"worker"}
	}
	tags := parseCommaSeparated(*tagsFlag)

	hasGPU := false
	for _, t := range tags {
		if strings.EqualFold(t, "gpu") {
			hasGPU = true
			break
		}
	}

	var agentProfiles []protocol.AgentProfile
	for _, name := range agentNames {
		agentProfiles = append(agentProfiles, protocol.AgentProfile{
			Name:        name,
			Description: fmt.Sprintf("%s agent", name),
			Tags:        tags,
		})
	}

	coordURL := strings.TrimRight(*coordinator, "/")
	joinURL := coordURL + "/v1/mesh/join"
	heartbeatURL := coordURL + "/v1/mesh/heartbeat"

	joinReq := protocol.NodeJoinRequest{
		NodeID:   id,
		Endpoint: *endpoint,
		Hardware: protocol.NodeHardware{
			CPUs:   runtime.NumCPU(),
			RAMGB:  8,
			HasGPU: hasGPU,
			OS:     runtime.GOOS,
		},
		Agents:         agentProfiles,
		MaxConcurrency: *concurrency,
	}

	joinBytes, err := json.Marshal(joinReq)
	if err != nil {
		return fmt.Errorf("failed to marshal join payload: %w", err)
	}

	listenAddr := *addr
	if listenAddr == "" {
		listenAddr = extractPortFromEndpoint(*endpoint)
	}

	workerSrv := worker.NewServer(worker.ServerConfig{
		Addr:        listenAddr,
		BearerToken: *token,
	})

	if err := workerSrv.Listen(); err != nil {
		return fmt.Errorf("failed to bind worker HTTP server on %s: %w", listenAddr, err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = workerSrv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(stdout, "Worker HTTP server started on %s\n", workerSrv.Addr())

	srvErrCh := make(chan error, 1)
	go func() {
		if err := workerSrv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErrCh <- err
		}
	}()

	client := &http.Client{Timeout: 10 * time.Second}

	// 1. Initial join request
	hReq, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL, bytes.NewReader(joinBytes))
	if err != nil {
		return fmt.Errorf("failed to build join request: %w", err)
	}
	hReq.Header.Set("Content-Type", "application/json")
	if *token != "" {
		hReq.Header.Set("Authorization", "Bearer "+*token)
	}

	resp, err := client.Do(hReq)
	if err != nil {
		return fmt.Errorf("failed to connect to coordinator at %s: %w", joinURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("coordinator rejected join (status %d): %s", resp.StatusCode, string(body))
	}

	fmt.Fprintf(stdout, "Worker %s joined mesh successfully (endpoint: %s, coordinator: %s)\n", id, *endpoint, coordURL)

	// 2. Periodic heartbeat loop
	ticker := time.NewTicker(*heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(stdout, "Worker %s shutting down...\n", id)
			return nil
		case err := <-srvErrCh:
			return fmt.Errorf("worker server error: %w", err)
		case <-ticker.C:
			hbReq := protocol.NodeHeartbeatRequest{
				NodeID:      id,
				ActiveTasks: workerSrv.ActiveTasks(),
				Timestamp:   time.Now().Unix(),
			}
			hbBytes, _ := json.Marshal(hbReq)

			hbHTTPReq, err := http.NewRequestWithContext(ctx, http.MethodPost, heartbeatURL, bytes.NewReader(hbBytes))
			if err != nil {
				fmt.Fprintf(stderr, "Failed to create heartbeat request: %v\n", err)
				continue
			}
			hbHTTPReq.Header.Set("Content-Type", "application/json")
			if *token != "" {
				hbHTTPReq.Header.Set("Authorization", "Bearer "+*token)
			}

			hbResp, err := client.Do(hbHTTPReq)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				fmt.Fprintf(stderr, "Heartbeat error: %v\n", err)
				continue
			}

			if hbResp.StatusCode == http.StatusNotFound {
				hbResp.Body.Close()
				fmt.Fprintf(stdout, "Worker %s not found on coordinator, re-registering...\n", id)
				reJoinReq, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL, bytes.NewReader(joinBytes))
				if err == nil {
					reJoinReq.Header.Set("Content-Type", "application/json")
					if *token != "" {
						reJoinReq.Header.Set("Authorization", "Bearer "+*token)
					}
					if rResp, err := client.Do(reJoinReq); err == nil {
						rResp.Body.Close()
					}
				}
			} else {
				hbResp.Body.Close()
			}
		}
	}
}

// runNodes queries the coordinator for registered nodes and displays a formatted ASCII table.
func runNodes(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("nodes", flag.ContinueOnError)
	fs.SetOutput(stderr)

	coordinator := fs.String("coordinator", "http://localhost:8080", "Coordinator base URL")
	token := fs.String("token", "", "Optional bearer authentication token")

	if err := fs.Parse(args); err != nil {
		return err
	}

	coordURL := strings.TrimRight(*coordinator, "/") + "/v1/mesh/nodes"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coordURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build nodes request: %w", err)
	}
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to query coordinator nodes at %s: %w", coordURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get nodes (status %d): %s", resp.StatusCode, string(body))
	}

	var data struct {
		Nodes []*protocol.NodeInfo `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fmt.Errorf("failed to parse nodes response: %w", err)
	}

	w := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "NODE ID\tENDPOINT\tSTATUS\tCONCURRENCY\tAGENTS\tTAGS")
	for _, n := range data.Nodes {
		var agentNames []string
		var allTags []string
		tagSet := make(map[string]bool)
		for _, a := range n.Agents {
			agentNames = append(agentNames, a.Name)
			for _, t := range a.Tags {
				if !tagSet[t] {
					tagSet[t] = true
					allTags = append(allTags, t)
				}
			}
		}

		concurrencyStr := fmt.Sprintf("%d/%d", n.ActiveTasks, n.MaxConcurrency)
		agentsStr := strings.Join(agentNames, ",")
		if agentsStr == "" {
			agentsStr = "-"
		}
		tagsStr := strings.Join(allTags, ",")
		if tagsStr == "" {
			tagsStr = "-"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			n.NodeID,
			n.Endpoint,
			n.Status,
			concurrencyStr,
			agentsStr,
			tagsStr,
		)
	}
	_ = w.Flush()

	if len(data.Nodes) == 0 {
		fmt.Fprintln(stdout, "(No nodes registered in the mesh)")
	}

	return nil
}

// runRadar queries the coordinator radar endpoint and displays an active subagent radar table.
func runRadar(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("radar", flag.ContinueOnError)
	fs.SetOutput(stderr)

	coordinator := fs.String("coordinator", "http://localhost:8080", "Coordinator base URL")
	token := fs.String("token", "", "Optional bearer authentication token")

	if err := fs.Parse(args); err != nil {
		return err
	}

	coordURL := strings.TrimRight(*coordinator, "/") + "/v1/mesh/radar"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coordURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build radar request: %w", err)
	}
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to query coordinator radar at %s: %w", coordURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get radar (status %d): %s", resp.StatusCode, string(body))
	}

	var report protocol.RadarReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return fmt.Errorf("failed to parse radar response: %w", err)
	}

	if len(report.ActiveAgents) == 0 {
		fmt.Fprintln(stdout, "No active agents currently running in the mesh radar.")
		return nil
	}

	w := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "NODE/TASK ID\tAGENT\tPHASE\tDOMAIN\tBLAST RADIUS\tSURFACES\tCURRENT ACTION")
	for _, a := range report.ActiveAgents {
		idStr := a.TaskID
		if a.NodeID != "" && a.TaskID != "" {
			idStr = fmt.Sprintf("%s/%s", a.NodeID, a.TaskID)
		} else if a.NodeID != "" {
			idStr = a.NodeID
		}
		if idStr == "" {
			idStr = "-"
		}

		agentStr := a.Agent
		if agentStr == "" {
			agentStr = "-"
		}

		phaseStr := string(a.Phase)
		if phaseStr == "" {
			phaseStr = "-"
		}

		domainStr := a.Domain
		if domainStr == "" {
			domainStr = "-"
		}

		blastStr := string(a.BlastRadius)
		if blastStr == "" {
			blastStr = string(protocol.BlastRadiusIsolated)
		}

		surfacesStr := strings.Join(a.EditSurfaces, ",")
		if surfacesStr == "" {
			surfacesStr = "-"
		}

		actionStr := a.CurrentAction
		if actionStr == "" {
			actionStr = "-"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			idStr,
			agentStr,
			phaseStr,
			domainStr,
			blastStr,
			surfacesStr,
			actionStr,
		)
	}
	_ = w.Flush()

	return nil
}

// runRun dispatches a task to the coordinator and streams SSE events to stdout.
func runRun(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)

	coordinator := fs.String("coordinator", "http://localhost:8080", "Coordinator base URL")
	agent := fs.String("agent", "worker", "Target subagent name")
	taskFlag := fs.String("task", "", "Subagent task prompt (required)")
	repo := fs.String("repo", "", "Optional Git repository URL")
	branch := fs.String("branch", "", "Optional Git branch")
	idempotencyKey := fs.String("idempotency-key", "", "Optional idempotency deduplication key")
	domain := fs.String("domain", "", "Architectural domain (e.g. auth, database, ui)")
	blastRadius := fs.String("blast-radius", "isolated-branch", "Blast radius scope (e.g. isolated-branch, read-only, shared-schema, breaking-change)")
	surfaces := fs.String("surfaces", "", "Comma-separated edit surfaces (files or directories)")
	tags := fs.String("tags", "", "Comma-separated required node tags (e.g. gpu, fast)")
	timeout := fs.Int("timeout", 60, "Task timeout in seconds")
	token := fs.String("token", "", "Optional bearer authentication token")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *taskFlag == "" {
		return errors.New("-task flag is required")
	}

	coordURL := strings.TrimRight(*coordinator, "/")
	tasksURL := coordURL + "/v1/tasks"

	taskReq := protocol.TaskRequest{
		IdempotencyKey: *idempotencyKey,
		Agent:          *agent,
		Task:           *taskFlag,
		GitRepo:        *repo,
		GitBranch:      *branch,
		Domain:         *domain,
		BlastRadius:    protocol.BlastRadius(*blastRadius),
		EditSurfaces:   parseCommaSeparated(*surfaces),
		Tags:           parseCommaSeparated(*tags),
		TimeoutSeconds: *timeout,
	}

	reqBytes, err := json.Marshal(taskReq)
	if err != nil {
		return fmt.Errorf("failed to marshal task request: %w", err)
	}

	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tasksURL, bytes.NewReader(reqBytes))
	if err != nil {
		return fmt.Errorf("failed to build task creation request: %w", err)
	}
	postReq.Header.Set("Content-Type", "application/json")
	if *token != "" {
		postReq.Header.Set("Authorization", "Bearer "+*token)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(postReq)
	if err != nil {
		return fmt.Errorf("failed to dispatch task to coordinator: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("coordinator rejected task (status %d): %s", resp.StatusCode, string(body))
	}

	var taskResp protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		return fmt.Errorf("failed to parse task response: %w", err)
	}

	fmt.Fprintf(stdout, "Task ID: %s (status: %s)\n", taskResp.TaskID, taskResp.Status)

	eventsURL := taskResp.EventsURL
	if !strings.HasPrefix(eventsURL, "http://") && !strings.HasPrefix(eventsURL, "https://") {
		eventsURL = coordURL + "/" + strings.TrimLeft(eventsURL, "/")
	}

	// Connect to SSE stream (no short timeout on client for streaming)
	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build events streaming request: %w", err)
	}
	streamReq.Header.Set("Accept", "text/event-stream")
	if *token != "" {
		streamReq.Header.Set("Authorization", "Bearer "+*token)
	}

	streamClient := &http.Client{}
	streamResp, err := streamClient.Do(streamReq)
	if err != nil {
		return fmt.Errorf("failed to connect to task events stream: %w", err)
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResp.Body)
		return fmt.Errorf("failed to stream events (status %d): %s", streamResp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(streamResp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		dataContent := strings.TrimPrefix(line, "data:")
		dataContent = strings.TrimSpace(dataContent)
		if dataContent == "" {
			continue
		}

		evt, err := protocol.ParseSSE([]byte(line))
		if err != nil {
			var directEvt protocol.Event
			if jsonErr := json.Unmarshal([]byte(dataContent), &directEvt); jsonErr != nil {
				continue
			}
			evt = &directEvt
		}

		if printEvent(stdout, evt) {
			return nil
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("error reading event stream: %w", err)
	}

	return nil
}

// printEvent formats an SSE event to stdout. Returns true if the event represents a terminal state.
func printEvent(stdout io.Writer, evt *protocol.Event) bool {
	if evt == nil {
		return false
	}

	switch evt.Type {
	case protocol.EventStatus:
		var sp protocol.StatusPayload
		if err := evt.UnmarshalPayload(&sp); err == nil {
			if sp.Message != "" {
				fmt.Fprintf(stdout, "[status] %s - %s\n", sp.Status, sp.Message)
			} else {
				fmt.Fprintf(stdout, "[status] %s\n", sp.Status)
			}
			if sp.Status == protocol.TaskStatusCompleted || sp.Status == protocol.TaskStatusFailed || sp.Status == protocol.TaskStatusCanceled {
				return true
			}
		} else {
			fmt.Fprintf(stdout, "[status] %s\n", string(evt.Payload))
		}
		return false

	case protocol.EventThought:
		var tp protocol.ThoughtPayload
		if err := evt.UnmarshalPayload(&tp); err == nil {
			fmt.Fprint(stdout, tp.Text)
		} else {
			fmt.Fprint(stdout, string(evt.Payload))
		}
		return false

	case protocol.EventToolCall:
		var tcp protocol.ToolCallPayload
		if err := evt.UnmarshalPayload(&tcp); err == nil {
			argsJSON, _ := json.Marshal(tcp.Args)
			fmt.Fprintf(stdout, "\n[tool_call] %s(%s)\n", tcp.Tool, string(argsJSON))
		} else {
			fmt.Fprintf(stdout, "\n[tool_call] %s\n", string(evt.Payload))
		}
		return false

	case protocol.EventToolResult:
		var trp protocol.ToolResultPayload
		if err := evt.UnmarshalPayload(&trp); err == nil {
			if trp.IsError {
				fmt.Fprintf(stdout, "[tool_result:error] %s\n", trp.Output)
			} else {
				fmt.Fprintf(stdout, "[tool_result] %s\n", trp.Output)
			}
		} else {
			fmt.Fprintf(stdout, "[tool_result] %s\n", string(evt.Payload))
		}
		return false

	case protocol.EventQuery:
		var qp protocol.QueryPayload
		if err := evt.UnmarshalPayload(&qp); err == nil {
			fmt.Fprintf(stdout, "\n[query] %s (choices: %v)\n", qp.Prompt, qp.Choices)
		} else {
			fmt.Fprintf(stdout, "\n[query] %s\n", string(evt.Payload))
		}
		return false

	case protocol.EventCompletion:
		var cp protocol.CompletionPayload
		if err := evt.UnmarshalPayload(&cp); err == nil {
			fmt.Fprintf(stdout, "\n[completion] %s\n", cp.Result)
			if len(cp.FilesChanged) > 0 {
				fmt.Fprintf(stdout, "  Files changed: %s\n", strings.Join(cp.FilesChanged, ", "))
			}
			if cp.CommitHash != "" {
				fmt.Fprintf(stdout, "  Commit: %s\n", cp.CommitHash)
			}
		} else {
			fmt.Fprintf(stdout, "\n[completion] %s\n", string(evt.Payload))
		}
		return true

	case protocol.EventError:
		var ep protocol.ErrorPayload
		if err := evt.UnmarshalPayload(&ep); err == nil {
			fmt.Fprintf(stdout, "\n[error] %s: %s\n", ep.Code, ep.Message)
		} else {
			fmt.Fprintf(stdout, "\n[error] %s\n", string(evt.Payload))
		}
		return true

	default:
		fmt.Fprintf(stdout, "[%s] %s\n", evt.Type, string(evt.Payload))
		return false
	}
}

func parseCommaSeparated(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// extractPortFromEndpoint extracts the port from a worker endpoint URL (e.g. "http://worker-alpha:8081" -> ":8081"),
// falling back to ":8081" if absent or invalid.
func extractPortFromEndpoint(endpoint string) string {
	if endpoint == "" {
		return ":8081"
	}
	ep := endpoint
	if !strings.Contains(ep, "://") {
		ep = "http://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return ":8081"
	}
	p := u.Port()
	if p == "" {
		return ":8081"
	}
	return ":" + p
}

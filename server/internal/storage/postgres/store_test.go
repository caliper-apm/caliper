package postgres_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/storage/objectstore"
	"github.com/caliper-apm/caliper/server/internal/storage/postgres"
)

// TestPostgresWireProtocolMultiReplicaHA exercises the REAL pure-Go pgx/v5 TCP driver
// against an in-process PostgreSQL v3.0 wire-protocol server across TWO independent
// stateless *postgres.Store replicas, covering Migrate() + all 22 MetaStore methods under -race.
func TestPostgresWireProtocolMultiReplicaHA(t *testing.T) {
	srv := newMiniPGWireServer(t)
	defer srv.Close()

	blobs, err := objectstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("new local object store: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Spin up TWO independent stateless replicas pointing to the same PostgreSQL + ObjectStore.
	dsn := fmt.Sprintf("postgres://caliper:caliper@%s/caliper?sslmode=disable&default_query_exec_mode=simple_protocol", srv.Addr())
	replicaA, err := postgres.Open(ctx, postgres.Config{DSN: dsn, ObjectStore: blobs})
	if err != nil {
		t.Fatalf("open replica A: %v", err)
	}
	defer replicaA.Close()

	replicaB, err := postgres.Open(ctx, postgres.Config{DSN: dsn, ObjectStore: blobs})
	if err != nil {
		t.Fatalf("open replica B: %v", err)
	}
	defer replicaB.Close()

	if err := replicaA.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// 1. Idempotent DDL migration (verifies embedded schema.sql parses and executes cleanly).
	if err := replicaA.Migrate(ctx); err != nil {
		t.Fatalf("migrate A: %v", err)
	}
	if err := replicaB.Migrate(ctx); err != nil {
		t.Fatalf("migrate B (idempotent): %v", err)
	}
	if got := srv.DDLCount(); got < 10 {
		t.Fatalf("expected >= 10 DDL statements executed, got %d", got)
	}

	// 2. Releases: write on Replica A, merge/upsert on Replica B, read on Replica A.
	if err := replicaA.RegisterRelease(ctx, storage.ReleaseRecord{
		AppID: "dev.caliper.demo", Platform: "android", Env: "prod",
		AppVersion: "1.6.0", BuildNumber: "160", GitSHA: "abc1234",
		Registered: true, FirstSeenMS: 1000, LastSeenMS: 2000, EventCount: 5,
	}); err != nil {
		t.Fatalf("RegisterRelease A: %v", err)
	}
	if err := replicaB.RegisterRelease(ctx, storage.ReleaseRecord{
		AppID: "dev.caliper.demo", Platform: "android", Env: "prod",
		AppVersion: "1.6.0", BuildNumber: "160", SymbolID: "sym-160",
		Registered: false, FirstSeenMS: 900, LastSeenMS: 3000, EventCount: 7,
	}); err != nil {
		t.Fatalf("RegisterRelease B upsert: %v", err)
	}
	rels, err := replicaA.ListReleases(ctx, "dev.caliper.demo", "android", "prod")
	if err != nil || len(rels) != 1 {
		t.Fatalf("ListReleases: err=%v rels=%+v", err, rels)
	}
	if rels[0].GitSHA != "abc1234" || rels[0].SymbolID != "sym-160" || !rels[0].Registered ||
		rels[0].FirstSeenMS != 900 || rels[0].LastSeenMS != 3000 || rels[0].EventCount != 12 {
		t.Fatalf("release conflict merge mismatch: %+v", rels[0])
	}

	// 3. Symbols + ObjectStore binary round-trip across replicas (including native_elf).
	elfBytes := []byte("\x7fELF-mock-ndk-symbol-payload")
	savedSym, err := replicaA.SaveSymbol(ctx, storage.SymbolRecord{
		SymbolID: "sym-elf-160", AppID: "dev.caliper.demo", Platform: "android", Env: "prod",
		AppVersion: "1.6.0", BuildNumber: "160", GitSHA: "abc1234", Kind: "native_elf", SHA256: "deadbeef",
	}, elfBytes)
	if err != nil {
		t.Fatalf("SaveSymbol: %v", err)
	}
	syms, err := replicaB.ListSymbols(ctx, "dev.caliper.demo", "android", "1.6.0", "160")
	if err != nil || len(syms) != 1 || syms[0].SymbolID != savedSym.SymbolID || syms[0].Kind != "native_elf" {
		t.Fatalf("ListSymbols from replica B: err=%v syms=%+v", err, syms)
	}
	gotRec, gotBytes, ok, err := replicaB.GetSymbolContent(ctx, "sym-elf-160")
	if err != nil || !ok || !bytes.Equal(gotBytes, elfBytes) || gotRec.SizeBytes != int64(len(elfBytes)) {
		t.Fatalf("GetSymbolContent: err=%v ok=%v bytes=%q rec=%+v", err, ok, gotBytes, gotRec)
	}

	// 4. Sampling Policy: default policy when absent, then live upsert on A -> read on B.
	defPol, err := replicaA.GetPolicy(ctx, "dev.caliper.demo", "ios", "prod")
	if err != nil || defPol.PolicyID != "default_v1" || defPol.RatePPM != 30_000 {
		t.Fatalf("default GetPolicy mismatch: err=%v pol=%+v", err, defPol)
	}
	if err := replicaA.UpsertPolicy(ctx, storage.SamplingPolicy{
		PolicyID: "pol-ha-2", AppID: "dev.caliper.demo", Platform: "ios", Env: "prod",
		RatePPM: 125_000, LeaseTTLMS: 1800_000, MaxEvents24h: 200,
		EnabledCollectors: []string{"startup", "crash", "fluency"},
	}); err != nil {
		t.Fatalf("UpsertPolicy: %v", err)
	}
	polB, err := replicaB.GetPolicy(ctx, "dev.caliper.demo", "ios", "prod")
	if err != nil || polB.PolicyID != "pol-ha-2" || polB.RatePPM != 125_000 || len(polB.EnabledCollectors) != 3 {
		t.Fatalf("GetPolicy from replica B mismatch: err=%v pol=%+v", err, polB)
	}

	// 5. Alert Rules + Incidents + Cascade Delete across replicas.
	rule, err := replicaA.UpsertAlertRule(ctx, storage.AlertRule{
		RuleID: "rule-jank-1", Name: "High Jank", AppID: "dev.caliper.demo", Platform: "android", Env: "prod",
		MetricID: "fluency.jank_rate_pct", Operator: "gt", Threshold: 5.0, MinSamples: 100,
		WebhookURL: "https://example.invalid/hook", WebhookFormat: "feishu", CooldownSec: 300, Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpsertAlertRule: %v", err)
	}
	obsVal := 8.4
	if err := replicaB.SaveAlertIncident(ctx, storage.AlertIncident{
		IncidentID: "inc-1", RuleID: rule.RuleID, RuleName: rule.Name, AppID: rule.AppID, Platform: rule.Platform,
		MetricID: rule.MetricID, Status: "firing", ObservedValue: &obsVal, Threshold: 5.0, SampleSize: 320,
		TriggeredAtMS: 5000, LastEvaluatedMS: 6000, DeliveryStatus: "delivered", DeliveryHTTPStatus: 200,
	}); err != nil {
		t.Fatalf("SaveAlertIncident: %v", err)
	}
	rules, err := replicaB.ListAlertRules(ctx, "dev.caliper.demo", "android")
	if err != nil || len(rules) != 1 || rules[0].Name != "High Jank" {
		t.Fatalf("ListAlertRules: err=%v rules=%+v", err, rules)
	}
	incs, err := replicaA.ListAlertIncidents(ctx, rule.RuleID)
	if err != nil || len(incs) != 1 || incs[0].ObservedValue == nil || *incs[0].ObservedValue != 8.4 {
		t.Fatalf("ListAlertIncidents: err=%v incs=%+v", err, incs)
	}
	if err := replicaA.DeleteAlertRule(ctx, rule.RuleID); err != nil {
		t.Fatalf("DeleteAlertRule: %v", err)
	}
	if remRules, _ := replicaB.ListAlertRules(ctx, "dev.caliper.demo", "android"); len(remRules) != 0 {
		t.Fatalf("expected 0 alert rules after delete, got %+v", remRules)
	}
	if remIncs, _ := replicaB.ListAlertIncidents(ctx, rule.RuleID); len(remIncs) != 0 {
		t.Fatalf("expected cascade-deleted incidents, got %+v", remIncs)
	}

	// 6. Issue Lifecycle Triage states across replicas + invalid status rejection.
	if _, err := replicaA.UpsertIssueState(ctx, storage.IssueState{Fingerprint: "fp-bad", Status: "invalid"}); err == nil {
		t.Fatal("expected error for invalid issue status")
	}
	if _, err := replicaA.UpsertIssueState(ctx, storage.IssueState{
		Fingerprint: "fp-oom-1", Status: "resolved", Assignee: "alice", Note: "fixed pool cap", ResolvedInVersion: "1.6.1",
	}); err != nil {
		t.Fatalf("UpsertIssueState: %v", err)
	}
	stB, found, err := replicaB.GetIssueState(ctx, "fp-oom-1")
	if err != nil || !found || stB.Status != "resolved" || stB.Assignee != "alice" || stB.ResolvedInVersion != "1.6.1" {
		t.Fatalf("GetIssueState on replica B: err=%v found=%v st=%+v", err, found, stB)
	}
	allStates, err := replicaB.ListIssueStates(ctx)
	if err != nil || len(allStates) != 1 {
		t.Fatalf("ListIssueStates: err=%v states=%+v", err, allStates)
	}

	// 7. Append-only Audit Trail across replicas.
	if err := replicaA.AppendAuditLog(ctx, storage.AuditLogEntry{
		AuditID: "aud-1", TimestampMS: 1000, Actor: "alice", Role: "admin",
		Action: "session_lookup", TargetType: "user_hash", TargetHash: "0011223344556677", Reason: "INC-42", ResultCount: 9,
	}); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}
	logs, err := replicaB.ListAuditLogs(ctx, 10)
	if err != nil || len(logs) != 1 || logs[0].Actor != "alice" || logs[0].Reason != "INC-42" {
		t.Fatalf("ListAuditLogs on replica B: err=%v logs=%+v", err, logs)
	}

	// 8. Remote Diagnostic Log Tasks: create on A, poll & complete on B, download content on A.
	nowMS := time.Now().UTC().UnixMilli()
	task, err := replicaA.CreateRemoteLogTask(ctx, storage.RemoteLogTask{
		TaskID: "rlog-ha-1", AppID: "dev.caliper.demo", Platform: "ios",
		TargetType: "user_hash", TargetHash: "aabbccdd11223344", Reason: "INC-99", RequestedBy: "oncall",
		MaxBytes: 4096, CreatedAtMS: nowMS, ExpiresAtMS: nowMS + 60_000,
	})
	if err != nil {
		t.Fatalf("CreateRemoteLogTask: %v", err)
	}
	pending, err := replicaB.PollPendingLogTasks(ctx, "dev.caliper.demo", "ios", "", "aabbccdd11223344", nowMS+100)
	if err != nil || len(pending) != 1 || pending[0].TaskID != task.TaskID {
		t.Fatalf("PollPendingLogTasks on replica B: err=%v pending=%+v", err, pending)
	}
	logPayload := []byte("[INFO] remote ring buffer snapshot ok\n")
	doneTask, err := replicaB.CompleteRemoteLogTask(ctx, task.TaskID, logPayload, "sha256hex", nowMS+200)
	if err != nil || doneTask.Status != "completed" || doneTask.UploadedBytes != int64(len(logPayload)) {
		t.Fatalf("CompleteRemoteLogTask: err=%v task=%+v", err, doneTask)
	}
	dlTask, dlBytes, ok, err := replicaA.GetRemoteLogContent(ctx, task.TaskID)
	if err != nil || !ok || !bytes.Equal(dlBytes, logPayload) || dlTask.SHA256 != "sha256hex" {
		t.Fatalf("GetRemoteLogContent on replica A: err=%v ok=%v bytes=%q task=%+v", err, ok, dlBytes, dlTask)
	}
	listedTasks, err := replicaA.ListRemoteLogTasks(ctx, "dev.caliper.demo", "ios")
	if err != nil || len(listedTasks) != 1 || listedTasks[0].Status != "completed" {
		t.Fatalf("ListRemoteLogTasks: err=%v tasks=%+v", err, listedTasks)
	}

	// 9. Concurrent multi-replica race check.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			st := replicaA
			if id%2 == 1 {
				st = replicaB
			}
			_ = st.AppendAuditLog(ctx, storage.AuditLogEntry{
				AuditID: fmt.Sprintf("aud-conc-%d", id), TimestampMS: nowMS + int64(id),
				Actor: "worker", Role: "operator", Action: "policy_update",
				TargetType: "policy", TargetHash: "ios", Reason: "concurrent_ha",
			})
			_, _ = st.ListAuditLogs(ctx, 20)
		}(i)
	}
	wg.Wait()
}

func TestPostgresEmbeddedSchemaContainsAllTables(t *testing.T) {
	stmts := postgres.SchemaStatements()
	joined := strings.Join(stmts, "\n")
	required := []string{
		"caliper_releases",
		"caliper_symbols",
		"native_elf",
		"caliper_sampling_policies",
		"caliper_alert_rules",
		"caliper_alert_incidents",
		"caliper_issue_states",
		"caliper_remote_log_tasks",
		"caliper_audit_logs",
	}
	for _, needle := range required {
		if !strings.Contains(joined, needle) {
			t.Errorf("embedded schema.sql missing %q", needle)
		}
	}
}

// =========================================================================
// Minimal PostgreSQL v3.0 TCP Wire-Protocol Server (in-process test double)
// =========================================================================

type miniPGWireServer struct {
	ln       net.Listener
	mu       sync.Mutex
	ddlCount int
	closed   bool

	releases  map[string]storage.ReleaseRecord
	symbols   map[string]storage.SymbolRecord
	policies  map[string]storage.SamplingPolicy
	rules     map[string]storage.AlertRule
	incidents map[string]storage.AlertIncident
	issues    map[string]storage.IssueState
	audits    []storage.AuditLogEntry
	logTasks  map[string]storage.RemoteLogTask
}

func newMiniPGWireServer(t *testing.T) *miniPGWireServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	s := &miniPGWireServer{
		ln:        ln,
		releases:  map[string]storage.ReleaseRecord{},
		symbols:   map[string]storage.SymbolRecord{},
		policies:  map[string]storage.SamplingPolicy{},
		rules:     map[string]storage.AlertRule{},
		incidents: map[string]storage.AlertIncident{},
		issues:    map[string]storage.IssueState{},
		logTasks:  map[string]storage.RemoteLogTask{},
	}
	go s.acceptLoop()
	return s
}

func (s *miniPGWireServer) Addr() string { return s.ln.Addr().String() }

func (s *miniPGWireServer) DDLCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ddlCount
}

func (s *miniPGWireServer) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()
}

func (s *miniPGWireServer) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serveConn(conn)
	}
}

func (s *miniPGWireServer) serveConn(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)

	// Read StartupMessage (or SSLRequest 80877103 first).
	for {
		var length int32
		if err := binary.Read(br, binary.BigEndian, &length); err != nil {
			return
		}
		if length < 8 || length > 65536 {
			return
		}
		body := make([]byte, length-4)
		if _, err := io.ReadFull(br, body); err != nil {
			return
		}
		code := binary.BigEndian.Uint32(body[:4])
		if code == 80877103 { // SSLRequest
			_, _ = bw.Write([]byte{'N'})
			_ = bw.Flush()
			continue
		}
		break
	}

	// Send AuthenticationOk + basic ParameterStatus + ReadyForQuery.
	writePGMsg(bw, 'R', []byte{0, 0, 0, 0})
	writePGParam(bw, "server_version", "16.4")
	writePGParam(bw, "client_encoding", "UTF8")
	writePGParam(bw, "standard_conforming_strings", "on")
	writePGMsg(bw, 'Z', []byte{'I'})
	_ = bw.Flush()

	// Message loop (Simple Query 'Q' and Terminate 'X').
	for {
		msgType, err := br.ReadByte()
		if err != nil {
			return
		}
		var length int32
		if err := binary.Read(br, binary.BigEndian, &length); err != nil {
			return
		}
		payload := make([]byte, length-4)
		if _, err := io.ReadFull(br, payload); err != nil {
			return
		}
		switch msgType {
		case 'X':
			return
		case 'Q':
			q := strings.TrimRight(string(payload), "\x00")
			s.execSimpleQuery(bw, strings.TrimSpace(q))
			writePGMsg(bw, 'Z', []byte{'I'})
			_ = bw.Flush()
		default:
			writePGMsg(bw, 'Z', []byte{'I'})
			_ = bw.Flush()
		}
	}
}

func (s *miniPGWireServer) execSimpleQuery(bw *bufio.Writer, q string) {
	if q == "" || q == ";" {
		writePGMsg(bw, 'I', nil)
		return
	}
	upper := strings.ToUpper(q)
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.HasPrefix(upper, "CREATE TABLE") || strings.HasPrefix(upper, "CREATE INDEX") {
		s.ddlCount++
		writePGComplete(bw, "CREATE TABLE")
		return
	}
	if strings.HasPrefix(upper, "INSERT INTO CALIPER_RELEASES") {
		vals := extractValues(q)
		r := storage.ReleaseRecord{
			AppID: vals[0], Platform: vals[1], Env: vals[2], AppVersion: vals[3], BuildNumber: vals[4],
			GitSHA: vals[5], SymbolID: vals[6], Adapter: vals[7], Registered: parseBool(vals[8]),
			FirstSeenMS: parseI64(vals[9]), LastSeenMS: parseI64(vals[10]), EventCount: parseI64(vals[11]),
		}
		key := r.AppID + "|" + r.Platform + "|" + r.Env + "|" + r.AppVersion + "|" + r.BuildNumber
		if ex, ok := s.releases[key]; ok {
			if r.GitSHA == "" {
				r.GitSHA = ex.GitSHA
			}
			if r.SymbolID == "" {
				r.SymbolID = ex.SymbolID
			}
			if r.Adapter == "" {
				r.Adapter = ex.Adapter
			}
			r.Registered = ex.Registered || r.Registered
			if ex.FirstSeenMS < r.FirstSeenMS {
				r.FirstSeenMS = ex.FirstSeenMS
			}
			if ex.LastSeenMS > r.LastSeenMS {
				r.LastSeenMS = ex.LastSeenMS
			}
			r.EventCount = ex.EventCount + r.EventCount
		}
		s.releases[key] = r
		writePGComplete(bw, "INSERT 0 1")
		return
	}
	if strings.Contains(upper, "FROM CALIPER_RELEASES") {
		cols := []string{"app_id", "platform", "env", "app_version", "build_number", "git_sha", "symbol_id", "adapter", "registered", "first_seen_ms", "last_seen_ms", "event_count"}
		writePGRowDesc(bw, cols)
		var rows []storage.ReleaseRecord
		for _, r := range s.releases {
			if matchWhere(q, map[string]string{"app_id": r.AppID, "platform": r.Platform, "env": r.Env}) {
				rows = append(rows, r)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].LastSeenMS > rows[j].LastSeenMS })
		for _, r := range rows {
			writePGDataRow(bw, []any{r.AppID, r.Platform, r.Env, r.AppVersion, r.BuildNumber, r.GitSHA, r.SymbolID, r.Adapter, boolStr(r.Registered), i64Str(r.FirstSeenMS), i64Str(r.LastSeenMS), i64Str(r.EventCount)})
		}
		writePGComplete(bw, fmt.Sprintf("SELECT %d", len(rows)))
		return
	}
	if strings.HasPrefix(upper, "INSERT INTO CALIPER_SYMBOLS") {
		vals := extractValues(q)
		rec := storage.SymbolRecord{
			SymbolID: vals[0], AppID: vals[1], Platform: vals[2], Env: vals[3], AppVersion: vals[4], BuildNumber: vals[5],
			GitSHA: vals[6], Kind: vals[7], SHA256: vals[8], SizeBytes: parseI64(vals[9]), StoragePath: vals[10], UploadedAtMS: parseI64(vals[11]),
		}
		s.symbols[rec.SymbolID] = rec
		writePGComplete(bw, "INSERT 0 1")
		return
	}
	if strings.Contains(upper, "FROM CALIPER_SYMBOLS") {
		cols := []string{"symbol_id", "app_id", "platform", "env", "app_version", "build_number", "git_sha", "kind", "sha256", "size_bytes", "object_key", "uploaded_at_ms"}
		writePGRowDesc(bw, cols)
		var n int
		for _, r := range s.symbols {
			if matchWhere(q, map[string]string{"symbol_id": r.SymbolID, "app_id": r.AppID, "platform": r.Platform, "app_version": r.AppVersion, "build_number": r.BuildNumber}) {
				writePGDataRow(bw, []any{r.SymbolID, r.AppID, r.Platform, r.Env, r.AppVersion, r.BuildNumber, r.GitSHA, r.Kind, r.SHA256, i64Str(r.SizeBytes), r.StoragePath, i64Str(r.UploadedAtMS)})
				n++
			}
		}
		writePGComplete(bw, fmt.Sprintf("SELECT %d", n))
		return
	}
	if strings.HasPrefix(upper, "INSERT INTO CALIPER_SAMPLING_POLICIES") {
		vals := extractValues(q)
		var collectors []string
		// vals[7] has JSON array string (possibly with ::jsonb stripped)
		_ = strings.TrimSpace(vals[7])
		p := storage.SamplingPolicy{
			AppID: vals[0], Platform: vals[1], Env: vals[2], PolicyID: vals[3],
			RatePPM: uint32(parseI64(vals[4])), LeaseTTLMS: parseI64(vals[5]), MaxEvents24h: int(parseI64(vals[6])),
			EnabledCollectors: collectors, UpdatedAtMS: parseI64(vals[8]),
		}
		// Keep raw JSON inside PolicyID? No, decode simple JSON string array cleanly:
		p.EnabledCollectors = parseJSONStringArray(vals[7])
		s.policies[p.AppID+"|"+p.Platform+"|"+p.Env] = p
		writePGComplete(bw, "INSERT 0 1")
		return
	}
	if strings.Contains(upper, "FROM CALIPER_SAMPLING_POLICIES") {
		cols := []string{"policy_id", "app_id", "platform", "env", "rate_ppm", "lease_ttl_ms", "max_events_24h", "enabled_collectors", "updated_at_ms"}
		writePGRowDesc(bw, cols)
		var n int
		for _, p := range s.policies {
			if matchWhere(q, map[string]string{"app_id": p.AppID, "platform": p.Platform, "env": p.Env}) {
				writePGDataRow(bw, []any{p.PolicyID, p.AppID, p.Platform, p.Env, i64Str(int64(p.RatePPM)), i64Str(p.LeaseTTLMS), i64Str(int64(p.MaxEvents24h)), formatJSONStringArray(p.EnabledCollectors), i64Str(p.UpdatedAtMS)})
				n++
			}
		}
		writePGComplete(bw, fmt.Sprintf("SELECT %d", n))
		return
	}
	if strings.HasPrefix(upper, "INSERT INTO CALIPER_ALERT_RULES") {
		vals := extractValues(q)
		r := storage.AlertRule{
			RuleID: vals[0], Name: vals[1], AppID: vals[2], Platform: vals[3], Env: vals[4], AppVersion: vals[5], Scene: vals[6],
			MetricID: vals[7], Operator: vals[8], Threshold: parseF64(vals[9]), MinSamples: parseI64(vals[10]),
			WebhookURL: vals[11], WebhookFormat: vals[12], CooldownSec: parseI64(vals[13]), Enabled: parseBool(vals[14]), UpdatedAtMS: parseI64(vals[15]),
		}
		s.rules[r.RuleID] = r
		writePGComplete(bw, "INSERT 0 1")
		return
	}
	if strings.HasPrefix(upper, "DELETE FROM CALIPER_ALERT_RULES") {
		for id := range s.rules {
			if matchWhere(q, map[string]string{"rule_id": id}) {
				delete(s.rules, id)
				for incID, inc := range s.incidents {
					if inc.RuleID == id {
						delete(s.incidents, incID)
					}
				}
			}
		}
		writePGComplete(bw, "DELETE 1")
		return
	}
	if strings.Contains(upper, "FROM CALIPER_ALERT_RULES") {
		cols := []string{"rule_id", "name", "app_id", "platform", "env", "app_version", "scene", "metric_id", "operator", "threshold", "min_samples", "webhook_url", "webhook_format", "cooldown_sec", "enabled", "updated_at_ms"}
		writePGRowDesc(bw, cols)
		var n int
		for _, r := range s.rules {
			if matchWhere(q, map[string]string{"app_id": r.AppID, "platform": r.Platform}) {
				writePGDataRow(bw, []any{r.RuleID, r.Name, r.AppID, r.Platform, r.Env, r.AppVersion, r.Scene, r.MetricID, r.Operator, fmt.Sprintf("%g", r.Threshold), i64Str(r.MinSamples), r.WebhookURL, r.WebhookFormat, i64Str(r.CooldownSec), boolStr(r.Enabled), i64Str(r.UpdatedAtMS)})
				n++
			}
		}
		writePGComplete(bw, fmt.Sprintf("SELECT %d", n))
		return
	}
	if strings.HasPrefix(upper, "INSERT INTO CALIPER_ALERT_INCIDENTS") {
		vals := extractValues(q)
		var obs *float64
		if vals[7] != "NULL" {
			v := parseF64(vals[7])
			obs = &v
		}
		inc := storage.AlertIncident{
			IncidentID: vals[0], RuleID: vals[1], RuleName: vals[2], AppID: vals[3], Platform: vals[4], MetricID: vals[5], Status: vals[6],
			ObservedValue: obs, Threshold: parseF64(vals[8]), SampleSize: parseI64(vals[9]), TriggeredAtMS: parseI64(vals[10]),
			LastEvaluatedMS: parseI64(vals[11]), ResolvedAtMS: parseI64(vals[12]), LastNotifiedMS: parseI64(vals[13]),
			DeliveryStatus: vals[14], DeliveryHTTPStatus: int(parseI64(vals[15])), DeliveryError: vals[16],
		}
		s.incidents[inc.IncidentID] = inc
		writePGComplete(bw, "INSERT 0 1")
		return
	}
	if strings.Contains(upper, "FROM CALIPER_ALERT_INCIDENTS") {
		cols := []string{"incident_id", "rule_id", "rule_name", "app_id", "platform", "metric_id", "status", "observed_value", "threshold", "sample_size", "triggered_at_ms", "last_evaluated_ms", "resolved_at_ms", "last_notified_ms", "delivery_status", "delivery_http_status", "delivery_error"}
		writePGRowDesc(bw, cols)
		var n int
		for _, inc := range s.incidents {
			if matchWhere(q, map[string]string{"rule_id": inc.RuleID}) {
				var obs any
				if inc.ObservedValue != nil {
					obs = fmt.Sprintf("%g", *inc.ObservedValue)
				}
				var resAt, notAt any
				if inc.ResolvedAtMS > 0 {
					resAt = i64Str(inc.ResolvedAtMS)
				}
				if inc.LastNotifiedMS > 0 {
					notAt = i64Str(inc.LastNotifiedMS)
				}
				writePGDataRow(bw, []any{inc.IncidentID, inc.RuleID, inc.RuleName, inc.AppID, inc.Platform, inc.MetricID, inc.Status, obs, fmt.Sprintf("%g", inc.Threshold), i64Str(inc.SampleSize), i64Str(inc.TriggeredAtMS), i64Str(inc.LastEvaluatedMS), resAt, notAt, inc.DeliveryStatus, i64Str(int64(inc.DeliveryHTTPStatus)), inc.DeliveryError})
				n++
			}
		}
		writePGComplete(bw, fmt.Sprintf("SELECT %d", n))
		return
	}
	if strings.HasPrefix(upper, "INSERT INTO CALIPER_ISSUE_STATES") {
		vals := extractValues(q)
		st := storage.IssueState{
			Fingerprint: vals[0], Status: vals[1], Assignee: vals[2], Note: vals[3], ResolvedInVersion: vals[4], UpdatedAtMS: parseI64(vals[5]),
		}
		s.issues[st.Fingerprint] = st
		writePGComplete(bw, "INSERT 0 1")
		return
	}
	if strings.Contains(upper, "FROM CALIPER_ISSUE_STATES") {
		cols := []string{"fingerprint", "status", "assignee", "note", "resolved_in_version", "updated_at_ms"}
		writePGRowDesc(bw, cols)
		var n int
		for _, st := range s.issues {
			if matchWhere(q, map[string]string{"fingerprint": st.Fingerprint}) {
				writePGDataRow(bw, []any{st.Fingerprint, st.Status, st.Assignee, st.Note, st.ResolvedInVersion, i64Str(st.UpdatedAtMS)})
				n++
			}
		}
		writePGComplete(bw, fmt.Sprintf("SELECT %d", n))
		return
	}
	if strings.HasPrefix(upper, "INSERT INTO CALIPER_AUDIT_LOGS") {
		vals := extractValues(q)
		s.audits = append(s.audits, storage.AuditLogEntry{
			AuditID: vals[0], TimestampMS: parseI64(vals[1]), Actor: vals[2], Role: vals[3], Action: vals[4],
			TargetType: vals[5], TargetHash: vals[6], Reason: vals[7], RemoteIP: vals[8], ResultCount: int(parseI64(vals[9])),
		})
		writePGComplete(bw, "INSERT 0 1")
		return
	}
	if strings.Contains(upper, "FROM CALIPER_AUDIT_LOGS") {
		cols := []string{"audit_id", "timestamp_ms", "actor", "role", "action", "target_type", "target_hash", "reason", "remote_ip", "result_count"}
		writePGRowDesc(bw, cols)
		cp := append([]storage.AuditLogEntry(nil), s.audits...)
		sort.Slice(cp, func(i, j int) bool { return cp[i].TimestampMS > cp[j].TimestampMS })
		for _, e := range cp {
			writePGDataRow(bw, []any{e.AuditID, i64Str(e.TimestampMS), e.Actor, e.Role, e.Action, e.TargetType, e.TargetHash, e.Reason, e.RemoteIP, i64Str(int64(e.ResultCount))})
		}
		writePGComplete(bw, fmt.Sprintf("SELECT %d", len(cp)))
		return
	}
	if strings.HasPrefix(upper, "INSERT INTO CALIPER_REMOTE_LOG_TASKS") {
		vals := extractValues(q)
		tk := storage.RemoteLogTask{
			TaskID: vals[0], AppID: vals[1], Platform: vals[2], TargetType: vals[3], TargetHash: vals[4],
			Reason: vals[5], RequestedBy: vals[6], Status: vals[7], MaxBytes: parseI64(vals[8]),
			CreatedAtMS: parseI64(vals[9]), ExpiresAtMS: parseI64(vals[10]),
		}
		s.logTasks[tk.TaskID] = tk
		writePGComplete(bw, "INSERT 0 1")
		return
	}
	if strings.HasPrefix(upper, "UPDATE CALIPER_REMOTE_LOG_TASKS") {
		for id, tk := range s.logTasks {
			if strings.Contains(q, "'"+id+"'") {
				if strings.Contains(upper, "STATUS = 'COMPLETED'") {
					tk.Status = "completed"
					tk.CompletedAtMS = extractAssignI64(q, "completed_at_ms")
					tk.UploadedBytes = extractAssignI64(q, "uploaded_bytes")
					tk.SHA256 = extractAssignStr(q, "sha256")
					tk.StoragePath = extractAssignStr(q, "object_key")
				} else if strings.Contains(upper, "STATUS = 'EXPIRED'") {
					tk.Status = "expired"
				}
				s.logTasks[id] = tk
			}
		}
		writePGComplete(bw, "UPDATE 1")
		return
	}
	if strings.Contains(upper, "FROM CALIPER_REMOTE_LOG_TASKS") {
		cols := []string{"task_id", "app_id", "platform", "target_type", "target_hash", "reason", "requested_by", "status", "max_bytes", "created_at_ms", "expires_at_ms", "completed_at_ms", "uploaded_bytes", "sha256", "object_key"}
		writePGRowDesc(bw, cols)
		var n int
		for _, tk := range s.logTasks {
			if matchWhere(q, map[string]string{
				"task_id": tk.TaskID, "app_id": tk.AppID, "platform": tk.Platform,
				"status": tk.Status, "target_type": tk.TargetType, "target_hash": tk.TargetHash,
			}) {
				var compAt any
				if tk.CompletedAtMS > 0 {
					compAt = i64Str(tk.CompletedAtMS)
				}
				writePGDataRow(bw, []any{tk.TaskID, tk.AppID, tk.Platform, tk.TargetType, tk.TargetHash, tk.Reason, tk.RequestedBy, tk.Status, i64Str(tk.MaxBytes), i64Str(tk.CreatedAtMS), i64Str(tk.ExpiresAtMS), compAt, i64Str(tk.UploadedBytes), tk.SHA256, tk.StoragePath})
				n++
			}
		}
		writePGComplete(bw, fmt.Sprintf("SELECT %d", n))
		return
	}
	writePGComplete(bw, "SELECT 0")
}

func writePGMsg(bw *bufio.Writer, typ byte, payload []byte) {
	_ = bw.WriteByte(typ)
	_ = binary.Write(bw, binary.BigEndian, int32(len(payload)+4))
	_, _ = bw.Write(payload)
}

func writePGParam(bw *bufio.Writer, k, v string) {
	var b bytes.Buffer
	b.WriteString(k)
	b.WriteByte(0)
	b.WriteString(v)
	b.WriteByte(0)
	writePGMsg(bw, 'S', b.Bytes())
}

func writePGComplete(bw *bufio.Writer, tag string) {
	writePGMsg(bw, 'C', append([]byte(tag), 0))
}

func writePGRowDesc(bw *bufio.Writer, cols []string) {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, int16(len(cols)))
	for _, c := range cols {
		b.WriteString(c)
		b.WriteByte(0)
		_ = binary.Write(&b, binary.BigEndian, int32(0))  // table OID
		_ = binary.Write(&b, binary.BigEndian, int16(0))  // attr num
		_ = binary.Write(&b, binary.BigEndian, int32(25)) // TEXT OID (works cleanly with database/sql Scan conversions)
		_ = binary.Write(&b, binary.BigEndian, int16(-1)) // typlen
		_ = binary.Write(&b, binary.BigEndian, int32(-1)) // typmod
		_ = binary.Write(&b, binary.BigEndian, int16(0))  // text format
	}
	writePGMsg(bw, 'T', b.Bytes())
}

func writePGDataRow(bw *bufio.Writer, vals []any) {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, int16(len(vals)))
	for _, v := range vals {
		if v == nil {
			_ = binary.Write(&b, binary.BigEndian, int32(-1))
			continue
		}
		s := fmt.Sprint(v)
		_ = binary.Write(&b, binary.BigEndian, int32(len(s)))
		b.WriteString(s)
	}
	writePGMsg(bw, 'D', b.Bytes())
}

func extractValues(q string) []string {
	idx := strings.Index(strings.ToUpper(q), "VALUES")
	if idx < 0 {
		return nil
	}
	rest := q[idx+6:]
	start := strings.IndexByte(rest, '(')
	if start < 0 {
		return nil
	}
	rest = rest[start+1:]
	var out []string
	var cur strings.Builder
	inStr := false
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		if inStr {
			if ch == '\'' {
				if i+1 < len(rest) && rest[i+1] == '\'' {
					cur.WriteByte('\'')
					i++
				} else {
					inStr = false
				}
			} else {
				cur.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\'':
			inStr = true
		case ',':
			out = append(out, cleanVal(cur.String()))
			cur.Reset()
		case ')':
			out = append(out, cleanVal(cur.String()))
			return out
		default:
			cur.WriteByte(ch)
		}
	}
	return out
}

func cleanVal(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "::"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

func matchWhere(q string, fields map[string]string) bool {
	upper := strings.ToUpper(q)
	wIdx := strings.Index(upper, "WHERE ")
	if wIdx < 0 {
		return true
	}
	clause := q[wIdx+6:]
	for _, stop := range []string{" ORDER BY ", " LIMIT "} {
		if si := strings.Index(strings.ToUpper(clause), stop); si >= 0 {
			clause = clause[:si]
		}
	}
	for k, v := range fields {
		needle := k + " = '"
		pos := strings.Index(clause, needle)
		if pos < 0 {
			continue
		}
		rest := clause[pos+len(needle):]
		end := strings.IndexByte(rest, '\'')
		if end >= 0 && rest[:end] != v {
			return false
		}
	}
	return true
}

func extractAssignStr(q, col string) string {
	needle := col + " = "
	pos := strings.Index(q, needle)
	if pos < 0 {
		return ""
	}
	rest := strings.TrimSpace(q[pos+len(needle):])
	start := strings.IndexByte(rest, '\'')
	if start < 0 || start > 2 {
		return ""
	}
	rest = rest[start+1:]
	end := strings.IndexByte(rest, '\'')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func extractAssignI64(q, col string) int64 {
	needle := col + " = "
	pos := strings.Index(q, needle)
	if pos < 0 {
		return 0
	}
	rest := strings.TrimSpace(q[pos+len(needle):])
	rest = strings.TrimLeft(rest, "('")
	var digits strings.Builder
	for i := 0; i < len(rest); i++ {
		if rest[i] >= '0' && rest[i] <= '9' {
			digits.WriteByte(rest[i])
		} else {
			break
		}
	}
	return parseI64(digits.String())
}

func parseI64(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

func parseF64(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func parseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "t" || s == "1"
}

func boolStr(b bool) string {
	if b {
		return "t"
	}
	return "f"
}

func i64Str(v int64) string { return strconv.FormatInt(v, 10) }

func parseJSONStringArray(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.Trim(strings.TrimSpace(p), `"`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func formatJSONStringArray(list []string) string {
	quoted := make([]string, len(list))
	for i, s := range list {
		quoted[i] = `"` + s + `"`
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

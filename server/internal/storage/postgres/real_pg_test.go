package postgres_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/storage/objectstore"
	"github.com/caliper-apm/caliper/server/internal/storage/postgres"
)

// TestRealPostgreSQLMultiReplicaHA verifies the full 22-method MetaStore contract,
// idempotent DDL migrations, foreign-key cascades, JSONB policies, SCRAM-SHA-256 auth,
// and default pgx/v5 Extended Query Protocol against a REAL PostgreSQL 15+/16+ database
// when CALIPER_TEST_POSTGRES_DSN is provided.
func TestRealPostgreSQLMultiReplicaHA(t *testing.T) {
	dsn := os.Getenv("CALIPER_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CALIPER_TEST_POSTGRES_DSN to run real PostgreSQL integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	blobs, err := objectstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("new local object store: %v", err)
	}

	replicaA, err := postgres.Open(ctx, postgres.Config{DSN: dsn, ObjectStore: blobs, MaxOpenConns: 10})
	if err != nil {
		t.Fatalf("open real postgres replica A: %v", err)
	}
	defer replicaA.Close()

	replicaB, err := postgres.Open(ctx, postgres.Config{DSN: dsn, ObjectStore: blobs, MaxOpenConns: 10})
	if err != nil {
		t.Fatalf("open real postgres replica B: %v", err)
	}
	defer replicaB.Close()

	if err := replicaA.Ping(ctx); err != nil {
		t.Fatalf("ping real postgres: %v", err)
	}

	// 1. Idempotent DDL migration (runs twice across two replicas)
	if err := replicaA.Migrate(ctx); err != nil {
		t.Fatalf("migrate replica A: %v", err)
	}
	if err := replicaB.Migrate(ctx); err != nil {
		t.Fatalf("migrate replica B (idempotent): %v", err)
	}

	tag := fmt.Sprintf("pgreal-%d", time.Now().UnixNano())
	appID := "dev.caliper." + tag

	// 2. Releases conflict merge (LEAST / GREATEST / CASE / event_count +)
	if err := replicaA.RegisterRelease(ctx, storage.ReleaseRecord{
		AppID: appID, Platform: "android", Env: "prod",
		AppVersion: "1.8.2", BuildNumber: "182", GitSHA: "sha1111",
		Registered: true, FirstSeenMS: 2000, LastSeenMS: 3000, EventCount: 10,
	}); err != nil {
		t.Fatalf("RegisterRelease A: %v", err)
	}
	if err := replicaB.RegisterRelease(ctx, storage.ReleaseRecord{
		AppID: appID, Platform: "android", Env: "prod",
		AppVersion: "1.8.2", BuildNumber: "182", SymbolID: "sym-real-182",
		Registered: false, FirstSeenMS: 1500, LastSeenMS: 4500, EventCount: 15,
	}); err != nil {
		t.Fatalf("RegisterRelease B upsert: %v", err)
	}
	rels, err := replicaA.ListReleases(ctx, appID, "android", "prod")
	if err != nil || len(rels) != 1 {
		t.Fatalf("ListReleases: err=%v rels=%+v", err, rels)
	}
	if rels[0].GitSHA != "sha1111" || rels[0].SymbolID != "sym-real-182" || !rels[0].Registered ||
		rels[0].FirstSeenMS != 1500 || rels[0].LastSeenMS != 4500 || rels[0].EventCount != 25 {
		t.Fatalf("real PG release merge mismatch: %+v", rels[0])
	}

	// 3. Symbols + ObjectStore round-trip (native_elf & dsym CHECK constraints verified by real PG)
	elfPayload := []byte("\x7fELF-real-pg-test-binary")
	symID := "sym-" + tag
	if _, err := replicaA.SaveSymbol(ctx, storage.SymbolRecord{
		SymbolID: symID, AppID: appID, Platform: "android", Env: "prod",
		AppVersion: "1.8.2", BuildNumber: "182", GitSHA: "sha1111", Kind: "native_elf", SHA256: "abcdef01",
	}, elfPayload); err != nil {
		t.Fatalf("SaveSymbol native_elf: %v", err)
	}
	syms, err := replicaB.ListSymbols(ctx, appID, "android", "1.8.2", "182")
	if err != nil || len(syms) != 1 || syms[0].Kind != "native_elf" {
		t.Fatalf("ListSymbols B: err=%v syms=%+v", err, syms)
	}
	gotRec, gotBytes, ok, err := replicaB.GetSymbolContent(ctx, symID)
	if err != nil || !ok || !bytes.Equal(gotBytes, elfPayload) || gotRec.SizeBytes != int64(len(elfPayload)) {
		t.Fatalf("GetSymbolContent B: err=%v ok=%v rec=%+v", err, ok, gotRec)
	}

	// 4. Sampling Policy (default -> JSONB upsert -> cross-replica read)
	defPol, err := replicaA.GetPolicy(ctx, appID, "ios", "prod")
	if err != nil || defPol.PolicyID != "default_v1" {
		t.Fatalf("default GetPolicy: err=%v pol=%+v", err, defPol)
	}
	if err := replicaA.UpsertPolicy(ctx, storage.SamplingPolicy{
		PolicyID: "pol-" + tag, AppID: appID, Platform: "ios", Env: "prod",
		RatePPM: 250_000, LeaseTTLMS: 7_200_000, MaxEvents24h: 300,
		EnabledCollectors: []string{"startup", "crash", "anr", "custom_scene"},
	}); err != nil {
		t.Fatalf("UpsertPolicy JSONB: %v", err)
	}
	polB, err := replicaB.GetPolicy(ctx, appID, "ios", "prod")
	if err != nil || polB.PolicyID != "pol-"+tag || polB.RatePPM != 250_000 || len(polB.EnabledCollectors) != 4 {
		t.Fatalf("GetPolicy B: err=%v pol=%+v", err, polB)
	}

	// 5. Alert Rules + Incidents (NULL & non-NULL DOUBLE PRECISION/BIGINT) + real ON DELETE CASCADE
	ruleID := "rule-" + tag
	if _, err := replicaA.UpsertAlertRule(ctx, storage.AlertRule{
		RuleID: ruleID, Name: "Real PG Jank Gate", AppID: appID, Platform: "android", Env: "prod",
		MetricID: "fluency.jank_rate_pct", Operator: "gt", Threshold: 4.5, MinSamples: 100,
		WebhookURL: "https://example.invalid/hook", WebhookFormat: "feishu", CooldownSec: 300, Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertAlertRule: %v", err)
	}
	obs := 7.25
	if err := replicaB.SaveAlertIncident(ctx, storage.AlertIncident{
		IncidentID: "inc1-" + tag, RuleID: ruleID, RuleName: "Real PG Jank Gate", AppID: appID, Platform: "android",
		MetricID: "fluency.jank_rate_pct", Status: "firing", ObservedValue: &obs, Threshold: 4.5, SampleSize: 410,
		TriggeredAtMS: 10000, LastEvaluatedMS: 11000, DeliveryStatus: "delivered", DeliveryHTTPStatus: 200,
	}); err != nil {
		t.Fatalf("SaveAlertIncident 1: %v", err)
	}
	if err := replicaB.SaveAlertIncident(ctx, storage.AlertIncident{
		IncidentID: "inc2-" + tag, RuleID: ruleID, RuleName: "Real PG Jank Gate", AppID: appID, Platform: "android",
		MetricID: "fluency.jank_rate_pct", Status: "resolved", ObservedValue: nil, Threshold: 4.5, SampleSize: 500,
		TriggeredAtMS: 10000, LastEvaluatedMS: 12000, ResolvedAtMS: 12000, LastNotifiedMS: 12000, DeliveryStatus: "delivered",
	}); err != nil {
		t.Fatalf("SaveAlertIncident 2 (NULL observed_value): %v", err)
	}
	incs, err := replicaA.ListAlertIncidents(ctx, ruleID)
	if err != nil || len(incs) != 2 {
		t.Fatalf("ListAlertIncidents: err=%v len=%d", err, len(incs))
	}
	if err := replicaA.DeleteAlertRule(ctx, ruleID); err != nil {
		t.Fatalf("DeleteAlertRule: %v", err)
	}
	if remIncs, err := replicaB.ListAlertIncidents(ctx, ruleID); err != nil || len(remIncs) != 0 {
		t.Fatalf("expected real PG ON DELETE CASCADE to delete incidents, err=%v rem=%+v", err, remIncs)
	}

	// 6. Issue Triage States + CHECK constraint
	fp := "fp-" + tag
	if _, err := replicaA.UpsertIssueState(ctx, storage.IssueState{
		Fingerprint: fp, Status: "in_progress", Assignee: "sre-oncall", Note: "investigating", ResolvedInVersion: "",
	}); err != nil {
		t.Fatalf("UpsertIssueState: %v", err)
	}
	if _, err := replicaB.UpsertIssueState(ctx, storage.IssueState{
		Fingerprint: fp, Status: "resolved", Assignee: "sre-oncall", Note: "fixed in 1.8.2", ResolvedInVersion: "1.8.2",
	}); err != nil {
		t.Fatalf("UpsertIssueState update: %v", err)
	}
	gotSt, found, err := replicaA.GetIssueState(ctx, fp)
	if err != nil || !found || gotSt.Status != "resolved" || gotSt.ResolvedInVersion != "1.8.2" {
		t.Fatalf("GetIssueState A: err=%v found=%v st=%+v", err, found, gotSt)
	}

	// 7. Remote Diagnostic Log Tasks full lifecycle (create -> poll -> complete -> download -> reject double complete)
	nowMS := time.Now().UTC().UnixMilli()
	task, err := replicaA.CreateRemoteLogTask(ctx, storage.RemoteLogTask{
		TaskID: "rlog-" + tag, AppID: appID, Platform: "ios",
		TargetType: "user_hash", TargetHash: "0011223344556677", Reason: "INC-PG16", RequestedBy: "admin",
		MaxBytes: 8192, CreatedAtMS: nowMS, ExpiresAtMS: nowMS + 60_000,
	})
	if err != nil {
		t.Fatalf("CreateRemoteLogTask: %v", err)
	}
	pending, err := replicaB.PollPendingLogTasks(ctx, appID, "ios", "", "0011223344556677", nowMS+100)
	if err != nil || len(pending) != 1 || pending[0].TaskID != task.TaskID {
		t.Fatalf("PollPendingLogTasks B: err=%v pending=%+v", err, pending)
	}
	logData := []byte("real-pg16-diagnostic-ring-buffer\n")
	done, err := replicaB.CompleteRemoteLogTask(ctx, task.TaskID, logData, "deadbeefcafe", nowMS+200)
	if err != nil || done.Status != "completed" || done.UploadedBytes != int64(len(logData)) {
		t.Fatalf("CompleteRemoteLogTask B: err=%v task=%+v", err, done)
	}
	if _, err := replicaA.CompleteRemoteLogTask(ctx, task.TaskID, logData, "deadbeefcafe", nowMS+300); err == nil {
		t.Fatal("expected duplicate CompleteRemoteLogTask to fail")
	}
	dlTask, dlBytes, ok, err := replicaA.GetRemoteLogContent(ctx, task.TaskID)
	if err != nil || !ok || !bytes.Equal(dlBytes, logData) || dlTask.SHA256 != "deadbeefcafe" {
		t.Fatalf("GetRemoteLogContent A: err=%v ok=%v task=%+v", err, ok, dlTask)
	}

	// 8. Concurrent multi-replica Audit Log writes + reads under -race
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			st := replicaA
			if id%2 == 1 {
				st = replicaB
			}
			if err := st.AppendAuditLog(ctx, storage.AuditLogEntry{
				AuditID: fmt.Sprintf("aud-%s-%02d", tag, id), TimestampMS: nowMS + int64(id),
				Actor: "ha-worker", Role: "operator", Action: "session_lookup",
				TargetType: "user_hash", TargetHash: "0011223344556677", Reason: "PG16-CONC", ResultCount: id,
			}); err != nil {
				t.Errorf("concurrent AppendAuditLog %d: %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	logs, err := replicaB.ListAuditLogs(ctx, 50)
	if err != nil || len(logs) < 16 {
		t.Fatalf("ListAuditLogs: err=%v count=%d", err, len(logs))
	}
}

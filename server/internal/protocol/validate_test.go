package protocol

import (
	"encoding/json"
	"testing"
)

func TestValidateEnvelopeAndPayloads(t *testing.T) {
	validBatch := BatchEnvelope{
		SchemaVersion: 1,
		SentAtMS:      1763540000000,
		Resource: Resource{
			AppID:       "dev.caliper.demo",
			Platform:    "android",
			Env:         "prod",
			AppVersion:  "1.0.0",
			BuildNumber: "100",
			GitSHA:      "abcdef1234567",
			Adapter:     "native",
			SDKName:     "caliper-android",
			SDKVersion:  "0.1.0",
			InstallHash: "0123456789abcdef",
		},
		Events: []Event{
			{
				EventID:         "evt-startup-0001",
				Kind:            KindStartup,
				OccurredAtMS:    1763539990000,
				ContractVersion: 1,
				Payload:         json.RawMessage(`{"total_duration_ms":650,"native_init_ms":180,"runtime_init_ms":210,"first_frame_ms":140,"cold_start":true,"start_anchor":"application_on_create","end_anchor":"first_content_frame","result":"success"}`),
			},
			{
				EventID:         "evt-fluency-0001",
				Kind:            KindFluencyWindow,
				OccurredAtMS:    1763539991000,
				ContractVersion: 1,
				Scene:           "home_feed",
				Payload:         json.RawMessage(`{"algorithm":"android_frame_metrics_deadline","window_duration_ms":30000,"frame_count":0,"jank_frame_count":0}`),
			},
		},
	}

	if err := validBatch.Validate(); err != nil {
		t.Fatalf("valid batch failed: %v", err)
	}
	for i := range validBatch.Events {
		if _, err := ValidateEvent(&validBatch.Events[i]); err != nil {
			t.Fatalf("event %d failed validation: %v", i, err)
		}
	}

	// Stage conservation violation must be rejected.
	badStartup := Event{
		EventID:         "evt-bad-startup",
		Kind:            KindStartup,
		OccurredAtMS:    1763539990000,
		ContractVersion: 1,
		Payload:         json.RawMessage(`{"total_duration_ms":300,"native_init_ms":200,"first_frame_ms":200,"cold_start":true,"start_anchor":"application_on_create","end_anchor":"first_content_frame","result":"success"}`),
	}
	if _, err := ValidateEvent(&badStartup); err == nil {
		t.Fatal("expected stage conservation violation error")
	}

	// Network bucket conservation mismatch must be rejected.
	badNet := Event{
		EventID:         "evt-bad-net-001",
		Kind:            KindNetworkSummary,
		OccurredAtMS:    1763539990000,
		ContractVersion: 1,
		Payload:         json.RawMessage(`{"window_duration_ms":30000,"call_count":10,"failure_counts":{"http":1},"duration_buckets":[1,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],"duration_sum_ms":100}`),
	}
	if _, err := ValidateEvent(&badNet); err == nil {
		t.Fatal("expected bucket conservation mismatch error")
	}
}

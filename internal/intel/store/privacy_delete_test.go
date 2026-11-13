package store

import (
	"context"
	"database/sql"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestPrivacyPurgeFencesLateWritesAndPreservesSharedIP(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "orphan", true: "shared"}[shared], func(t *testing.T) {
			s := openTemp(t)
			ctx := context.Background()
			h := "bad"
			ip := "203.0.113.10"
			if _, _, err := s.RecordEgress(ctx, EgressObservation{NodeHash: h, IPv4: netip.MustParseAddr(ip), NowNs: 1}); err != nil {
				t.Fatal(err)
			}
			if shared {
				if _, _, err := s.RecordEgress(ctx, EgressObservation{NodeHash: "good", IPv4: netip.MustParseAddr(ip), NowNs: 1}); err != nil {
					t.Fatal(err)
				}
			}
			row := Assessment{IP: ip, State: "valid", PurityScore: sql.NullInt64{Int64: 23, Valid: true}, ReasonsJSON: "[]", ComponentsJSON: "[]", ValidUntilNs: time.Now().Add(time.Hour).UnixNano()}
			if err := s.UpsertAssessment(ctx, row); err != nil {
				t.Fatal(err)
			}
			job := Job{ID: "job", Kind: "full", Status: JobQueued, RequestJSON: `{"scope":{"node_hashes":["bad","good"]}}`, CreatedBy: "fixture", CreatedAtNs: 1}
			if err := s.CreateJob(ctx, job, []JobItem{{NodeHash: h, Status: ItemQueued, NextRunAtNs: 1, UpdatedAtNs: 1}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`INSERT INTO provider_node_state(provider,node_hash,day) VALUES('fixture',?,'2026-11-05')`, h); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`INSERT INTO provider_queue(provider,ip,priority,status,next_run_at_ns,enqueued_at_ns) VALUES('fixture',?,1,'queued',1,1)`, ip); err != nil {
				t.Fatal(err)
			}
			ips, err := s.PurgePrivacyNode(ctx, h)
			if err != nil {
				t.Fatal(err)
			}
			if (len(ips) == 0) != shared {
				t.Fatal(ips)
			}
			// Every ordinary writer and even a newly-created job is fenced.
			_ = s.UpsertNodeEgress(ctx, NodeEgress{NodeHash: h, IPv4: ip})
			_ = s.UpsertAssessment(ctx, row)
			job.ID = "late"
			if err := s.CreateJob(ctx, job, []JobItem{{NodeHash: h, Status: ItemQueued, UpdatedAtNs: 2}}); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"node_egress", "egress_history", "node_checks", "job_items", "node_measurements", "provider_node_state"} {
				var n int
				if err := s.DB().QueryRow(`SELECT count(*) FROM `+table+` WHERE node_hash=?`, h).Scan(&n); err != nil || n != 0 {
					t.Fatal(table, n, err)
				}
			}
			var n int
			_ = s.DB().QueryRow(`SELECT count(*) FROM ip_assessment WHERE ip=?`, ip).Scan(&n)
			if (n == 1) != shared {
				t.Fatal("shared assessment lost / orphan resurrected", n)
			}
			_ = s.DB().QueryRow(`SELECT count(*) FROM provider_queue WHERE ip=?`, ip).Scan(&n)
			if (n == 1) != shared {
				t.Fatal("orphan queue retained / shared queue lost", n)
			}
			rows, err := s.DB().Query(`SELECT request_json FROM jobs`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var raw string
				_ = rows.Scan(&raw)
				if strings.Contains(raw, `"bad"`) {
					t.Fatal("job retained removed hash", raw)
				}
			}
		})
	}
}

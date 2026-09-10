package billing

import (
	"strconv"
	"testing"
	"time"
)

func TestIndependentQuotaWindows(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{{ID: "p", Windows: []QuotaWindow{
			{ID: "short", Name: "短时", AmountUSD: wantSubsetCost, PeriodSeconds: 3600},
			{ID: "long", Name: "预算", AmountUSD: wantSubsetCost, PeriodSeconds: 7200},
		}}}
		state.Keys["s"] = &KeyState{PlanID: "p"}
	})
	for _, scope := range []string{"", "unknown"} {
		if !store.Authorize(scope, now).Allowed {
			t.Fatal("unknown key blocked")
		}
	}
	view, _ := store.KeyViewForScope("s")
	if view.Windows[0].Started || view.Windows[1].Started {
		t.Fatal("reading quota started cycles")
	}
	first := store.Authorize("s", now)
	if !first.Allowed || !first.Windows[0].StartAt.Equal(first.Windows[1].StartAt) {
		t.Fatalf("first admission: %+v", first)
	}
	store.RecordUsage(subsetEvent("s", now))
	blocked := store.Authorize("s", now)
	if blocked.Allowed || !blocked.RetryAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("blocked = %+v", blocked)
	}
	for _, d := range blocked.Windows {
		used, _ := d.Dimensions[0].Used.Float64()
		assertClose(t, "window cost", used, wantSubsetCost)
	}
	if rows := mustRequestEvents(t, store, RequestEventQuery{}).Entries; len(rows) != 1 || rows[0].Cost.TotalUSD != wantSubsetCost {
		t.Fatalf("duplicated history: %+v", rows)
	}
	store.now = func() time.Time { return now.Add(time.Hour) }
	views := store.KeyViews()
	if len(views) != 1 || views[0].Windows[0].Started || !views[0].Windows[1].Blocked {
		t.Fatalf("reading quota after expiration: %+v", views)
	}
	blocked = store.Authorize("s", now.Add(time.Hour))
	if blocked.Allowed || blocked.Windows[0].Started || !blocked.Windows[1].Blocked {
		t.Fatalf("expired short window restarted while blocked: %+v", blocked)
	}
	next := store.Authorize("s", now.Add(10*time.Hour))
	if !next.Allowed || !next.Windows[0].StartAt.Equal(now.Add(10*time.Hour)) || next.Windows[1].Dimensions[0].Used != "0" {
		t.Fatalf("idle restart: %+v", next)
	}
}

func TestQuotaWindowsWithDifferentDimensions(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := newAccountStore(t, now)
	plan := Plan{ID: "p", Windows: []QuotaWindow{
		{ID: "requests", Name: "请求", RequestLimit: 1, PeriodSeconds: 3600},
		{ID: "tokens", Name: "Token", TokenLimit: 3000, PeriodSeconds: 7200},
		{ID: "amount", Name: "金额", AmountUSD: 4 * wantSubsetCost, PeriodSeconds: 86400},
	}}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{plan}
		state.Keys["s"] = &KeyState{PlanID: plan.ID}
	})
	for hour := range 4 {
		at := now.Add(time.Duration(hour) * time.Hour)
		store.now = func() time.Time { return at }
		before := store.Authorize("s", at)
		if !before.Allowed || before.Windows[0].Dimensions[0].Used != "0" || before.Windows[1].Dimensions[0].Used.String() != strconv.Itoa((hour%2)*1500) {
			t.Fatalf("hour %d: windows did not reset independently: %+v", hour, before)
		}
		spent, _ := before.Windows[2].Dimensions[0].Used.Float64()
		assertClose(t, "amount carried across shorter windows", spent, float64(hour)*wantSubsetCost)
		store.RecordUsage(subsetEvent("s", at))
		after := store.Authorize("s", at)
		if after.Allowed || !after.Windows[0].Blocked || after.Windows[1].Blocked != (hour%2 == 1) || after.Windows[2].Blocked != (hour == 3) {
			t.Fatalf("hour %d: dimension enforcement interfered: %+v", hour, after)
		}
		reset := at.Add(time.Hour)
		if hour == 3 {
			reset = now.Add(24 * time.Hour)
		}
		if !after.RetryAt.Equal(reset) {
			t.Fatalf("hour %d: retry at %v, want %v", hour, after.RetryAt, reset)
		}
	}
	store.now = func() time.Time { return now.Add(4 * time.Hour) }
	blocked := store.Authorize("s", store.Now())
	if blocked.Allowed || blocked.Windows[0].Started || blocked.Windows[1].Started || !blocked.Windows[2].Blocked {
		t.Fatalf("short window reset bypassed the amount limit: %+v", blocked)
	}
	store.now = func() time.Time { return now.Add(24 * time.Hour) }
	restored := store.Authorize("s", store.Now())
	if !restored.Allowed || restored.Windows[0].Dimensions[0].Used != "0" || restored.Windows[1].Dimensions[0].Used != "0" || restored.Windows[2].Dimensions[0].Used != "0" {
		t.Fatalf("windows did not recover after all exhausted quotas reset: %+v", restored)
	}
}

func TestAnchoredQuotaWindowsShareCalendarBoundaries(t *testing.T) {
	china := time.FixedZone("CST", 8*60*60)
	anchor := time.Date(2026, 9, 14, 0, 0, 0, 0, china)
	period := 7 * 24 * time.Hour
	store := newAccountStore(t, anchor)
	plan := Plan{ID: "weekly", Windows: []QuotaWindow{{
		ID: "weekly", Name: "每周额度", AmountUSD: 400, PeriodSeconds: int64(period / time.Second),
		CycleMode: QuotaCycleAnchored, AnchorAt: anchor,
	}}}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{plan}
		state.Keys["a"] = &KeyState{PlanID: plan.ID}
		state.Keys["b"] = &KeyState{PlanID: plan.ID}
	})

	before := store.Authorize("a", anchor.Add(-time.Minute))
	if before.Allowed || !before.Pending || !before.RetryAt.Equal(anchor) || len(store.state.Keys["a"].Cycles) != 0 {
		t.Fatalf("before anchor = %+v, cycles = %+v", before, store.state.Keys["a"].Cycles)
	}

	firstUse := anchor.Add(6 * time.Hour)
	for _, scope := range []string{"a", "b"} {
		decision := store.Authorize(scope, firstUse)
		cycle := store.state.Keys[scope].Cycles["weekly"]
		if !decision.Allowed || !cycle.StartAt.Equal(anchor) || !cycle.EndAt.Equal(anchor.Add(period)) {
			t.Fatalf("%s cycle = %+v, decision = %+v", scope, cycle, decision)
		}
	}

	store.RecordUsage(subsetEvent("a", firstUse))
	if store.state.Keys["a"].Cycles["weekly"].SpentUSD == 0 || store.state.Keys["b"].Cycles["weekly"].SpentUSD != 0 {
		t.Fatalf("key usage was not isolated: a=%+v b=%+v", store.state.Keys["a"].Cycles, store.state.Keys["b"].Cycles)
	}

	next := store.Authorize("a", anchor.Add(period))
	cycle := store.state.Keys["a"].Cycles["weekly"]
	if !next.Allowed || !cycle.StartAt.Equal(anchor.Add(period)) || !cycle.EndAt.Equal(anchor.Add(2*period)) {
		t.Fatalf("next anchored cycle = %+v, decision = %+v", cycle, next)
	}
}

func TestChangingQuotaWindowScheduleResetsBoundCycles(t *testing.T) {
	china := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, china)
	store := newAccountStore(t, now)
	plan := Plan{ID: "weekly", Windows: []QuotaWindow{{ID: "weekly", Name: "额度", AmountUSD: 400, PeriodSeconds: 7 * 24 * 3600}}}
	store.ReplaceAll(func(state *State) {
		state.Plans = []Plan{plan}
		state.Keys["key"] = &KeyState{PlanID: plan.ID}
	})
	if !store.Authorize("key", now).Allowed {
		t.Fatal("rolling quota did not allow its first request")
	}
	updated := clonePlan(plan)
	updated.Windows[0].CycleMode = QuotaCycleAnchored
	updated.Windows[0].AnchorAt = time.Date(2026, 9, 7, 0, 0, 0, 0, china)
	if _, err := store.UpdatePlanWithBindings(PlanPatch{ID: plan.ID, Windows: &updated.Windows}, nil); err != nil {
		t.Fatal(err)
	}
	if len(store.state.Keys["key"].Cycles) != 0 {
		t.Fatalf("schedule change retained a rolling cycle: %+v", store.state.Keys["key"].Cycles)
	}
}

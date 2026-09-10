package billing

import (
	"math"
	"testing"
	"time"
)

func TestQuotaWindowsValidationAndIdentity(t *testing.T) {
	input := []QuotaWindow{{Name: " Long ", AmountUSD: 10, PeriodSeconds: 7200}, {Name: "Short", AmountUSD: 2, PeriodSeconds: 3600}}
	windows, err := prepareWindows(input, nil)
	if err != nil || windows[0].Name != "Short" || windows[1].Name != "Long" || windows[0].ID == windows[1].ID {
		t.Fatalf("windows = %+v, %v", windows, err)
	}
	valid := Plan{ID: "p", Windows: windows}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	edited, err := prepareWindows(windows, windows)
	if err != nil || edited[0].ID != windows[0].ID {
		t.Fatal("editing changed window identity")
	}
	if _, err := prepareWindows(windows, nil); err == nil {
		t.Fatal("unknown IDs accepted")
	}
	anchor := time.Date(2026, 9, 14, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	for _, change := range []func(*Plan){
		func(p *Plan) { p.Windows = nil },
		func(p *Plan) { p.Windows[0].Name = " long " },
		func(p *Plan) { p.Windows[0].PeriodSeconds = 7200 },
		func(p *Plan) { p.Windows[0].PeriodSeconds = 0 },
		func(p *Plan) { p.Windows[0].PeriodSeconds = -1 },
		func(p *Plan) { p.Windows[0].PeriodSeconds = maxPeriodSeconds + 1 },
		func(p *Plan) { p.Windows[0].AmountUSD = math.NaN() },
		func(p *Plan) { p.Windows[0].AmountUSD = math.Inf(1) },
		func(p *Plan) { p.Windows[0].AmountUSD = 0 },
		func(p *Plan) { p.Windows[0].AmountUSD = -1 },
		func(p *Plan) { p.Windows[0].RequestLimit = -1 },
		func(p *Plan) { p.Windows[0].RequestLimit = maxQuotaCount + 1 },
		func(p *Plan) { p.Windows[0].TokenLimit = -1 },
		func(p *Plan) { p.Windows[0].TokenLimit = maxQuotaCount + 1 },
		func(p *Plan) { p.Windows[0].CycleMode = QuotaCycleAnchored },
		func(p *Plan) { p.Windows[0].AnchorAt = anchor },
		func(p *Plan) { p.Windows[0].CycleMode = "unsupported" },
	} {
		invalid := clonePlan(valid)
		change(&invalid)
		if invalid.Validate() == nil {
			t.Fatalf("invalid plan accepted: %+v", invalid)
		}
	}
	anchored := clonePlan(valid)
	anchored.Windows[0].CycleMode = QuotaCycleAnchored
	anchored.Windows[0].AnchorAt = anchor
	if err := anchored.Validate(); err != nil {
		t.Fatalf("anchored plan rejected: %v", err)
	}
}

func TestPlanAcceptsIndependentQuotaDimensions(t *testing.T) {
	plan := Plan{ID: "p", Windows: []QuotaWindow{
		{ID: "requests", Name: "请求", PeriodSeconds: 3600, RequestLimit: maxQuotaCount},
		{ID: "tokens", Name: "Token", PeriodSeconds: 86400, TokenLimit: maxQuotaCount},
		{ID: "mixed", Name: "综合", PeriodSeconds: 604800, AmountUSD: 100, TokenLimit: 10000, RequestLimit: 100},
	}}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
}

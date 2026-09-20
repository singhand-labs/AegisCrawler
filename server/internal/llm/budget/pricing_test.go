package budget

import (
	"errors"
	"math"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

func TestCategoryCostRoundsUpward(t *testing.T) {
	cases := []struct {
		name   string
		tokens int
		rate   config.USDNanos
		want   config.USDNanos
	}{
		{"zero tokens", 0, 1_000_000_000, 0},
		{"zero rate", 1_000_000, 0, 0},
		{"exact million", 1_000_000, 2_000_000_000, 2_000_000_000},
		{"exact half million", 500_000, 2_000_000_000, 1_000_000_000},
		{"single token rounds up", 1, 1_000_000_000, 1_000},
		{"sub nano rounds up to one", 1, 1, 1},
		{"one token past a million", 1_000_001, 1_000_000_000, 1_000_001_000},
		{"exact division", 3, 1_000_000, 3},
		{"remainder rounds up", 1_500_001, 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CategoryCost(tc.tokens, tc.rate)
			if err != nil {
				t.Fatalf("CategoryCost(%d, %d) returned error: %v", tc.tokens, tc.rate, err)
			}
			if got != tc.want {
				t.Fatalf("CategoryCost(%d, %d) = %d, want %d", tc.tokens, tc.rate, got, tc.want)
			}
		})
	}
}

func TestCategoryCostNeverRoundsDown(t *testing.T) {
	// Any nonzero cost must be at least one nano so a priced call is never free.
	for tokens := 1; tokens <= 32; tokens++ {
		got, err := CategoryCost(tokens, 1)
		if err != nil {
			t.Fatalf("CategoryCost(%d, 1) returned error: %v", tokens, err)
		}
		if got < 1 {
			t.Fatalf("CategoryCost(%d, 1) = %d, want at least 1", tokens, got)
		}
	}
}

func TestCategoryCostRejectsInvalidInput(t *testing.T) {
	if _, err := CategoryCost(-1, 1_000_000_000); err == nil {
		t.Fatal("expected negative token count to be rejected")
	}
	if _, err := CategoryCost(1, -1); err == nil {
		t.Fatal("expected negative rate to be rejected")
	}
}

func TestCategoryCostFailsClosedOnOverflow(t *testing.T) {
	_, err := CategoryCost(math.MaxInt32, config.USDNanos(math.MaxInt64))
	if !errors.Is(err, ErrPricingOverflow) {
		t.Fatalf("expected ErrPricingOverflow, got %v", err)
	}
}

func TestCategoryCostHandlesProductNearCeiling(t *testing.T) {
	// A product that is representable but close to the ceiling must not
	// overflow inside the ceiling-division step.
	rate := config.USDNanos(math.MaxInt64 / 1_000_000)
	got, err := CategoryCost(1_000_000, rate)
	if err != nil {
		t.Fatalf("CategoryCost near ceiling returned error: %v", err)
	}
	if got != rate {
		t.Fatalf("CategoryCost near ceiling = %d, want %d", got, rate)
	}
}

func TestAddFailsClosedOnOverflow(t *testing.T) {
	if _, err := Add(config.USDNanos(math.MaxInt64), 1); !errors.Is(err, ErrPricingOverflow) {
		t.Fatalf("expected ErrPricingOverflow, got %v", err)
	}
	if _, err := Add(-1); err == nil {
		t.Fatal("expected negative amount to be rejected")
	}
	total, err := Add(1, 2, 3)
	if err != nil || total != 6 {
		t.Fatalf("Add(1,2,3) = %d, %v; want 6, nil", total, err)
	}
}

func TestReservationUsesHigherInputRate(t *testing.T) {
	rates := Rates{
		InputUSDPerMillion:       600_000_000,
		CachedInputUSDPerMillion: 150_000_000,
		OutputUSDPerMillion:      2_000_000_000,
	}
	caps := Caps{MaxInputTokens: 100_000, MaxOutputTokens: 4_000}
	got, err := Reservation(rates, caps)
	if err != nil {
		t.Fatalf("Reservation returned error: %v", err)
	}
	// ceil(100000 * 600000000 / 1e6) +
	// ceil(4000 * 2000000000 / 1e6) + split-rounding headroom.
	want := config.USDNanos(60_000_000 + 8_000_000 + 1)
	if got != want {
		t.Fatalf("Reservation = %d, want %d", got, want)
	}
}

func TestReservationPrefersCachedRateWhenHigher(t *testing.T) {
	// A deliberately conservative configuration may price cached input above
	// uncached input. The reservation must use whichever is higher.
	rates := Rates{
		InputUSDPerMillion:       100_000_000,
		CachedInputUSDPerMillion: 900_000_000,
		OutputUSDPerMillion:      1_000_000,
	}
	caps := Caps{MaxInputTokens: 1_000_000, MaxOutputTokens: 1_000_000}
	got, err := Reservation(rates, caps)
	if err != nil {
		t.Fatalf("Reservation returned error: %v", err)
	}
	want := config.USDNanos(900_000_000 + 1_000_000 + 1)
	if got != want {
		t.Fatalf("Reservation = %d, want %d", got, want)
	}
}

func TestReservationRejectsNonPositiveCaps(t *testing.T) {
	rates := Rates{InputUSDPerMillion: 1, CachedInputUSDPerMillion: 1, OutputUSDPerMillion: 1}
	if _, err := Reservation(rates, Caps{MaxInputTokens: 0, MaxOutputTokens: 1}); err == nil {
		t.Fatal("expected zero input cap to be rejected")
	}
	if _, err := Reservation(rates, Caps{MaxInputTokens: 1, MaxOutputTokens: 0}); err == nil {
		t.Fatal("expected zero output cap to be rejected")
	}
}

func TestReservationFailsClosedOnOverflow(t *testing.T) {
	rates := Rates{
		InputUSDPerMillion:       config.USDNanos(math.MaxInt64),
		CachedInputUSDPerMillion: 1,
		OutputUSDPerMillion:      1,
	}
	caps := Caps{MaxInputTokens: math.MaxInt32, MaxOutputTokens: 1}
	if _, err := Reservation(rates, caps); !errors.Is(err, ErrPricingOverflow) {
		t.Fatalf("expected ErrPricingOverflow, got %v", err)
	}
}

func TestSettledCostChargesReportedCategories(t *testing.T) {
	rates := Rates{
		InputUSDPerMillion:       600_000_000,
		CachedInputUSDPerMillion: 150_000_000,
		OutputUSDPerMillion:      2_000_000_000,
	}
	usage := Usage{
		UncachedInputTokens: 10_000,
		CachedInputTokens:   40_000,
		OutputTokens:        1_000,
		CachedInputReported: true,
	}
	got, err := SettledCost(rates, usage)
	if err != nil {
		t.Fatalf("SettledCost returned error: %v", err)
	}
	want := config.USDNanos(6_000_000 + 6_000_000 + 2_000_000)
	if got != want {
		t.Fatalf("SettledCost = %d, want %d", got, want)
	}
}

func TestSettledCostChargesHigherInputRateWithoutCachedSplit(t *testing.T) {
	rates := Rates{
		InputUSDPerMillion:       600_000_000,
		CachedInputUSDPerMillion: 150_000_000,
		OutputUSDPerMillion:      2_000_000_000,
	}
	// The provider did not report a cached category, so the entire input must
	// be charged at the higher configured input rate.
	usage := Usage{UncachedInputTokens: 50_000, OutputTokens: 1_000}
	got, err := SettledCost(rates, usage)
	if err != nil {
		t.Fatalf("SettledCost returned error: %v", err)
	}
	want := config.USDNanos(30_000_000 + 2_000_000)
	if got != want {
		t.Fatalf("SettledCost = %d, want %d", got, want)
	}
}

func TestSettledCostNeverExceedsReservation(t *testing.T) {
	rates := Rates{
		InputUSDPerMillion:       600_000_000,
		CachedInputUSDPerMillion: 150_000_000,
		OutputUSDPerMillion:      2_000_000_000,
	}
	caps := Caps{MaxInputTokens: 100_000, MaxOutputTokens: 4_000}
	reservation, err := Reservation(rates, caps)
	if err != nil {
		t.Fatalf("Reservation returned error: %v", err)
	}
	// Usage at exactly the prepared bounds is the most expensive trusted
	// settlement possible and must still fit inside the reservation.
	usage := Usage{UncachedInputTokens: caps.MaxInputTokens, OutputTokens: caps.MaxOutputTokens}
	settled, err := SettledCost(rates, usage)
	if err != nil {
		t.Fatalf("SettledCost returned error: %v", err)
	}
	if settled > reservation {
		t.Fatalf("settled cost %d exceeds reservation %d", settled, reservation)
	}
}

func TestReservationCoversEveryValidCachedInputSplit(t *testing.T) {
	for _, rates := range []Rates{
		{
			InputUSDPerMillion:       0,
			CachedInputUSDPerMillion: 0,
			OutputUSDPerMillion:      0,
		},
		{
			InputUSDPerMillion:       1,
			CachedInputUSDPerMillion: 1,
			OutputUSDPerMillion:      1,
		},
		{
			InputUSDPerMillion:       7,
			CachedInputUSDPerMillion: 3,
			OutputUSDPerMillion:      11,
		},
		{
			InputUSDPerMillion:       3,
			CachedInputUSDPerMillion: 7,
			OutputUSDPerMillion:      11,
		},
		{
			InputUSDPerMillion:       999_999,
			CachedInputUSDPerMillion: 1_000_001,
			OutputUSDPerMillion:      1_000_000,
		},
		{
			InputUSDPerMillion:       1_000_001,
			CachedInputUSDPerMillion: 999_999,
			OutputUSDPerMillion:      1_000_000,
		},
	} {
		caps := Caps{MaxInputTokens: 32, MaxOutputTokens: 8}
		reservation, err := Reservation(rates, caps)
		if err != nil {
			t.Fatalf("Reservation(%+v) returned error: %v", rates, err)
		}
		for totalInput := 0; totalInput <= caps.MaxInputTokens; totalInput++ {
			for cached := 0; cached <= totalInput; cached++ {
				for output := 0; output <= caps.MaxOutputTokens; output++ {
					usage := Usage{
						UncachedInputTokens: totalInput - cached,
						CachedInputTokens:   cached,
						OutputTokens:        output,
						CachedInputReported: true,
					}
					settled, err := SettledCost(rates, usage)
					if err != nil {
						t.Fatalf("SettledCost(%+v, %+v) returned error: %v", rates, usage, err)
					}
					if settled > reservation {
						t.Fatalf(
							"valid usage %+v settled at %d above reservation %d for rates %+v",
							usage,
							settled,
							reservation,
							rates,
						)
					}
				}
			}
		}
	}
}

func TestReservationCoversIndependentInputCategoryRounding(t *testing.T) {
	rates := Rates{
		InputUSDPerMillion:       1,
		CachedInputUSDPerMillion: 1,
		OutputUSDPerMillion:      1,
	}
	caps := Caps{MaxInputTokens: 2, MaxOutputTokens: 1}
	reservation, err := Reservation(rates, caps)
	if err != nil {
		t.Fatalf("Reservation returned error: %v", err)
	}
	usage := Usage{
		UncachedInputTokens: 1,
		CachedInputTokens:   1,
		OutputTokens:        1,
		CachedInputReported: true,
	}
	settled, err := SettledCost(rates, usage)
	if err != nil {
		t.Fatalf("SettledCost returned error: %v", err)
	}
	if reservation != 3 || settled != 3 {
		t.Fatalf("reservation = %d, settlement = %d; want both 3", reservation, settled)
	}
}

func TestSettledCostRejectsNegativeUsage(t *testing.T) {
	rates := Rates{InputUSDPerMillion: 1, CachedInputUSDPerMillion: 1, OutputUSDPerMillion: 1}
	if _, err := SettledCost(rates, Usage{UncachedInputTokens: -1}); err == nil {
		t.Fatal("expected negative usage to be rejected")
	}
	if _, err := SettledCost(rates, Usage{OutputTokens: -1}); err == nil {
		t.Fatal("expected negative output usage to be rejected")
	}
}

func TestValidateUsage(t *testing.T) {
	caps := Caps{MaxInputTokens: 1_000, MaxOutputTokens: 100}
	cases := []struct {
		name              string
		usage             Usage
		unknownCategories []string
		wantErr           bool
	}{
		{"within bounds", Usage{UncachedInputTokens: 900, OutputTokens: 50}, nil, false},
		{"exactly at bounds", Usage{UncachedInputTokens: 1_000, OutputTokens: 100}, nil, false},
		{"split within bounds", Usage{UncachedInputTokens: 400, CachedInputTokens: 600, OutputTokens: 1, CachedInputReported: true}, nil, false},
		{"input over bound", Usage{UncachedInputTokens: 1_001, OutputTokens: 1}, nil, true},
		{"split sum over bound", Usage{UncachedInputTokens: 600, CachedInputTokens: 600, CachedInputReported: true}, nil, true},
		{"output over bound", Usage{UncachedInputTokens: 1, OutputTokens: 101}, nil, true},
		{"negative usage", Usage{UncachedInputTokens: -1}, nil, true},
		{"cached without category", Usage{CachedInputTokens: 10}, nil, true},
		{"unknown category", Usage{UncachedInputTokens: 1}, []string{"reasoning"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUsage(tc.usage, caps, tc.unknownCategories)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateUsageRejectsOverflowingInputSplit(t *testing.T) {
	err := ValidateUsage(
		Usage{
			UncachedInputTokens: math.MaxInt,
			CachedInputTokens:   1,
			CachedInputReported: true,
		},
		Caps{MaxInputTokens: math.MaxInt, MaxOutputTokens: 1},
		nil,
	)
	if err == nil {
		t.Fatal("overflowing cached and uncached input split was accepted")
	}
}

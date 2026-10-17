package main

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// RateSource is declared by its consumer and holds only the method it calls,
// so a test fake is five lines.
type RateSource interface {
	Rate(ctx context.Context, currency string) (float64, error)
}

type Converter struct {
	Rates    RateSource
	Timeout  time.Duration // per attempt
	Attempts int
	Backoff  time.Duration
}

func (c *Converter) Convert(ctx context.Context, cents int64, currency string) (int64, error) {
	var lastErr error
	for attempt := range c.Attempts {
		if attempt > 0 {
			select {
			case <-time.After(c.Backoff):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		actx, cancel := context.WithTimeout(ctx, c.Timeout)
		rate, err := c.Rates.Rate(actx, currency)
		cancel()
		if err == nil {
			return int64(math.Round(float64(cents) * rate)), nil
		}
		lastErr = err
	}
	return 0, fmt.Errorf("rate %s after %d attempts: %w", currency, c.Attempts, lastErr)
}

// ParseAmount parses untrusted input such as "12.34" into cents.
func ParseAmount(s string) (int64, error) {
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || (hasDot && (frac == "" || len(frac) > 2)) ||
		strings.Trim(whole+frac, "0123456789") != "" {
		return 0, fmt.Errorf("parse amount %q: want digits with up to 2 decimals", s)
	}
	n, err := strconv.ParseInt(whole+(frac + "00")[:2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse amount %q: %w", s, err)
	}
	return n, nil
}

func FormatCents(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

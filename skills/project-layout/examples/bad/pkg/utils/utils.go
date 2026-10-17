// MISTAKE: pkg/ sits outside internal/, so any module can import this and
// every change to it becomes a potential breaking change for strangers.
// MISTAKE: "utils" names no responsibility; money formatting and ID
// generation share no reason to change, yet everything imports both.
package utils

import "fmt"

func FormatCents(c int64) string { return fmt.Sprintf("$%d.%02d", c/100, c%100) }

func NewID(prefix string, n int) string { return fmt.Sprintf("%s_%d", prefix, n) }

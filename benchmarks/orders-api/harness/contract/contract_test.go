// Package contract checks that an orders-api implementation follows SPEC.md.
// It talks only HTTP, so it works for an implementation in any language:
//
//	BASE_URL=http://localhost:8080 go test ./...
//
// The pricing mock must be running: SKUs starting with X are unknown and
// SKUs starting with SLOW never answer in time.
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

var base = func() string {
	if v := os.Getenv("BASE_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}()

var client = &http.Client{Timeout: 20 * time.Second}

func do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, base+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func create(t *testing.T, customer int, items string) int64 {
	t.Helper()
	status, body := do(t, "POST", "/orders", fmt.Sprintf(`{"customer_id":%d,"items":%s}`, customer, items))
	if status != http.StatusCreated {
		t.Fatalf("POST /orders = %d %v, want 201", status, body)
	}
	id, ok := body["id"].(float64)
	if !ok {
		t.Fatalf("POST /orders body %v has no numeric id", body)
	}
	return int64(id)
}

func TestHealthz(t *testing.T) {
	if status, _ := do(t, "GET", "/healthz", ""); status != 200 {
		t.Fatalf("GET /healthz = %d", status)
	}
}

func TestCreateAndGet(t *testing.T) {
	id := create(t, 900001, `[{"sku":"A1","qty":2},{"sku":"B2","qty":1}]`)
	status, body := do(t, "GET", fmt.Sprintf("/orders/%d", id), "")
	if status != 200 {
		t.Fatalf("GET order = %d", status)
	}
	if body["customer_id"] != float64(900001) || body["status"] != "new" {
		t.Errorf("order = %v", body)
	}
	if _, err := time.Parse(time.RFC3339, fmt.Sprint(body["created_at"])); err != nil {
		t.Errorf("created_at %v is not RFC 3339", body["created_at"])
	}
	if items, _ := body["items"].([]any); len(items) != 2 {
		t.Errorf("items = %v, want 2", body["items"])
	}
}

func TestCreateRejectsInvalid(t *testing.T) {
	for name, body := range map[string]string{
		"no items":       `{"customer_id":1,"items":[]}`,
		"zero qty":       `{"customer_id":1,"items":[{"sku":"A","qty":0}]}`,
		"duplicate sku":  `{"customer_id":1,"items":[{"sku":"A","qty":1},{"sku":"A","qty":2}]}`,
		"bad customer":   `{"customer_id":0,"items":[{"sku":"A","qty":1}]}`,
		"not json":       `{"customer_id":`,
		"too many items": tooMany(),
	} {
		t.Run(name, func(t *testing.T) {
			if status, _ := do(t, "POST", "/orders", body); status != 400 {
				t.Errorf("status = %d, want 400", status)
			}
		})
	}
}

func tooMany() string {
	var b bytes.Buffer
	b.WriteString(`{"customer_id":1,"items":[`)
	for i := range 101 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"sku":"S%d","qty":1}`, i)
	}
	b.WriteString("]}")
	return b.String()
}

func TestGetUnknown(t *testing.T) {
	if status, _ := do(t, "GET", "/orders/999999999", ""); status != 404 {
		t.Errorf("status = %d, want 404", status)
	}
}

func TestListPaginates(t *testing.T) {
	const customer = 900002
	for range 5 {
		create(t, customer, `[{"sku":"A1","qty":1}]`)
	}
	seen := map[float64]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		status, body := do(t, "GET", fmt.Sprintf("/customers/%d/orders?limit=2&cursor=%s", customer, cursor), "")
		if status != 200 {
			t.Fatalf("list = %d %v", status, body)
		}
		orders, _ := body["orders"].([]any)
		for _, o := range orders {
			m := o.(map[string]any)
			if items, _ := m["items"].([]any); len(items) != 1 {
				t.Errorf("order %v has items %v, want 1", m["id"], m["items"])
			}
			seen[m["id"].(float64)] = true
		}
		cursor, _ = body["next_cursor"].(string)
		if cursor == "" {
			break
		}
	}
	if len(seen) < 5 {
		t.Errorf("saw %d distinct orders across pages, want at least 5", len(seen))
	}
}

func TestQuote(t *testing.T) {
	id := create(t, 900003, `[{"sku":"A1","qty":2}]`)
	status, body := do(t, "GET", fmt.Sprintf("/orders/%d/quote", id), "")
	if status != 200 {
		t.Fatalf("quote = %d %v", status, body)
	}
	if total, _ := body["total_cents"].(float64); total <= 0 {
		t.Errorf("total_cents = %v", body["total_cents"])
	}
}

func TestQuoteUnknownSKU(t *testing.T) {
	id := create(t, 900004, `[{"sku":"A1","qty":1},{"sku":"XNOPE","qty":1}]`)
	if status, _ := do(t, "GET", fmt.Sprintf("/orders/%d/quote", id), ""); status != 422 {
		t.Errorf("status = %d, want 422", status)
	}
}

func TestQuoteSlowPricing(t *testing.T) {
	id := create(t, 900005, `[{"sku":"SLOW1","qty":1}]`)
	start := time.Now()
	status, _ := do(t, "GET", fmt.Sprintf("/orders/%d/quote", id), "")
	if status != 503 {
		t.Errorf("status = %d after %v, want 503", status, time.Since(start))
	}
}

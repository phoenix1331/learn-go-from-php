package shop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// HTTPPayments is the real Payments, talking to a provider's HTTP API.
type HTTPPayments struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

// This line does nothing at runtime. It makes the build fail if
// *HTTPPayments ever stops satisfying Payments, even with no caller.
var _ Payments = (*HTTPPayments)(nil)

func (p *HTTPPayments) Charge(ctx context.Context, key string, pence int64) (string, error) {
	body, err := json.Marshal(map[string]int64{"pence": pence})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/charges", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)

	resp, err := p.send(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusCreated:
		var out struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", fmt.Errorf("decoding charge: %w", err)
		}
		return out.ID, nil
	case resp.StatusCode == http.StatusPaymentRequired:
		return "", ErrDeclined
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	default:
		return "", fmt.Errorf("charge: unexpected status %d", resp.StatusCode)
	}
}

func (p *HTTPPayments) Refund(ctx context.Context, chargeID string) error {
	target := p.BaseURL + "/charges/" + url.PathEscape(chargeID) + "/refund"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return err
	}
	resp, err := p.send(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refund: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// send adds auth and treats a failed connection as an outage. The context's
// error stays wrapped too, so errors.Is(err, context.Canceled) still works.
func (p *HTTPPayments) send(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return resp, nil
}

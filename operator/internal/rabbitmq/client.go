// Package rabbitmq is a small client for the RabbitMQ HTTP management API.
//
// It covers only the calls the operator makes. Every write is a PUT, which the
// management API treats as create-or-update, so the reconciler can replay the
// same sequence on every pass without tracking what it created before.
package rabbitmq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Client talks to one broker.
type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

// New returns a client for a management API at baseURL, for example
// "http://rabbit-ha.messaging.svc:15672".
func New(baseURL, username, password string) *Client {
	return &Client{
		baseURL:  baseURL,
		username: username,
		password: password,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// do makes one request. A body of nil sends no body.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, 0, fmt.Errorf("encode body: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, &buf)
	if err != nil {
		return nil, 0, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	out := new(bytes.Buffer)
	if _, err := out.ReadFrom(resp.Body); err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return out.Bytes(), resp.StatusCode, fmt.Errorf("%s %s: %s: %s",
			method, path, resp.Status, out.String())
	}
	return out.Bytes(), resp.StatusCode, nil
}

// esc encodes one path segment. The default vhost "/" must become "%2F".
func esc(s string) string { return url.PathEscape(s) }

// EnsureVhost creates the vhost, or does nothing if it exists.
func (c *Client) EnsureVhost(ctx context.Context, vhost string) error {
	_, _, err := c.do(ctx, http.MethodPut, "/api/vhosts/"+esc(vhost), map[string]any{})
	return err
}

// EnsureExchange creates a durable topic exchange.
func (c *Client) EnsureExchange(ctx context.Context, vhost, name string) error {
	_, _, err := c.do(ctx, http.MethodPut,
		fmt.Sprintf("/api/exchanges/%s/%s", esc(vhost), esc(name)),
		map[string]any{"type": "topic", "durable": true})
	return err
}

// EnsureQueue creates a durable quorum queue. Pass deadLetterTo to route
// rejected messages to another queue through the default exchange.
func (c *Client) EnsureQueue(ctx context.Context, vhost, name, deadLetterTo string) error {
	args := map[string]any{"x-queue-type": "quorum"}
	if deadLetterTo != "" {
		// The empty exchange is the default exchange, which routes by queue
		// name. This avoids a separate dead letter exchange per queue.
		args["x-dead-letter-exchange"] = ""
		args["x-dead-letter-routing-key"] = deadLetterTo
	}
	_, _, err := c.do(ctx, http.MethodPut,
		fmt.Sprintf("/api/queues/%s/%s", esc(vhost), esc(name)),
		map[string]any{"durable": true, "arguments": args})
	return err
}

// EnsureBinding binds a queue to an exchange with a routing key.
//
// The management API creates bindings with POST, which is not idempotent and
// would add a duplicate on every reconcile. So read the bindings first and
// post only when the routing key is absent.
func (c *Client) EnsureBinding(ctx context.Context, vhost, exchange, queue, routingKey string) error {
	path := fmt.Sprintf("/api/bindings/%s/e/%s/q/%s", esc(vhost), esc(exchange), esc(queue))

	raw, _, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var existing []struct {
		RoutingKey string `json:"routing_key"`
	}
	if err := json.Unmarshal(raw, &existing); err != nil {
		return fmt.Errorf("decode bindings: %w", err)
	}
	for _, b := range existing {
		if b.RoutingKey == routingKey {
			return nil
		}
	}

	_, _, err = c.do(ctx, http.MethodPost, path, map[string]any{"routing_key": routingKey})
	return err
}

// EnsureUser creates the user, or resets its password if it exists.
func (c *Client) EnsureUser(ctx context.Context, name, password string) error {
	_, _, err := c.do(ctx, http.MethodPut, "/api/users/"+esc(name),
		map[string]any{"password": password, "tags": ""})
	return err
}

// SetPermissions scopes a user to one vhost. The user may publish and consume.
// An empty configure pattern stops it creating or deleting topology.
func (c *Client) SetPermissions(ctx context.Context, vhost, user string) error {
	_, _, err := c.do(ctx, http.MethodPut,
		fmt.Sprintf("/api/permissions/%s/%s", esc(vhost), esc(user)),
		map[string]any{"configure": "^$", "write": ".*", "read": ".*"})
	return err
}

// QueueInfo is the live state of a quorum queue.
type QueueInfo struct {
	Leader  string   `json:"leader"`
	Members []string `json:"members"`
}

// GetQueue reads the live state of a queue.
func (c *Client) GetQueue(ctx context.Context, vhost, name string) (*QueueInfo, error) {
	raw, _, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/queues/%s/%s", esc(vhost), esc(name)), nil)
	if err != nil {
		return nil, err
	}
	info := new(QueueInfo)
	if err := json.Unmarshal(raw, info); err != nil {
		return nil, fmt.Errorf("decode queue: %w", err)
	}
	return info, nil
}

// DeleteQueue removes a queue. A queue that is already gone is not an error.
func (c *Client) DeleteQueue(ctx context.Context, vhost, name string) error {
	_, status, err := c.do(ctx, http.MethodDelete,
		fmt.Sprintf("/api/queues/%s/%s", esc(vhost), esc(name)), nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// DeleteExchange removes an exchange. An exchange that is already gone is not
// an error.
func (c *Client) DeleteExchange(ctx context.Context, vhost, name string) error {
	_, status, err := c.do(ctx, http.MethodDelete,
		fmt.Sprintf("/api/exchanges/%s/%s", esc(vhost), esc(name)), nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// DeleteUser revokes a user. A user that is already gone is not an error.
func (c *Client) DeleteUser(ctx context.Context, name string) error {
	_, status, err := c.do(ctx, http.MethodDelete, "/api/users/"+esc(name), nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

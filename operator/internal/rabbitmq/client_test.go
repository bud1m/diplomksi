package rabbitmq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// call records one request the client made.
type call struct {
	method string
	path   string
	body   map[string]any
}

// recorder is a fake management API. It records every call and replies with
// whatever the test queued up.
type recorder struct {
	calls   []call
	replies map[string]string // "METHOD path" -> JSON body
	status  map[string]int    // "METHOD path" -> status code
}

func newRecorder() *recorder {
	return &recorder{
		replies: map[string]string{},
		status:  map[string]int{},
	}
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	c := call{method: req.Method, path: req.URL.EscapedPath()}
	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&c.body)
	}
	r.calls = append(r.calls, c)

	key := req.Method + " " + req.URL.EscapedPath()
	if code, ok := r.status[key]; ok {
		w.WriteHeader(code)
	}
	if body, ok := r.replies[key]; ok {
		_, _ = w.Write([]byte(body))
		return
	}
	_, _ = w.Write([]byte("{}"))
}

func (r *recorder) find(method, path string) *call {
	for i := range r.calls {
		if r.calls[i].method == method && r.calls[i].path == path {
			return &r.calls[i]
		}
	}
	return nil
}

func (r *recorder) count(method, path string) int {
	n := 0
	for _, c := range r.calls {
		if c.method == method && c.path == path {
			n++
		}
	}
	return n
}

// setup returns a client pointed at a fake broker.
func setup(t *testing.T) (*Client, *recorder) {
	t.Helper()
	rec := newRecorder()
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return New(srv.URL, "guest", "guest"), rec
}

func TestEnsureQueueRequestsQuorumType(t *testing.T) {
	client, rec := setup(t)

	if err := client.EnsureQueue(context.Background(), "orders", "orders.created", "orders.created.dlq"); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}

	c := rec.find(http.MethodPut, "/api/queues/orders/orders.created")
	if c == nil {
		t.Fatal("no PUT to the queue path")
	}
	if c.body["durable"] != true {
		t.Errorf("durable = %v, want true", c.body["durable"])
	}

	args, ok := c.body["arguments"].(map[string]any)
	if !ok {
		t.Fatalf("arguments missing, body = %v", c.body)
	}
	// A classic queue here would defeat the whole thesis. Assert the type.
	if args["x-queue-type"] != "quorum" {
		t.Errorf("x-queue-type = %v, want quorum", args["x-queue-type"])
	}
	// The empty exchange is the default exchange, which routes by queue name.
	if args["x-dead-letter-exchange"] != "" {
		t.Errorf("x-dead-letter-exchange = %v, want the empty string", args["x-dead-letter-exchange"])
	}
	if args["x-dead-letter-routing-key"] != "orders.created.dlq" {
		t.Errorf("x-dead-letter-routing-key = %v", args["x-dead-letter-routing-key"])
	}
}

func TestEnsureQueueWithoutDeadLetterSetsNoDeadLetterArguments(t *testing.T) {
	client, rec := setup(t)

	// The dead letter queue itself must not dead letter anywhere, or a
	// rejected message loops between the two queues.
	if err := client.EnsureQueue(context.Background(), "orders", "orders.created.dlq", ""); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}

	c := rec.find(http.MethodPut, "/api/queues/orders/orders.created.dlq")
	args := c.body["arguments"].(map[string]any)
	if _, found := args["x-dead-letter-exchange"]; found {
		t.Error("the dead letter queue must not carry a dead letter exchange")
	}
	if args["x-queue-type"] != "quorum" {
		t.Errorf("x-queue-type = %v, want quorum", args["x-queue-type"])
	}
}

func TestEnsureBindingSkipsAnExistingRoutingKey(t *testing.T) {
	client, rec := setup(t)
	path := "/api/bindings/orders/e/orders-service/q/orders.created"
	rec.replies["GET "+path] = `[{"routing_key":"order.created"}]`

	if err := client.EnsureBinding(context.Background(),
		"orders", "orders-service", "orders.created", "order.created"); err != nil {
		t.Fatalf("EnsureBinding: %v", err)
	}

	// The management API creates bindings with POST, which is not idempotent.
	// A second POST would add a duplicate binding on every reconcile.
	if n := rec.count(http.MethodPost, path); n != 0 {
		t.Errorf("POST count = %d, want 0 for a binding that already exists", n)
	}
}

func TestEnsureBindingCreatesAMissingRoutingKey(t *testing.T) {
	client, rec := setup(t)
	path := "/api/bindings/orders/e/orders-service/q/orders.created"
	rec.replies["GET "+path] = `[{"routing_key":"order.other"}]`

	if err := client.EnsureBinding(context.Background(),
		"orders", "orders-service", "orders.created", "order.created"); err != nil {
		t.Fatalf("EnsureBinding: %v", err)
	}

	c := rec.find(http.MethodPost, path)
	if c == nil {
		t.Fatal("no POST for the missing binding")
	}
	if c.body["routing_key"] != "order.created" {
		t.Errorf("routing_key = %v", c.body["routing_key"])
	}
}

func TestSetPermissionsDeniesTopologyChanges(t *testing.T) {
	client, rec := setup(t)

	if err := client.SetPermissions(context.Background(), "orders", "shop-orders"); err != nil {
		t.Fatalf("SetPermissions: %v", err)
	}

	c := rec.find(http.MethodPut, "/api/permissions/orders/shop-orders")
	// An empty configure pattern is what stops the microservice creating or
	// deleting queues. It may only publish and consume.
	if c.body["configure"] != "^$" {
		t.Errorf("configure = %v, want ^$", c.body["configure"])
	}
	if c.body["write"] != ".*" || c.body["read"] != ".*" {
		t.Errorf("write/read = %v/%v, want .*/.*", c.body["write"], c.body["read"])
	}
}

func TestDeleteIsNotAnErrorWhenTheObjectIsGone(t *testing.T) {
	client, rec := setup(t)
	rec.status["DELETE /api/users/shop-orders"] = http.StatusNotFound
	rec.status["DELETE /api/queues/orders/orders.created"] = http.StatusNotFound

	// Cleanup runs on every requeue. A second pass must not block the
	// finalizer just because the first pass already removed the object.
	if err := client.DeleteUser(context.Background(), "shop-orders"); err != nil {
		t.Errorf("DeleteUser on a missing user: %v", err)
	}
	if err := client.DeleteQueue(context.Background(), "orders", "orders.created"); err != nil {
		t.Errorf("DeleteQueue on a missing queue: %v", err)
	}
}

func TestDeleteReportsARealFailure(t *testing.T) {
	client, rec := setup(t)
	rec.status["DELETE /api/users/shop-orders"] = http.StatusInternalServerError

	if err := client.DeleteUser(context.Background(), "shop-orders"); err == nil {
		t.Error("a 500 must be an error, or the finalizer drops a live user")
	}
}

func TestGetQueueReadsTheRaftMembers(t *testing.T) {
	client, rec := setup(t)
	rec.replies["GET /api/queues/orders/orders.created"] =
		`{"leader":"rabbit@server-1","members":["rabbit@server-0","rabbit@server-1","rabbit@server-2"]}`

	info, err := client.GetQueue(context.Background(), "orders", "orders.created")
	if err != nil {
		t.Fatalf("GetQueue: %v", err)
	}
	if info.Leader != "rabbit@server-1" {
		t.Errorf("leader = %q", info.Leader)
	}
	if len(info.Members) != 3 {
		t.Errorf("members = %d, want 3", len(info.Members))
	}
}

func TestDefaultVhostIsEscaped(t *testing.T) {
	client, rec := setup(t)

	// The default vhost is named "/" and must travel as %2F, or the request
	// hits a different path entirely.
	if err := client.EnsureVhost(context.Background(), "/"); err != nil {
		t.Fatalf("EnsureVhost: %v", err)
	}
	if rec.find(http.MethodPut, "/api/vhosts/%2F") == nil {
		t.Errorf("vhost path not escaped, calls = %v", rec.calls)
	}
}

func TestAnErrorCarriesTheBrokerMessage(t *testing.T) {
	client, rec := setup(t)
	rec.status["PUT /api/vhosts/orders"] = http.StatusBadRequest
	rec.replies["PUT /api/vhosts/orders"] = `{"error":"bad name"}`

	err := client.EnsureVhost(context.Background(), "orders")
	if err == nil {
		t.Fatal("want an error")
	}
	// The reconciler puts this text into the Ready condition, so it has to
	// say what the broker actually complained about.
	if got := err.Error(); !strings.Contains(got, "bad name") {
		t.Errorf("error = %q, want it to carry the broker message", got)
	}
}
